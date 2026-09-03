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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
)

func crVersionContext(t *testing.T, maintenance *corev1alpha1.MaintenanceSpec, objs ...client.Object) *controller.Context {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1alpha1.AddToScheme(scheme))
	require.NoError(t, backupv1alpha1.AddToScheme(scheme))
	require.NoError(t, psmdbv1.SchemeBuilder.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))

	orig := currentNamespace
	currentNamespace = func() (string, error) { return "everest-system", nil }
	t.Cleanup(func() { currentNamespace = orig })

	in := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "my-mongo", Namespace: "db"},
		Spec:       corev1alpha1.InstanceSpec{Maintenance: maintenance},
	}
	provider := &corev1alpha1.Provider{
		ObjectMeta: metav1.ObjectMeta{Name: "psmdb"},
		Spec:       corev1alpha1.ProviderSpec{Release: &corev1alpha1.Release{Version: "0.3"}},
	}
	operator := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "release-operator", Namespace: "everest-system"},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Image: "percona/percona-server-mongodb-operator:1.22.0"}},
		}}},
	}
	objs = append(objs, in, provider, operator)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return controller.NewContext(context.Background(), fakeClient, in, "psmdb")
}

func existingPSMDB(crVersion string) *psmdbv1.PerconaServerMongoDB {
	return &psmdbv1.PerconaServerMongoDB{
		ObjectMeta: metav1.ObjectMeta{Name: "my-mongo", Namespace: "db"},
		Spec:       psmdbv1.PerconaServerMongoDBSpec{CRVersion: crVersion},
	}
}

func TestConvergedCRVersion(t *testing.T) {
	t.Run("new cluster leaves crVersion for the operator to pin", func(t *testing.T) {
		c := crVersionContext(t, nil)

		got, err := convergedCRVersion(c)

		require.NoError(t, err)
		assert.Empty(t, got)
		assert.Empty(t, c.GetPendingMaintenance())
	})

	t.Run("converged cluster stays put without maintenance", func(t *testing.T) {
		c := crVersionContext(t, nil, existingPSMDB("1.22.0"))

		got, err := convergedCRVersion(c)

		require.NoError(t, err)
		assert.Equal(t, "1.22.0", got)
		assert.Empty(t, c.GetPendingMaintenance())
	})

	t.Run("semantically equal version is not a lag", func(t *testing.T) {
		c := crVersionContext(t, nil, existingPSMDB("1.22"))

		got, err := convergedCRVersion(c)

		require.NoError(t, err)
		assert.Equal(t, "1.22", got)
		assert.Empty(t, c.GetPendingMaintenance())
	})

	t.Run("lagging cluster is held at its current version by default", func(t *testing.T) {
		c := crVersionContext(t, nil, existingPSMDB("1.21.0"))

		got, err := convergedCRVersion(c)

		require.NoError(t, err)
		assert.Equal(t, "1.21.0", got, "the bump must not happen without approval")
		pending := c.GetPendingMaintenance()
		require.Len(t, pending, 1)
		assert.Equal(t, "upgrade-to-0.3", pending[0].ApprovalToken, "token names the provider release, not operator internals")
		assert.Equal(t, corev1alpha1.MaintenanceRollingRestart, pending[0].Severity)
	})

	t.Run("tolerant instance converges hands-off", func(t *testing.T) {
		c := crVersionContext(t,
			&corev1alpha1.MaintenanceSpec{AutoApproveUpTo: corev1alpha1.MaintenanceRollingRestart},
			existingPSMDB("1.21.0"))

		got, err := convergedCRVersion(c)

		require.NoError(t, err)
		assert.Equal(t, "1.22.0", got)
		assert.Empty(t, c.GetPendingMaintenance())
	})

	t.Run("token approval converges a held cluster", func(t *testing.T) {
		c := crVersionContext(t,
			&corev1alpha1.MaintenanceSpec{Approved: "upgrade-to-0.3"},
			existingPSMDB("1.21.0"))

		got, err := convergedCRVersion(c)

		require.NoError(t, err)
		assert.Equal(t, "1.22.0", got)
		assert.Empty(t, c.GetPendingMaintenance())
	})

	t.Run("mid-creation empty crVersion is left for the operator", func(t *testing.T) {
		c := crVersionContext(t, nil, existingPSMDB(""))

		got, err := convergedCRVersion(c)

		require.NoError(t, err)
		assert.Empty(t, got)
		assert.Empty(t, c.GetPendingMaintenance())
	})

	t.Run("cluster ahead of the operator is left alone", func(t *testing.T) {
		// A provider chart rollback leaves the CR ahead of the running
		// operator; converging would be a downgrade, which is never offered.
		c := crVersionContext(t, nil, existingPSMDB("1.23.0"))

		got, err := convergedCRVersion(c)

		require.NoError(t, err)
		assert.Equal(t, "1.23.0", got)
		assert.Empty(t, c.GetPendingMaintenance())
	})

	t.Run("discovery failure holds the current version without failing", func(t *testing.T) {
		c := crVersionContext(t, nil, existingPSMDB("1.21.0"))
		orig := currentNamespace
		currentNamespace = func() (string, error) { return "", assert.AnError }
		t.Cleanup(func() { currentNamespace = orig })

		got, err := convergedCRVersion(c)

		require.NoError(t, err, "a discovery failure must not stop unrelated spec management")
		assert.Equal(t, "1.21.0", got)
		assert.Empty(t, c.GetPendingMaintenance())
	})
}
