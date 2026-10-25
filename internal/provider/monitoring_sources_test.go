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
	"testing"

	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

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
// CR that make them resolvable. The legacy monitoring component is absent.
func bindingContext(t *testing.T, entries []corev1alpha1.InstanceMonitoringDestination, objs ...client.Object) (*controller.Context, client.Client) {
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

// livePSMDB is the engine CR as the operator currently sees it.
func livePSMDB(state psmdbv1.AppState, sharded, withExporter bool) *psmdbv1.PerconaServerMongoDB {
	psmdb := &psmdbv1.PerconaServerMongoDB{
		ObjectMeta: metav1.ObjectMeta{Name: bindingTestInstance, Namespace: bindingTestNS},
		Spec: psmdbv1.PerconaServerMongoDBSpec{
			Replsets: []*psmdbv1.ReplsetSpec{{Name: "rs0"}},
			PMM:      psmdbv1.PMMSpec{Enabled: true, ServerHost: "live-pmm.example.com", Image: "percona/pmm-client:3.5.0"},
		},
		Status: psmdbv1.PerconaServerMongoDBStatus{State: state},
	}
	if sharded {
		psmdb.Spec.Sharding = psmdbv1.Sharding{
			Enabled:          true,
			ConfigsvrReplSet: &psmdbv1.ReplsetSpec{Name: "cfg"},
			Mongos:           &psmdbv1.MongosSpec{},
		}
	}
	if withExporter {
		psmdb.Spec.Replsets[0].Sidecars = []corev1.Container{{Name: metricsExporterContainerName}}
	}
	return psmdb
}

func TestMetricsExporterDemanded(t *testing.T) {
	t.Parallel()

	t.Run("an ExtensionManaged binding whose class requires metrics demands the exporter", func(t *testing.T) {
		t.Parallel()

		c, _ := bindingContext(t, []corev1alpha1.InstanceMonitoringDestination{entry("prom", "prom-dst")},
			acceptedBinding("prom", "prom-dst", "prometheus", corev1alpha1.MonitoringExecutionModeExtensionManaged),
			destination("prom-dst", "prometheus", "", ""),
			metricsClass("prometheus"),
		)

		demanded, err := metricsExporterDemanded(c)
		require.NoError(t, err)
		assert.True(t, demanded)
	})

	t.Run("a ProviderManaged pmm binding alone does not", func(t *testing.T) {
		t.Parallel()

		c, _ := bindingContext(t, []corev1alpha1.InstanceMonitoringDestination{entry("pmm", "pmm-dst")},
			acceptedBinding("pmm", "pmm-dst", "pmm", corev1alpha1.MonitoringExecutionModeProviderManaged),
			destination("pmm-dst", "pmm", `{"url":"https://pmm.example.com"}`, "pmm-token"),
			pmmClass("percona/pmm-client:3.8.0"),
			livePSMDB(psmdbv1.AppStateReady, false, true),
		)

		demanded, err := metricsExporterDemanded(c)
		require.NoError(t, err)
		assert.False(t, demanded, "settled without a metrics class: the live sidecar is not held")
	})

	t.Run("no entries and no legacy component means no exporter", func(t *testing.T) {
		t.Parallel()

		c, _ := bindingContext(t, nil, livePSMDB(psmdbv1.AppStateReady, false, true))

		demanded, err := metricsExporterDemanded(c)
		require.NoError(t, err)
		assert.False(t, demanded)
	})

	t.Run("the legacy metrics parameters still demand it", func(t *testing.T) {
		t.Parallel()

		c, _ := metricsContext(t, metricsOnly(true, false), true)

		demanded, err := metricsExporterDemanded(c)
		require.NoError(t, err)
		assert.True(t, demanded)
	})

	t.Run("while an entry has no binding yet the live sidecar is held", func(t *testing.T) {
		t.Parallel()

		c, _ := bindingContext(t, []corev1alpha1.InstanceMonitoringDestination{entry("prom", "prom-dst")},
			livePSMDB(psmdbv1.AppStateReady, false, true),
		)

		demanded, err := metricsExporterDemanded(c)
		require.NoError(t, err)
		assert.True(t, demanded, "hold rule")
	})

	t.Run("while an entry has no binding yet and nothing is live, nothing is added", func(t *testing.T) {
		t.Parallel()

		c, _ := bindingContext(t, []corev1alpha1.InstanceMonitoringDestination{entry("prom", "prom-dst")})

		demanded, err := metricsExporterDemanded(c)
		require.NoError(t, err)
		assert.False(t, demanded)
	})

	t.Run("configureMetricsExporter follows the demand", func(t *testing.T) {
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

	t.Run("nothing is published without the exporter, before readiness or before the CR exists", func(t *testing.T) {
		t.Parallel()

		for name, objs := range map[string][]client.Object{
			"no exporter": {livePSMDB(psmdbv1.AppStateReady, true, false)},
			"not ready":   {livePSMDB(psmdbv1.AppStateInit, true, true)},
			"no CR":       {},
		} {
			c, _ := bindingContext(t, nil, objs...)

			s, err := p.MonitoringSources(c)
			require.NoError(t, err, name)
			assert.Empty(t, s.Metrics, name)
		}
	})
}

func TestApplyPMMBindings(t *testing.T) {
	t.Parallel()

	pmmToken := func() *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "pmm-token", Namespace: bindingTestNS},
			Data:       map[string][]byte{"apiKey": []byte("secret-token")},
		}
	}
	pmmEntry := []corev1alpha1.InstanceMonitoringDestination{entry("pmm", "pmm-dst")}

	t.Run("renders spec.pmm from an Accepted binding", func(t *testing.T) {
		t.Parallel()

		c, _ := bindingContext(t, pmmEntry,
			acceptedBinding("pmm", "pmm-dst", "pmm", corev1alpha1.MonitoringExecutionModeProviderManaged),
			destination("pmm-dst", "pmm", `{"url":"https://pmm.example.com:8443/graph","verifyTLS":false}`, "pmm-token"),
			pmmClass("percona/pmm-client:3.8.0"),
			pmmToken(),
		)
		psmdb := &psmdbv1.PerconaServerMongoDB{}

		bound, err := applyPMMBindings(c, psmdb, metricsTestUsersSecret)
		require.NoError(t, err)
		assert.True(t, bound)

		assert.True(t, psmdb.Spec.PMM.Enabled)
		assert.Equal(t, "pmm.example.com:8443", psmdb.Spec.PMM.ServerHost)
		assert.Equal(t, "percona/pmm-client:3.8.0", psmdb.Spec.PMM.Image, "the class owns the agent image")
		assert.Equal(t, pmmResourcesSmall, psmdb.Spec.PMM.Resources, "1G engine memory is the small tier")
		assert.Equal(t, "--custom-labels=namespace=db,core_openeverest_io_instance=my-mongo", psmdb.Spec.PMM.MongodParams)
		assert.Equal(t, "--custom-labels=namespace=db,core_openeverest_io_instance=my-mongo,core_openeverest_io_component=proxy", psmdb.Spec.PMM.MongosParams)

		users := &corev1.Secret{}
		require.NoError(t, c.Get(users, metricsTestUsersSecret))
		assert.Equal(t, []byte("secret-token"), users.Data["PMM_SERVER_TOKEN"], "the operator reads the token from the users secret")

		_, _, staged := c.MonitoringBindingResult(bindingTestInstance + "-pmm")
		assert.False(t, staged, "a clean render leaves Configured to the runtime")
	})

	t.Run("falls back to the provider's default pmm image when the class sets none", func(t *testing.T) {
		t.Parallel()

		c, _ := bindingContext(t, pmmEntry,
			acceptedBinding("pmm", "pmm-dst", "pmm", corev1alpha1.MonitoringExecutionModeProviderManaged),
			destination("pmm-dst", "pmm", `{"url":"https://pmm.example.com"}`, "pmm-token"),
			pmmClass(""),
			pmmToken(),
		)
		psmdb := &psmdbv1.PerconaServerMongoDB{}

		_, err := applyPMMBindings(c, psmdb, metricsTestUsersSecret)
		require.NoError(t, err)

		spec, err := c.ProviderSpec()
		require.NoError(t, err)
		assert.Equal(t, controller.GetDefaultImageForComponent(spec, common.ComponentMonitoring), psmdb.Spec.PMM.Image)
	})

	t.Run("binding parameters override the size tier resources", func(t *testing.T) {
		t.Parallel()

		b := acceptedBinding("pmm", "pmm-dst", "pmm", corev1alpha1.MonitoringExecutionModeProviderManaged)
		b.Spec.Parameters = &runtime.RawExtension{Raw: []byte(`{"resources":{"limits":{"memory":"1Gi"},"requests":{"cpu":"250m"}}}`)}
		c, _ := bindingContext(t, pmmEntry, b,
			destination("pmm-dst", "pmm", `{"url":"https://pmm.example.com"}`, "pmm-token"),
			pmmClass("percona/pmm-client:3.8.0"),
			pmmToken(),
		)
		psmdb := &psmdbv1.PerconaServerMongoDB{}

		_, err := applyPMMBindings(c, psmdb, metricsTestUsersSecret)
		require.NoError(t, err)

		assert.Equal(t, resource.MustParse("1Gi"), psmdb.Spec.PMM.Resources.Limits[corev1.ResourceMemory])
		assert.Equal(t, resource.MustParse("250m"), psmdb.Spec.PMM.Resources.Requests[corev1.ResourceCPU])
		assert.Equal(t, pmmResourcesSmall.Requests[corev1.ResourceMemory], psmdb.Spec.PMM.Resources.Requests[corev1.ResourceMemory], "unset values keep the tier default")
	})

	t.Run("a binding that is not Accepted keeps the live rendering", func(t *testing.T) {
		t.Parallel()

		b := acceptedBinding("pmm", "pmm-dst", "pmm", corev1alpha1.MonitoringExecutionModeProviderManaged)
		b.Status.Conditions = nil
		live := livePSMDB(psmdbv1.AppStateReady, false, false)
		c, _ := bindingContext(t, pmmEntry, b,
			destination("pmm-dst", "pmm", `{"url":"https://pmm.example.com"}`, "pmm-token"),
			pmmClass("percona/pmm-client:3.8.0"),
			pmmToken(),
			live,
		)
		psmdb := &psmdbv1.PerconaServerMongoDB{}

		bound, err := applyPMMBindings(c, psmdb, metricsTestUsersSecret)
		require.NoError(t, err)
		assert.True(t, bound, "the entry still claims spec.pmm, the legacy component must not take over")
		assert.Equal(t, live.Spec.PMM, psmdb.Spec.PMM)
	})

	t.Run("a destination that cannot be rendered reports RenderFailed and keeps the live rendering", func(t *testing.T) {
		t.Parallel()

		live := livePSMDB(psmdbv1.AppStateReady, false, false)
		for name, dst := range map[string]*monitoringv1alpha1.MonitoringDestination{
			"relative url":   destination("pmm-dst", "pmm", `{"url":"pmm.example.com"}`, "pmm-token"),
			"no credentials": destination("pmm-dst", "pmm", `{"url":"https://pmm.example.com"}`, ""),
		} {
			c, _ := bindingContext(t, pmmEntry,
				acceptedBinding("pmm", "pmm-dst", "pmm", corev1alpha1.MonitoringExecutionModeProviderManaged),
				dst, pmmClass("percona/pmm-client:3.8.0"), pmmToken(), live.DeepCopy(),
			)
			psmdb := &psmdbv1.PerconaServerMongoDB{}

			bound, err := applyPMMBindings(c, psmdb, metricsTestUsersSecret)
			require.NoError(t, err, name)
			assert.True(t, bound, name)
			assert.Equal(t, live.Spec.PMM, psmdb.Spec.PMM, name)

			reason, _, staged := c.MonitoringBindingResult(bindingTestInstance + "-pmm")
			require.True(t, staged, name)
			assert.Equal(t, monitoringv1alpha1.ReasonRenderFailed, reason, name)
		}
	})

	t.Run("without a pmm binding the legacy component is left in charge", func(t *testing.T) {
		t.Parallel()

		c, _ := bindingContext(t, []corev1alpha1.InstanceMonitoringDestination{entry("prom", "prom-dst")},
			acceptedBinding("prom", "prom-dst", "prometheus", corev1alpha1.MonitoringExecutionModeExtensionManaged),
			destination("prom-dst", "prometheus", "", ""),
			metricsClass("prometheus"),
		)
		psmdb := &psmdbv1.PerconaServerMongoDB{}

		bound, err := applyPMMBindings(c, psmdb, metricsTestUsersSecret)
		require.NoError(t, err)
		assert.False(t, bound)
		assert.False(t, psmdb.Spec.PMM.Enabled)
	})
}
