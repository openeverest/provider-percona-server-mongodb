// Copyright (C) 2026 The OpenEverest Contributors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package provider

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/yaml"

	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	monitoringv1alpha1 "github.com/openeverest/openeverest/v2/api/monitoring/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
)

const (
	bindingTestInstance = "my-mongo"
	bindingTestNS       = "db"
)

// bindingContext builds a context for an Instance whose spec.monitoring lists
// entries; objs are the bindings, destinations, classes, secrets and engine
// CR that make them resolvable.
func bindingContext(t *testing.T, entries []corev1alpha1.InstanceMonitoringDestination, objs ...client.Object) (*controller.Context, client.Client) {
	t.Helper()

	return bindingContextWith(t, interceptor.Funcs{}, entries, objs...)
}

// bindingContextWith is bindingContext over a client whose calls go through
// funcs.
func bindingContextWith(
	t *testing.T,
	funcs interceptor.Funcs,
	entries []corev1alpha1.InstanceMonitoringDestination,
	objs ...client.Object,
) (*controller.Context, client.Client) {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, monitoringv1alpha1.AddToScheme(scheme))
	require.NoError(t, psmdbv1.SchemeBuilder.AddToScheme(scheme))

	in := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: bindingTestInstance, Namespace: bindingTestNS, UID: "uid"},
		Spec: corev1alpha1.InstanceSpec{
			Components: map[string]corev1alpha1.ComponentSpec{
				common.ComponentEngine: {
					Resources: &corev1.ResourceRequirements{
						Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1G")},
					},
				},
			},
		},
	}
	if entries != nil {
		in.Spec.Monitoring = &corev1alpha1.InstanceMonitoringSpec{Destinations: entries}
	}
	provider := &corev1alpha1.Provider{
		ObjectMeta: metav1.ObjectMeta{Name: "psmdb"},
		Spec:       loadProviderSpec(t),
	}
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(append(objs, in, provider)...).
		WithInterceptorFuncs(funcs).
		Build()
	return controller.NewContext(context.Background(), cl, in, "psmdb"), cl
}

func entry(name, destination string) corev1alpha1.InstanceMonitoringDestination {
	return corev1alpha1.InstanceMonitoringDestination{
		Name:           name,
		DestinationRef: commonv1alpha1.MonitoringDestinationRef{Name: destination},
	}
}

// acceptedBinding is what core materialises for entry once it has resolved
// destination, class and mode.
func acceptedBinding(entryName, destination, class string, mode corev1alpha1.MonitoringExecutionMode) *monitoringv1alpha1.MonitoringBinding {
	b := &monitoringv1alpha1.MonitoringBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:       bindingTestInstance + "-" + entryName,
			Namespace:  bindingTestNS,
			Generation: 1,
			Labels: map[string]string{
				monitoringv1alpha1.BindingInstanceLabel: bindingTestInstance,
				monitoringv1alpha1.BindingEntryLabel:    entryName,
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: corev1alpha1.GroupVersion.String(),
				Kind:       "Instance",
				Name:       bindingTestInstance,
				UID:        "uid",
				Controller: new(true),
			}},
		},
		Spec: monitoringv1alpha1.MonitoringBindingSpec{
			InstanceRef:    commonv1alpha1.ObjectRef{Name: bindingTestInstance},
			DestinationRef: commonv1alpha1.MonitoringDestinationRef{Name: destination},
			ClassRef:       &commonv1alpha1.ObjectRef{Name: class},
			ExecutionMode:  mode,
		},
	}
	b.Status.Conditions = []metav1.Condition{{
		Type: monitoringv1alpha1.MonitoringBindingConditionAccepted, Status: metav1.ConditionTrue,
		Reason: monitoringv1alpha1.ReasonAccepted, ObservedGeneration: 1,
	}}
	return b
}

func metricsClass(name string) *monitoringv1alpha1.MonitoringClass {
	return &monitoringv1alpha1.MonitoringClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: monitoringv1alpha1.MonitoringClassSpec{
			ControllerName: "openeverest.io/" + name,
			ExtensionManaged: &monitoringv1alpha1.ExtensionManagedFulfilment{
				Requires: monitoringv1alpha1.SourceRequirements{Metrics: true},
			},
		},
	}
}

func pmmClass(agentImage string) *monitoringv1alpha1.MonitoringClass {
	return &monitoringv1alpha1.MonitoringClass{
		ObjectMeta: metav1.ObjectMeta{Name: "pmm"},
		Spec: monitoringv1alpha1.MonitoringClassSpec{
			ControllerName: "openeverest.io/monitoring-pmm",
			ProviderManaged: &monitoringv1alpha1.ProviderManagedFulfilment{
				Integration: pmmIntegration,
				AgentImage:  agentImage,
			},
		},
	}
}

func destination(name, class string, parameters string, secret string) *monitoringv1alpha1.MonitoringDestination {
	d := &monitoringv1alpha1.MonitoringDestination{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: bindingTestNS},
		Spec:       monitoringv1alpha1.MonitoringDestinationSpec{ClassRef: commonv1alpha1.ObjectRef{Name: class}},
	}
	if parameters != "" {
		d.Spec.Parameters = &runtime.RawExtension{Raw: []byte(parameters)}
	}
	if secret != "" {
		d.Spec.CredentialsSecretRef = &commonv1alpha1.SecretRef{Name: secret}
	}
	return d
}

