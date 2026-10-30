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
	"testing"

	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	monitoringv1alpha1 "github.com/openeverest/openeverest/v2/api/monitoring/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
)

func corootClass() *monitoringv1alpha1.MonitoringClass {
	return &monitoringv1alpha1.MonitoringClass{
		ObjectMeta: metav1.ObjectMeta{Name: "coroot"},
		Spec: monitoringv1alpha1.MonitoringClassSpec{
			AuthModes: []commonv1alpha1.AuthMode{{Name: commonv1alpha1.AuthModeAnonymous}},
			ExtensionManaged: &monitoringv1alpha1.ExtensionManagedFulfilment{
				Requires:    monitoringv1alpha1.SourceRequirements{Connection: true},
				PodMetadata: &monitoringv1alpha1.PodMetadataPolicy{AnnotationPrefixes: []string{"coroot.com/"}},
			},
		},
	}
}

func monitorSecret() *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: bindingTestInstance + "-monitor", Namespace: bindingTestNS}}
}

func usersSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "everest-secrets-" + bindingTestInstance, Namespace: bindingTestNS},
		Data: map[string][]byte{
			clusterMonitorUserKey:            []byte("clusterMonitor"),
			clusterMonitorPasswordKey:        []byte("monitor-pass"),
			"MONGODB_CLUSTER_ADMIN_PASSWORD": []byte("admin-pass"),
		},
	}
}

func withTLS(psmdb *psmdbv1.PerconaServerMongoDB, mode psmdbv1.TLSMode) *psmdbv1.PerconaServerMongoDB {
	psmdb.Spec.TLS = &psmdbv1.TLSSpec{Mode: mode}
	return psmdb
}

func TestMonitoringSourcesConnections(t *testing.T) {
	t.Parallel()

	p := &PSMDBProvider{}
	ca := &commonv1alpha1.CABundleSource{SecretKeyRef: &commonv1alpha1.SecretKeyRef{Name: bindingTestInstance + "-ssl", Key: "ca.crt"}}
	engine := func(tls *corev1alpha1.ConnectionTLS) corev1alpha1.ConnectionEndpoint {
		return corev1alpha1.ConnectionEndpoint{
			Component: common.ComponentEngine,
			Kind:      "mongodb",
			PodSelector: map[string]string{
				controller.InstanceLabel:  bindingTestInstance,
				controller.ComponentLabel: common.ComponentEngine,
			},
			Container: "mongod",
			Port:      intstr.FromInt32(27017),
			TLS:       tls,
			Auth:      &corev1alpha1.MetricsEndpointAuth{CredentialProfile: corev1alpha1.CredentialProfileMonitor},
		}
	}

	tests := []struct {
		name            string
		objs            []client.Object
		wantConnections []corev1alpha1.ConnectionEndpoint
	}{
		{
			name:            "preferTLS: TLS published but not required",
			objs:            []client.Object{monitorSecret(), withTLS(livePSMDB(psmdbv1.AppStateReady, false, false), psmdbv1.TLSModePrefer)},
			wantConnections: []corev1alpha1.ConnectionEndpoint{engine(&corev1alpha1.ConnectionTLS{CA: ca})},
		},
		{
			name:            "requireTLS: TLS required",
			objs:            []client.Object{monitorSecret(), withTLS(livePSMDB(psmdbv1.AppStateReady, false, false), psmdbv1.TLSModeRequire)},
			wantConnections: []corev1alpha1.ConnectionEndpoint{engine(&corev1alpha1.ConnectionTLS{Required: true, CA: ca})},
		},
		{
			name:            "TLS disabled: plaintext only",
			objs:            []client.Object{monitorSecret(), withTLS(livePSMDB(psmdbv1.AppStateReady, false, false), psmdbv1.TLSModeDisabled)},
			wantConnections: []corev1alpha1.ConnectionEndpoint{engine(nil)},
		},
		{
			name: "no monitor credential: nothing published",
			objs: []client.Object{withTLS(livePSMDB(psmdbv1.AppStateReady, false, false), psmdbv1.TLSModePrefer)},
		},
		{
			name: "cluster not ready: nothing serving",
			objs: []client.Object{monitorSecret(), livePSMDB(psmdbv1.AppStateInit, false, false)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, _ := bindingContext(t, nil, tt.objs...)

			s, err := p.MonitoringSources(c)

			require.NoError(t, err)
			assert.Equal(t, tt.wantConnections, s.Connections)
		})
	}

	t.Run("sharded: each mongod component, not mongos", func(t *testing.T) {
		t.Parallel()
		c, _ := bindingContext(t, nil, monitorSecret(), livePSMDB(psmdbv1.AppStateReady, true, false))

		s, err := p.MonitoringSources(c)

		require.NoError(t, err)
		var components []string
		for _, ep := range s.Connections {
			components = append(components, ep.Component)
		}
		assert.Equal(t, []string{common.ComponentEngine, common.ComponentConfigServer}, components)
		assert.Equal(t, []corev1alpha1.MonitoringCredential{{
			Profile:   corev1alpha1.CredentialProfileMonitor,
			SecretRef: commonv1alpha1.SecretRef{Name: bindingTestInstance + "-monitor"},
		}}, s.Credentials)
	})
}

