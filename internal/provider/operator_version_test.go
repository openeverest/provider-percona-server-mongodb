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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
)

func TestOperatorImageTag(t *testing.T) {
	tests := []struct {
		name    string
		image   string
		wantTag string
		wantOK  bool
	}{
		{"plain", "percona/percona-server-mongodb-operator:1.22.0", "1.22.0", true},
		{"v-prefixed tag", "percona/percona-server-mongodb-operator:v1.22.0", "1.22.0", true},
		{"registry with port", "myregistry.example.com:5000/percona/percona-server-mongodb-operator:1.22.0", "1.22.0", true},
		{"tag plus digest", "percona/percona-server-mongodb-operator:1.22.0@sha256:deadbeef", "1.22.0", true},
		{"digest only", "percona/percona-server-mongodb-operator@sha256:deadbeef", "", false},
		{"no tag", "percona/percona-server-mongodb-operator", "", false},
		{"different image", "percona/percona-server-mongodb:8.0.12-4", "", false},
		{"registry with port, different image", "myregistry.example.com:5000/other/thing:1.0", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tag, ok := operatorImageTag(tt.image)

			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantTag, tag)
		})
	}
}

func operatorDeployment(name, image string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "everest-system"},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Image: image}},
		}}},
	}
}

func operatorVersionContext(t *testing.T, objs ...client.Object) *controller.Context {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, appsv1.AddToScheme(scheme))

	orig := currentNamespace
	currentNamespace = func() (string, error) { return "everest-system", nil }
	t.Cleanup(func() { currentNamespace = orig })

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return controller.NewContext(context.Background(), fakeClient, nil, "psmdb")
}

func TestOperatorVersion(t *testing.T) {
	t.Run("found via a ported private registry", func(t *testing.T) {
		c := operatorVersionContext(t,
			operatorDeployment("op", "reg.example.com:5000/percona/percona-server-mongodb-operator:1.22.0"))

		got, err := operatorVersion(c)

		require.NoError(t, err)
		assert.Equal(t, "1.22.0", got)
	})

	t.Run("no matching deployment errors", func(t *testing.T) {
		c := operatorVersionContext(t, operatorDeployment("other", "nginx:1.25"))

		_, err := operatorVersion(c)

		require.ErrorContains(t, err, "not found")
	})

	t.Run("conflicting versions error", func(t *testing.T) {
		c := operatorVersionContext(t,
			operatorDeployment("op-a", "percona/percona-server-mongodb-operator:1.22.0"),
			operatorDeployment("op-b", "percona/percona-server-mongodb-operator:1.23.0"))

		_, err := operatorVersion(c)

		require.ErrorContains(t, err, "conflicting versions")
	})

	t.Run("duplicate deployments with the same version agree", func(t *testing.T) {
		c := operatorVersionContext(t,
			operatorDeployment("op-a", "percona/percona-server-mongodb-operator:1.22.0"),
			operatorDeployment("op-b", "percona/percona-server-mongodb-operator:1.22.0"))

		got, err := operatorVersion(c)

		require.NoError(t, err)
		assert.Equal(t, "1.22.0", got)
	})
}