// livePSMDB is the engine CR as the operator currently sees it. A ready
// cluster reports every pod of every component ready, any other state none.
func livePSMDB(state psmdbv1.AppState, sharded, withExporter bool) *psmdbv1.PerconaServerMongoDB {
	var ready int32
	if state == psmdbv1.AppStateReady {
		ready = 3
	}
	psmdb := &psmdbv1.PerconaServerMongoDB{
		ObjectMeta: metav1.ObjectMeta{Name: bindingTestInstance, Namespace: bindingTestNS},
		Spec: psmdbv1.PerconaServerMongoDBSpec{
			Replsets: []*psmdbv1.ReplsetSpec{{Name: "rs0"}},
			PMM:      psmdbv1.PMMSpec{Enabled: true, ServerHost: "live-pmm.example.com", Image: "percona/pmm-client:3.5.0"},
		},
		Status: psmdbv1.PerconaServerMongoDBStatus{
			State:    state,
			Replsets: map[string]psmdbv1.ReplsetStatus{"rs0": {Size: 3, Ready: ready}},
		},
	}
	if sharded {
		psmdb.Spec.Sharding = psmdbv1.Sharding{
			Enabled:          true,
			ConfigsvrReplSet: &psmdbv1.ReplsetSpec{Name: psmdbv1.ConfigReplSetName},
			Mongos:           &psmdbv1.MongosSpec{},
		}
		psmdb.Status.Replsets[psmdbv1.ConfigReplSetName] = psmdbv1.ReplsetStatus{Size: 3, Ready: ready}
		psmdb.Status.Mongos = &psmdbv1.MongosStatus{Size: 3, Ready: int(ready)}
	}
	if withExporter {
		psmdb.Spec.Replsets[0].Sidecars = []corev1.Container{{Name: metricsExporterContainerName}}
	}
	return psmdb
}

