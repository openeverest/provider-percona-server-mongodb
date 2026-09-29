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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
)

func TestCopySecretDataKeepsExistingKeys(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	in := &corev1alpha1.Instance{ObjectMeta: metav1.ObjectMeta{Name: "my-mongo", Namespace: "db", UID: "uid"}}
	pmmCreds := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "pmm-creds", Namespace: "db"},
		Data:       map[string][]byte{"apiKey": []byte("token")},
	}
	users := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "everest-secrets-my-mongo", Namespace: "db"},
		Data:       map[string][]byte{"MONGODB_DATABASE_ADMIN_PASSWORD": []byte("pw")},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(in, pmmCreds, users).Build()
	c := controller.NewContext(context.Background(), cl, in, "psmdb")

	require.NoError(t, copySecretData(c, "pmm-creds", "everest-secrets-my-mongo", "apiKey", "PMM_SERVER_TOKEN"))

	got := &corev1.Secret{}
	require.NoError(t, c.Get(got, "everest-secrets-my-mongo"))
	assert.Equal(t, map[string][]byte{
		"MONGODB_DATABASE_ADMIN_PASSWORD": []byte("pw"),
		"PMM_SERVER_TOKEN":                []byte("token"),
	}, got.Data)
}

func TestConfigureMonitoringDisabledKeepsLiveResources(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1alpha1.AddToScheme(scheme))
	require.NoError(t, psmdbv1.SchemeBuilder.AddToScheme(scheme))

	in := &corev1alpha1.Instance{ObjectMeta: metav1.ObjectMeta{Name: "my-mongo", Namespace: "db"}}
	resources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("95m")},
	}
	live := &psmdbv1.PerconaServerMongoDB{
		ObjectMeta: metav1.ObjectMeta{Name: "my-mongo", Namespace: "db"},
		Spec:       psmdbv1.PerconaServerMongoDBSpec{PMM: psmdbv1.PMMSpec{Enabled: true, Resources: resources}},
	}

	for name, tt := range map[string]struct {
		objs []client.Object
		want corev1.ResourceRequirements
	}{
		"no cluster yet":      {objs: []client.Object{in}},
		"was being monitored": {objs: []client.Object{in, live}, want: resources},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.objs...).Build()
			c := controller.NewContext(context.Background(), cl, in, "psmdb")

			got, err := configureMonitoring(c, "everest-secrets-my-mongo")
			require.NoError(t, err)
			assert.False(t, got.Enabled)
			assert.Equal(t, tt.want, got.Resources)
		})
	}
}
