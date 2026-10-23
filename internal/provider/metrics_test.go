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
	"encoding/json"
	"slices"
	"testing"

	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mongodb/definition/components"
	vmv1beta1 "github.com/openeverest/provider-percona-server-mongodb/internal/apis/victoriametrics/v1beta1"
	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
)

const metricsTestUsersSecret = "everest-secrets-my-mongo"

// metricsContext builds a context for an Instance whose monitoring component
// carries params; a nil params leaves the component out. withScrapeKinds
// controls whether the cluster serves PodMonitor and VMPodScrape, i.e. whether
// their operators' CRDs are installed. The provider's scheme knows the types
// either way.
func metricsContext(t *testing.T, params *components.MonitoringParameters, withScrapeKinds bool, objs ...client.Object) (*controller.Context, client.Client) {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, monitoringv1.AddToScheme(scheme))
	require.NoError(t, vmv1beta1.AddToScheme(scheme))

	scrapeKinds := []schema.GroupVersionKind{
		monitoringv1.SchemeGroupVersion.WithKind(monitoringv1.PodMonitorsKind),
		vmv1beta1.GroupVersion.WithKind("VMPodScrape"),
	}
	mapper := meta.NewDefaultRESTMapper(nil)
	if withScrapeKinds {
		for _, gvk := range scrapeKinds {
			mapper.Add(gvk, meta.RESTScopeNamespace)
		}
	}
	// Without the CRDs the API server rejects any request for the kinds.
	notServed := func(obj client.Object) error {
		gvk, err := apiutil.GVKForObject(obj, scheme)
		if err != nil || withScrapeKinds || !slices.Contains(scrapeKinds, gvk) {
			return nil
		}
		return &meta.NoKindMatchError{GroupKind: gvk.GroupKind(), SearchedVersions: []string{gvk.Version}}
	}

	in := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "my-mongo", Namespace: "db", UID: "uid"},
		Spec: corev1alpha1.InstanceSpec{
			Components: map[string]corev1alpha1.ComponentSpec{
				common.ComponentEngine: {},
			},
		},
	}
	if params != nil {
		raw, err := json.Marshal(params)
		require.NoError(t, err)
		in.Spec.Components[common.ComponentMonitoring] = corev1alpha1.ComponentSpec{
			Parameters: &runtime.RawExtension{Raw: raw},
		}
	}

	provider := &corev1alpha1.Provider{
		ObjectMeta: metav1.ObjectMeta{Name: "psmdb"},
		Spec:       loadProviderSpec(t),
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRESTMapper(mapper).
		WithObjects(append(objs, in, provider)...).
		WithInterceptorFuncs(interceptor.Funcs{
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				if err := notServed(obj); err != nil {
					return err
				}
				return c.Delete(ctx, obj, opts...)
			},
		}).
		Build()
	return controller.NewContext(context.Background(), cl, in, "psmdb"), cl
}

func metricsOnly(prometheus, victoriaMetrics bool) *components.MonitoringParameters {
	return &components.MonitoringParameters{
		Metrics: &components.MetricsParameters{
			Prometheus:      &components.ScrapeParameters{Enabled: prometheus},
			VictoriaMetrics: &components.ScrapeParameters{Enabled: victoriaMetrics},
		},
	}
}

func shardedPSMDB() *psmdbv1.PerconaServerMongoDB {
	return &psmdbv1.PerconaServerMongoDB{
		Spec: psmdbv1.PerconaServerMongoDBSpec{
			Replsets: []*psmdbv1.ReplsetSpec{{Name: "rs0"}, {Name: "rs1"}},
			Sharding: psmdbv1.Sharding{
				Enabled:          true,
				ConfigsvrReplSet: &psmdbv1.ReplsetSpec{Name: "cfg"},
				Mongos:           &psmdbv1.MongosSpec{Port: 27018},
			},
		},
	}
}

func exporterSidecar(t *testing.T, sidecars []corev1.Container) corev1.Container {
	t.Helper()

	require.Len(t, sidecars, 1)
	assert.Equal(t, metricsExporterContainerName, sidecars[0].Name)
	return sidecars[0]
}