func TestMonitoringIntegrations(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Clean("../../definition/monitoring.yaml"))
	require.NoError(t, err)
	var contract struct {
		Integrations map[string]any `json:"integrations"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &contract))

	assert.ElementsMatch(t, slices.Collect(maps.Keys(contract.Integrations)), (&PSMDBProvider{}).MonitoringIntegrations(),
		"the provider renders exactly the integrations its contract declares")
}

func TestMetricsExporterRequired(t *testing.T) {
	t.Parallel()

	t.Run("an ExtensionManaged binding whose class requires metrics requires the exporter", func(t *testing.T) {
		t.Parallel()

		c, _ := bindingContext(t, []corev1alpha1.InstanceMonitoringDestination{entry("prom", "prom-dst")},
			acceptedBinding("prom", "prom-dst", "prometheus", corev1alpha1.MonitoringExecutionModeExtensionManaged),
			destination("prom-dst", "prometheus", "", ""),
			metricsClass("prometheus"),
		)

		required, err := metricsExporterRequired(c)
		require.NoError(t, err)
		assert.True(t, required)
	})

	t.Run("a ProviderManaged pmm binding alone does not", func(t *testing.T) {
		t.Parallel()

		c, _ := bindingContext(t, []corev1alpha1.InstanceMonitoringDestination{entry("pmm", "pmm-dst")},
			acceptedBinding("pmm", "pmm-dst", "pmm", corev1alpha1.MonitoringExecutionModeProviderManaged),
			destination("pmm-dst", "pmm", `{"url":"https://pmm.example.com"}`, "pmm-token"),
			pmmClass("percona/pmm-client:3.8.0"),
			livePSMDB(psmdbv1.AppStateReady, false, true),
		)

		required, err := metricsExporterRequired(c)
		require.NoError(t, err)
		assert.False(t, required, "resolved without a metrics class: the live sidecar is not held")
	})

	t.Run("no entries means no exporter", func(t *testing.T) {
		t.Parallel()

		c, _ := bindingContext(t, nil, livePSMDB(psmdbv1.AppStateReady, false, true))

		required, err := metricsExporterRequired(c)
		require.NoError(t, err)
		assert.False(t, required)
	})

	t.Run("while an entry has no binding yet the live sidecar is held", func(t *testing.T) {
		t.Parallel()

		c, _ := bindingContext(t, []corev1alpha1.InstanceMonitoringDestination{entry("prom", "prom-dst")},
			livePSMDB(psmdbv1.AppStateReady, false, true),
		)

		required, err := metricsExporterRequired(c)
		require.NoError(t, err)
		assert.True(t, required, "hold rule")
	})

	t.Run("while an entry has no binding yet and nothing is live, nothing is added", func(t *testing.T) {
		t.Parallel()

		c, _ := bindingContext(t, []corev1alpha1.InstanceMonitoringDestination{entry("prom", "prom-dst")})

		required, err := metricsExporterRequired(c)
		require.NoError(t, err)
		assert.False(t, required)
	})

	t.Run("configureMetricsExporter follows the requirement", func(t *testing.T) {
		t.Parallel()

		c, _ := bindingContext(t, []corev1alpha1.InstanceMonitoringDestination{entry("prom", "prom-dst")},
			acceptedBinding("prom", "prom-dst", "prometheus", corev1alpha1.MonitoringExecutionModeExtensionManaged),
			destination("prom-dst", "prometheus", "", ""),
			metricsClass("prometheus"),
		)
		psmdb := shardedPSMDB()

		require.NoError(t, configureMetricsExporter(c, psmdb, metricsTestUsersSecret))

		for _, rs := range psmdb.Spec.Replsets {
			exporterSidecar(t, rs.Sidecars)
		}
		exporterSidecar(t, psmdb.Spec.Sharding.ConfigsvrReplSet.Sidecars)
		exporterSidecar(t, psmdb.Spec.Sharding.Mongos.Sidecars)
	})
}

func TestMonitoringSources(t *testing.T) {
	t.Parallel()

	p := &PSMDBProvider{}
	endpointComponents := func(s *corev1alpha1.MonitoringSources) []string {
		out := make([]string, 0, len(s.Metrics))
		for _, ep := range s.Metrics {
			out = append(out, ep.Component)
		}
		return out
	}

	t.Run("publishes one endpoint per component once the cluster is ready", func(t *testing.T) {
		t.Parallel()

		c, _ := bindingContext(t, nil, livePSMDB(psmdbv1.AppStateReady, true, true))

		s, err := p.MonitoringSources(c)
		require.NoError(t, err)
		assert.Equal(t, []string{common.ComponentEngine, common.ComponentConfigServer, common.ComponentProxy}, endpointComponents(s))
		for _, ep := range s.Metrics {
			assert.Equal(t, "mongodb", ep.Kind)
			assert.Equal(t, intstr.FromString(metricsPortName), ep.Port)
			assert.Equal(t, metricsPath, ep.Path)
			assert.Equal(t, "http", ep.Scheme)
			assert.Equal(t, map[string]string{
				controller.InstanceLabel:  bindingTestInstance,
				controller.ComponentLabel: ep.Component,
			}, ep.PodSelector, "each endpoint selects only its own component's pods")
		}
	})

	t.Run("a replica set exposes only the engine", func(t *testing.T) {
		t.Parallel()

		c, _ := bindingContext(t, nil, livePSMDB(psmdbv1.AppStateReady, false, true))

		s, err := p.MonitoringSources(c)
		require.NoError(t, err)
		assert.Equal(t, []string{common.ComponentEngine}, endpointComponents(s))
	})

	t.Run("nothing is published without the exporter, before a pod is ready or before the CR exists", func(t *testing.T) {
		t.Parallel()

		for name, objs := range map[string][]client.Object{
			"no exporter":  {livePSMDB(psmdbv1.AppStateReady, true, false)},
			"no ready pod": {livePSMDB(psmdbv1.AppStateInit, true, true)},
			"no CR":        {},
		} {
			c, _ := bindingContext(t, nil, objs...)

			s, err := p.MonitoringSources(c)
			require.NoError(t, err, name)
			assert.Empty(t, s.Metrics, name)
		}
	})

	t.Run("an API error reading the engine CR is returned, not published as no endpoints", func(t *testing.T) {
		t.Parallel()

		errAPI := apierrors.NewServiceUnavailable("apiserver is restarting")
		c, _ := bindingContextWith(t, interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*psmdbv1.PerconaServerMongoDB); ok {
					return errAPI
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}, nil, livePSMDB(psmdbv1.AppStateReady, false, true))

		_, err := p.MonitoringSources(c)
		require.ErrorIs(t, err, errAPI, "the runtime keeps the published sources on an error")
	})

	t.Run("a rolling restart keeps the endpoints of components with a ready pod", func(t *testing.T) {
		t.Parallel()

		// restarting is a sharded cluster the operator reports initializing
		// with this many of three pods ready per component.
		restarting := func(engine, configServer, proxy int32) *psmdbv1.PerconaServerMongoDB {
			psmdb := livePSMDB(psmdbv1.AppStateInit, true, true)
			psmdb.Status.Replsets["rs0"] = psmdbv1.ReplsetStatus{Size: 3, Ready: engine}
			psmdb.Status.Replsets[psmdbv1.ConfigReplSetName] = psmdbv1.ReplsetStatus{Size: 3, Ready: configServer}
			psmdb.Status.Mongos.Ready = int(proxy)
			return psmdb
		}
		for name, tc := range map[string]struct {
			live *psmdbv1.PerconaServerMongoDB
			want []string
		}{
			"one pod of each component restarting": {
				restarting(2, 2, 2),
				[]string{common.ComponentEngine, common.ComponentConfigServer, common.ComponentProxy},
			},
			"every mongos restarting": {
				restarting(3, 3, 0),
				[]string{common.ComponentEngine, common.ComponentConfigServer},
			},
		} {
			c, _ := bindingContext(t, nil, tc.live)

			s, err := p.MonitoringSources(c)
			require.NoError(t, err, name)
			assert.Equal(t, tc.want, endpointComponents(s), name)
		}
	})
}
