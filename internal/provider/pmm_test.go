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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	monitoringv1alpha1 "github.com/openeverest/openeverest/v2/api/monitoring/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
)

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

		require.NoError(t, applyPMMBindings(c, psmdb, metricsTestUsersSecret))

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

		require.NoError(t, applyPMMBindings(c, psmdb, metricsTestUsersSecret))

		assert.Equal(t, resource.MustParse("1Gi"), psmdb.Spec.PMM.Resources.Limits[corev1.ResourceMemory])
		assert.Equal(t, resource.MustParse("250m"), psmdb.Spec.PMM.Resources.Requests[corev1.ResourceCPU])
		assert.Equal(t, pmmResourcesSmall.Requests[corev1.ResourceMemory], psmdb.Spec.PMM.Resources.Requests[corev1.ResourceMemory], "unset values keep the tier default")
	})

	t.Run("an entry limit below the tier request lowers the request to the limit", func(t *testing.T) {
		t.Parallel()

		b := acceptedBinding("pmm", "pmm-dst", "pmm", corev1alpha1.MonitoringExecutionModeProviderManaged)
		b.Spec.Parameters = &runtime.RawExtension{Raw: []byte(`{"resources":{"limits":{"cpu":"50m","memory":"64Mi"}}}`)}
		c, _ := bindingContext(t, pmmEntry, b,
			destination("pmm-dst", "pmm", `{"url":"https://pmm.example.com"}`, "pmm-token"),
			pmmClass("percona/pmm-client:3.8.0"),
			pmmToken(),
		)
		psmdb := &psmdbv1.PerconaServerMongoDB{}

		require.NoError(t, applyPMMBindings(c, psmdb, metricsTestUsersSecret))

		limits := corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("50m"),
			corev1.ResourceMemory: resource.MustParse("64Mi"),
		}
		assert.Equal(t, corev1.ResourceRequirements{Requests: limits, Limits: limits}, psmdb.Spec.PMM.Resources,
			"the small tier requests 95m and 97Mi, above these limits")
	})

	t.Run("the engine memory limit picks the size tier", func(t *testing.T) {
		t.Parallel()

		for engineMemory, want := range map[string]corev1.ResourceRequirements{
			"8G":  pmmResourcesMedium,
			"32G": pmmResourcesLarge,
		} {
			t.Run(engineMemory, func(t *testing.T) {
				t.Parallel()

				c, _ := bindingContextWith(t, interceptor.Funcs{}, engineMemory, pmmEntry,
					acceptedBinding("pmm", "pmm-dst", "pmm", corev1alpha1.MonitoringExecutionModeProviderManaged),
					destination("pmm-dst", "pmm", `{"url":"https://pmm.example.com"}`, "pmm-token"),
					pmmClass("percona/pmm-client:3.8.0"),
					pmmToken(),
				)
				psmdb := &psmdbv1.PerconaServerMongoDB{}

				require.NoError(t, applyPMMBindings(c, psmdb, metricsTestUsersSecret))
				assert.Equal(t, want, psmdb.Spec.PMM.Resources)
			})
		}
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

		require.NoError(t, applyPMMBindings(c, psmdb, metricsTestUsersSecret))
		assert.Equal(t, live.Spec.PMM, psmdb.Spec.PMM)
	})

	t.Run("a binding that cannot be rendered reports RenderFailed and keeps the live rendering", func(t *testing.T) {
		t.Parallel()

		type objects struct {
			binding *monitoringv1alpha1.MonitoringBinding
			dst     *monitoringv1alpha1.MonitoringDestination
			class   *monitoringv1alpha1.MonitoringClass
			token   *corev1.Secret
		}
		live := livePSMDB(psmdbv1.AppStateReady, false, false)
		for name, tc := range map[string]struct {
			breakIt func(o *objects)
			message string
		}{
			"relative url": {
				func(o *objects) { o.dst.Spec.Parameters.Raw = []byte(`{"url":"pmm.example.com"}`) },
				`parameters.url "pmm.example.com": not an absolute URL`,
			},
			"no credentials": {
				func(o *objects) { o.dst.Spec.CredentialsSecretRef = nil },
				"has no credentialsSecretRef",
			},
			"no agent image": {
				func(o *objects) { o.class.Spec.ProviderManaged.AgentImage = "" },
				"sets no providerManaged.agentImage",
			},
			"entry request above its own limit": {
				func(o *objects) {
					o.binding.Spec.Parameters = &runtime.RawExtension{Raw: []byte(`{"resources":{"requests":{"cpu":"500m"},"limits":{"cpu":"200m"}}}`)}
				},
				"resources.requests.cpu 500m exceeds resources.limits.cpu 200m",
			},
			"credentials Secret missing": {
				func(o *objects) { o.token = nil },
				"secret pmm-token not found",
			},
			"credentials Secret without apiKey": {
				func(o *objects) { o.token.Data = map[string][]byte{"token": []byte("secret-token")} },
				"failed to get key apiKey from secret pmm-token",
			},
			"undecodable destination parameters": {
				func(o *objects) { o.dst.Spec.Parameters.Raw = []byte(`{"url":42}`) },
				`decode MonitoringDestination "pmm-dst" parameters`,
			},
			"undecodable binding parameters": {
				func(o *objects) {
					o.binding.Spec.Parameters = &runtime.RawExtension{Raw: []byte(`{"resources":"lots"}`)}
				},
				"decode binding parameters",
			},
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				o := objects{
					binding: acceptedBinding("pmm", "pmm-dst", "pmm", corev1alpha1.MonitoringExecutionModeProviderManaged),
					dst:     destination("pmm-dst", "pmm", `{"url":"https://pmm.example.com"}`, "pmm-token"),
					class:   pmmClass("percona/pmm-client:3.8.0"),
					token:   pmmToken(),
				}
				tc.breakIt(&o)
				objs := []client.Object{o.binding, o.dst, o.class, live.DeepCopy()}
				if o.token != nil {
					objs = append(objs, o.token)
				}
				c, _ := bindingContext(t, pmmEntry, objs...)
				psmdb := &psmdbv1.PerconaServerMongoDB{}

				require.NoError(t, applyPMMBindings(c, psmdb, metricsTestUsersSecret))
				assert.Equal(t, live.Spec.PMM, psmdb.Spec.PMM)

				reason, message, staged := c.MonitoringBindingResult(bindingTestInstance + "-pmm")
				require.True(t, staged)
				assert.Equal(t, monitoringv1alpha1.ReasonRenderFailed, reason)
				assert.Contains(t, message, tc.message)
			})
		}
	})

	t.Run("API errors fail Sync instead of reporting RenderFailed", func(t *testing.T) {
		t.Parallel()

		errAPI := apierrors.NewServiceUnavailable("apiserver is restarting")
		for name, funcs := range map[string]interceptor.Funcs{
			"reading the credentials Secret": {
				Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*corev1.Secret); ok && key.Name == "pmm-token" {
						return errAPI
					}
					return cl.Get(ctx, key, obj, opts...)
				},
			},
			"applying the users Secret": {
				Apply: func(context.Context, client.WithWatch, runtime.ApplyConfiguration, ...client.ApplyOption) error {
					return errAPI
				},
			},
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				c, _ := bindingContextWith(t, funcs, "1G", pmmEntry,
					acceptedBinding("pmm", "pmm-dst", "pmm", corev1alpha1.MonitoringExecutionModeProviderManaged),
					destination("pmm-dst", "pmm", `{"url":"https://pmm.example.com"}`, "pmm-token"),
					pmmClass("percona/pmm-client:3.8.0"),
					pmmToken(),
					livePSMDB(psmdbv1.AppStateReady, false, false),
				)
				psmdb := &psmdbv1.PerconaServerMongoDB{}

				require.ErrorIs(t, applyPMMBindings(c, psmdb, metricsTestUsersSecret), errAPI)

				_, _, staged := c.MonitoringBindingResult(bindingTestInstance + "-pmm")
				assert.False(t, staged, "the retry decides the binding's result")
			})
		}
	})

	t.Run("a binding that cannot be rendered leaves PMM disabled while no engine CR exists", func(t *testing.T) {
		t.Parallel()

		c, _ := bindingContext(t, pmmEntry,
			acceptedBinding("pmm", "pmm-dst", "pmm", corev1alpha1.MonitoringExecutionModeProviderManaged),
			destination("pmm-dst", "pmm", `{"url":"pmm.example.com"}`, "pmm-token"),
			pmmClass("percona/pmm-client:3.8.0"),
			pmmToken(),
		)
		psmdb := &psmdbv1.PerconaServerMongoDB{}

		require.NoError(t, applyPMMBindings(c, psmdb, metricsTestUsersSecret))
		assert.Equal(t, psmdbv1.PMMSpec{}, psmdb.Spec.PMM)

		reason, _, staged := c.MonitoringBindingResult(bindingTestInstance + "-pmm")
		require.True(t, staged)
		assert.Equal(t, monitoringv1alpha1.ReasonRenderFailed, reason)
	})

	t.Run("removing the entry disables PMM while its binding still exists", func(t *testing.T) {
		t.Parallel()

		c, _ := bindingContext(t, nil,
			acceptedBinding("pmm", "pmm-dst", "pmm", corev1alpha1.MonitoringExecutionModeProviderManaged),
			destination("pmm-dst", "pmm", `{"url":"https://pmm.example.com"}`, "pmm-token"),
			pmmClass("percona/pmm-client:3.8.0"),
			pmmToken(),
			livePSMDB(psmdbv1.AppStateReady, false, false),
		)
		psmdb := &psmdbv1.PerconaServerMongoDB{}

		require.NoError(t, applyPMMBindings(c, psmdb, metricsTestUsersSecret))
		assert.Equal(t, psmdbv1.PMMSpec{}, psmdb.Spec.PMM, "core deletes the binding of a removed entry")

		_, _, staged := c.MonitoringBindingResult(bindingTestInstance + "-pmm")
		assert.False(t, staged)
	})

	t.Run("a usable pmm binding renders while another entry has no binding yet", func(t *testing.T) {
		t.Parallel()

		c, _ := bindingContext(t, []corev1alpha1.InstanceMonitoringDestination{entry("pmm", "pmm-dst"), entry("prom", "prom-dst")},
			acceptedBinding("pmm", "pmm-dst", "pmm", corev1alpha1.MonitoringExecutionModeProviderManaged),
			destination("pmm-dst", "pmm", `{"url":"https://pmm.example.com"}`, "pmm-token"),
			pmmClass("percona/pmm-client:3.8.0"),
			pmmToken(),
			livePSMDB(psmdbv1.AppStateReady, false, false),
		)
		psmdb := &psmdbv1.PerconaServerMongoDB{}

		require.NoError(t, applyPMMBindings(c, psmdb, metricsTestUsersSecret))
		assert.True(t, psmdb.Spec.PMM.Enabled)
		assert.Equal(t, "pmm.example.com", psmdb.Spec.PMM.ServerHost, "rendered from the binding, not held from the live CR")
	})

	t.Run("without a pmm entry spec.pmm is disabled", func(t *testing.T) {
		t.Parallel()

		c, _ := bindingContext(t, []corev1alpha1.InstanceMonitoringDestination{entry("prom", "prom-dst")},
			acceptedBinding("prom", "prom-dst", "prometheus", corev1alpha1.MonitoringExecutionModeExtensionManaged),
			destination("prom-dst", "prometheus", "", ""),
			metricsClass("prometheus"),
			livePSMDB(psmdbv1.AppStateReady, false, false),
		)
		psmdb := &psmdbv1.PerconaServerMongoDB{}

		require.NoError(t, applyPMMBindings(c, psmdb, metricsTestUsersSecret))
		assert.Equal(t, psmdbv1.PMMSpec{}, psmdb.Spec.PMM, "the live PMM goes once its entry is removed")
	})

	t.Run("the token leaves the users secret once the live cluster has PMM off", func(t *testing.T) {
		t.Parallel()

		operatorKeys := map[string][]byte{psmdbv1.EnvMongoDBDatabaseAdminPassword: []byte("pw")}
		c, cl := bindingContext(t, pmmEntry,
			acceptedBinding("pmm", "pmm-dst", "pmm", corev1alpha1.MonitoringExecutionModeProviderManaged),
			destination("pmm-dst", "pmm", `{"url":"https://pmm.example.com"}`, "pmm-token"),
			pmmClass("percona/pmm-client:3.8.0"),
			pmmToken(),
			&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: metricsTestUsersSecret, Namespace: bindingTestNS},
				Data:       operatorKeys,
			},
		)
		require.NoError(t, applyPMMBindings(c, &psmdbv1.PerconaServerMongoDB{}, metricsTestUsersSecret))

		usersData := func() map[string][]byte {
			users := &corev1.Secret{}
			require.NoError(t, c.Get(users, metricsTestUsersSecret))
			return users.Data
		}
		require.Contains(t, usersData(), psmdbv1.PMMServerToken)

		detachedInstance := c.Instance().DeepCopy()
		detachedInstance.Spec.Monitoring = nil
		detached := controller.NewContext(context.Background(), cl, detachedInstance, "psmdb")
		live := livePSMDB(psmdbv1.AppStateReady, false, false)
		require.NoError(t, cl.Create(context.Background(), live))

		require.NoError(t, applyPMMBindings(detached, &psmdbv1.PerconaServerMongoDB{}, metricsTestUsersSecret))
		assert.Contains(t, usersData(), psmdbv1.PMMServerToken, "the operator still runs PMM")

		live.Spec.PMM = psmdbv1.PMMSpec{}
		require.NoError(t, cl.Update(context.Background(), live))

		require.NoError(t, applyPMMBindings(detached, &psmdbv1.PerconaServerMongoDB{}, metricsTestUsersSecret))
		assert.Equal(t, operatorKeys, usersData(), "only the token goes")
	})

	t.Run("a cluster without PMM gets no users secret from it", func(t *testing.T) {
		t.Parallel()

		live := livePSMDB(psmdbv1.AppStateReady, false, false)
		live.Spec.PMM = psmdbv1.PMMSpec{}
		c, _ := bindingContext(t, nil, live)

		require.NoError(t, applyPMMBindings(c, &psmdbv1.PerconaServerMongoDB{}, metricsTestUsersSecret))

		err := c.Get(&corev1.Secret{}, metricsTestUsersSecret)
		assert.True(t, apierrors.IsNotFound(err), "the operator creates the users secret: %v", err)
	})
}