func TestConfigureMetricsExporter(t *testing.T) {
	t.Parallel()

	t.Run("adds an exporter to every mongod and mongos pod", func(t *testing.T) {
		t.Parallel()

		c, _ := metricsContext(t, metricsOnly(false, false), true)
		psmdb := shardedPSMDB()

		require.NoError(t, configureMetricsExporter(c, psmdb, metricsTestUsersSecret))

		for _, rs := range psmdb.Spec.Replsets {
			sidecar := exporterSidecar(t, rs.Sidecars)
			assert.Equal(t, "percona/mongodb_exporter:0.53.0", sidecar.Image)
			assert.Contains(t, sidecar.Args, "--mongodb.uri=mongodb://127.0.0.1:27017")
			assert.Contains(t, sidecar.Args, "--collector.replicasetstatus")
			assert.Equal(t, []corev1.ContainerPort{{Name: "metrics", ContainerPort: 9216, Protocol: corev1.ProtocolTCP}}, sidecar.Ports)

			require.Len(t, sidecar.Env, 2)
			for env, key := range map[string]string{"MONGODB_USER": clusterMonitorUserKey, "MONGODB_PASSWORD": clusterMonitorPasswordKey} {
				idx := -1
				for i := range sidecar.Env {
					if sidecar.Env[i].Name == env {
						idx = i
					}
				}
				require.NotEqual(t, -1, idx, "missing %s", env)
				ref := sidecar.Env[idx].ValueFrom.SecretKeyRef
				assert.Equal(t, metricsTestUsersSecret, ref.Name)
				assert.Equal(t, key, ref.Key)
			}
		}

		exporterSidecar(t, psmdb.Spec.Sharding.ConfigsvrReplSet.Sidecars)

		mongos := exporterSidecar(t, psmdb.Spec.Sharding.Mongos.Sidecars)
		assert.Contains(t, mongos.Args, "--mongodb.uri=mongodb://127.0.0.1:27018", "the exporter follows the mongos port")
		assert.Contains(t, mongos.Args, "--collector.shards")
		assert.NotContains(t, mongos.Args, "--collector.replicasetstatus", "mongos has no replica set status")
	})

	t.Run("leaves pods alone when metrics are not exposed", func(t *testing.T) {
		t.Parallel()

		pmmOnly := &components.MonitoringParameters{MonitoringConfigName: ptr.To("pmm")}
		for name, params := range map[string]*components.MonitoringParameters{"no monitoring": nil, "PMM only": pmmOnly} {
			c, _ := metricsContext(t, params, true)
			psmdb := shardedPSMDB()

			require.NoError(t, configureMetricsExporter(c, psmdb, metricsTestUsersSecret), name)

			assert.Empty(t, psmdb.Spec.Replsets[0].Sidecars, name)
			assert.Empty(t, psmdb.Spec.Sharding.ConfigsvrReplSet.Sidecars, name)
			assert.Empty(t, psmdb.Spec.Sharding.Mongos.Sidecars, name)
		}
	})
}