func TestSyncMonitorCredential(t *testing.T) {
	t.Parallel()
	const usersSecretName = "everest-secrets-" + bindingTestInstance

	t.Run("copies only the cluster monitor user while connections are required", func(t *testing.T) {
		t.Parallel()
		c, cl := bindingContext(t, []corev1alpha1.InstanceMonitoringDestination{entry("coroot", "coroot")},
			corootClass(), destination("coroot", "coroot", "", ""),
			acceptedBinding("coroot", "coroot", "coroot", corev1alpha1.MonitoringExecutionModeExtensionManaged),
			usersSecret(),
		)

		require.NoError(t, syncMonitorCredential(c, usersSecretName))

		got := &corev1.Secret{}
		require.NoError(t, cl.Get(t.Context(), client.ObjectKey{Namespace: bindingTestNS, Name: bindingTestInstance + "-monitor"}, got))
		assert.Equal(t, map[string][]byte{"username": []byte("clusterMonitor"), "password": []byte("monitor-pass")}, got.Data)
		assert.True(t, metav1.IsControlledBy(got, c.Instance()), "the Instance must control the credential Secret")
	})

	t.Run("removes the Secret once nothing requires connections", func(t *testing.T) {
		t.Parallel()
		c, cl := bindingContext(t, nil, usersSecret(), monitorSecret())

		require.NoError(t, syncMonitorCredential(c, usersSecretName))

		err := cl.Get(t.Context(), client.ObjectKey{Namespace: bindingTestNS, Name: bindingTestInstance + "-monitor"}, &corev1.Secret{})
		assert.True(t, apierrors.IsNotFound(err))
	})
}

func TestRenderMonitoringPodAnnotations(t *testing.T) {
	t.Parallel()

	binding := acceptedBinding("coroot", "coroot", "coroot", corev1alpha1.MonitoringExecutionModeExtensionManaged)
	binding.Status.PodMetadata = []monitoringv1alpha1.ComponentPodMetadata{
		{Component: common.ComponentEngine, Annotations: map[string]string{"coroot.com/mongodb-scrape": "true", "sidecar.istio.io/inject": "true"}},
		{Component: common.ComponentConfigServer, Annotations: map[string]string{"coroot.com/mongodb-scrape": "cfg"}},
	}
	c, _ := bindingContext(t, []corev1alpha1.InstanceMonitoringDestination{entry("coroot", "coroot")},
		corootClass(), destination("coroot", "coroot", "", ""), binding,
	)
	psmdb := livePSMDB(psmdbv1.AppStateReady, true, false)
	psmdb.Spec.Replsets[0].Annotations = map[string]string{"team": "orders"}

	require.NoError(t, renderMonitoringPodAnnotations(c, psmdb))

	assert.Equal(t, map[string]string{"team": "orders", "coroot.com/mongodb-scrape": "true"}, psmdb.Spec.Replsets[0].Annotations)
	assert.Equal(t, map[string]string{"coroot.com/mongodb-scrape": "cfg"}, psmdb.Spec.Sharding.ConfigsvrReplSet.Annotations)
	assert.Empty(t, psmdb.Spec.Sharding.Mongos.Annotations)
}