func TestReconcileMetricsScrapes(t *testing.T) {
	t.Parallel()

	get := func(t *testing.T, cl client.Client, obj client.Object) error {
		t.Helper()
		name := "my-mongo-prometheus"
		if _, ok := obj.(*vmv1beta1.VMPodScrape); ok {
			name = "my-mongo-victoriametrics"
		}
		return cl.Get(context.Background(), client.ObjectKey{Namespace: "db", Name: name}, obj)
	}

	t.Run("creates a PodMonitor for Prometheus", func(t *testing.T) {
		t.Parallel()

		c, cl := metricsContext(t, metricsOnly(true, false), true)

		require.NoError(t, reconcileMetricsScrapes(c))

		pm := &monitoringv1.PodMonitor{}
		require.NoError(t, get(t, cl, pm))
		assert.Equal(t, map[string]string{controller.InstanceLabel: "my-mongo"}, pm.Spec.Selector.MatchLabels)
		assert.Equal(t, []string{controller.InstanceLabel, controller.ComponentLabel}, pm.Spec.PodTargetLabels)
		require.Len(t, pm.Spec.PodMetricsEndpoints, 1)
		assert.Equal(t, ptr.To("metrics"), pm.Spec.PodMetricsEndpoints[0].Port)
		require.Len(t, pm.OwnerReferences, 1, "the PodMonitor goes away with the Instance")

		assert.True(t, apierrors.IsNotFound(get(t, cl, &vmv1beta1.VMPodScrape{})))
	})

	t.Run("creates a VMPodScrape for VictoriaMetrics", func(t *testing.T) {
		t.Parallel()

		c, cl := metricsContext(t, metricsOnly(false, true), true)

		require.NoError(t, reconcileMetricsScrapes(c))

		scrape := &vmv1beta1.VMPodScrape{}
		require.NoError(t, get(t, cl, scrape))
		assert.Equal(t, map[string]string{controller.InstanceLabel: "my-mongo"}, scrape.Spec.Selector.MatchLabels)
		require.Len(t, scrape.Spec.PodMetricsEndpoints, 1)
		assert.Equal(t, ptr.To("metrics"), scrape.Spec.PodMetricsEndpoints[0].Port)

		assert.True(t, apierrors.IsNotFound(get(t, cl, &monitoringv1.PodMonitor{})))
	})

	t.Run("removes scrapes that are no longer enabled", func(t *testing.T) {
		t.Parallel()

		existing := []client.Object{
			&monitoringv1.PodMonitor{ObjectMeta: metav1.ObjectMeta{Name: "my-mongo-prometheus", Namespace: "db"}},
			&vmv1beta1.VMPodScrape{ObjectMeta: metav1.ObjectMeta{Name: "my-mongo-victoriametrics", Namespace: "db"}},
		}
		c, cl := metricsContext(t, nil, true, existing...)

		require.NoError(t, reconcileMetricsScrapes(c))

		assert.True(t, apierrors.IsNotFound(get(t, cl, &monitoringv1.PodMonitor{})))
		assert.True(t, apierrors.IsNotFound(get(t, cl, &vmv1beta1.VMPodScrape{})))
	})

	t.Run("leaves the VMPodScrape converted from its PodMonitor to the converter", func(t *testing.T) {
		t.Parallel()

		converted := &vmv1beta1.VMPodScrape{ObjectMeta: metav1.ObjectMeta{Name: "my-mongo-prometheus", Namespace: "db"}}
		c, cl := metricsContext(t, metricsOnly(true, false), true, converted)

		require.NoError(t, reconcileMetricsScrapes(c))

		require.NoError(t, cl.Get(context.Background(), client.ObjectKeyFromObject(converted), &vmv1beta1.VMPodScrape{}))
	})

	t.Run("removes the converted VMPodScrape with the PodMonitor", func(t *testing.T) {
		t.Parallel()

		converted := &vmv1beta1.VMPodScrape{ObjectMeta: metav1.ObjectMeta{Name: "my-mongo-prometheus", Namespace: "db"}}
		c, cl := metricsContext(t, metricsOnly(false, true), true, converted)

		require.NoError(t, reconcileMetricsScrapes(c))

		err := cl.Get(context.Background(), client.ObjectKeyFromObject(converted), &vmv1beta1.VMPodScrape{})
		assert.True(t, apierrors.IsNotFound(err), "the instance would stay scraped through it")
		require.NoError(t, get(t, cl, &vmv1beta1.VMPodScrape{}), "the provider's own VMPodScrape stays")
	})

	t.Run("tolerates a cluster without the scrape kinds when disabled", func(t *testing.T) {
		t.Parallel()

		c, _ := metricsContext(t, nil, false)

		require.NoError(t, reconcileMetricsScrapes(c))
	})
}

func TestValidateMetrics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		params          *components.MonitoringParameters
		withScrapeKinds bool
		expectErr       string
	}{
		{name: "no monitoring", withScrapeKinds: false},
		{name: "exporter without scraping needs no operator", params: metricsOnly(false, false), withScrapeKinds: false},
		{name: "operators installed", params: metricsOnly(true, true), withScrapeKinds: true},
		{name: "Prometheus Operator missing", params: metricsOnly(true, false), expectErr: "Prometheus Operator is not installed"},
		{name: "VictoriaMetrics Operator missing", params: metricsOnly(false, true), expectErr: "VictoriaMetrics Operator is not installed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c, _ := metricsContext(t, tt.params, tt.withScrapeKinds)

			err := validateMetrics(c)

			if tt.expectErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.expectErr)
		})
	}
}

func TestPMMIsOptionalInTheMonitoringComponent(t *testing.T) {
	t.Parallel()

	c, _ := metricsContext(t, metricsOnly(true, false), true)

	pmm, err := configureMonitoring(c, metricsTestUsersSecret)

	require.NoError(t, err)
	assert.False(t, pmm.Enabled)
}
