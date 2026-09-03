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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
)

// skewContext builds a hook-like Context: a client and no Instance.
func skewContext(t *testing.T, objs ...client.Object) *controller.Context {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1alpha1.AddToScheme(scheme))
	require.NoError(t, psmdbv1.SchemeBuilder.AddToScheme(scheme))
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return controller.NewContext(context.Background(), fakeClient, nil, "psmdb")
}

func skewInstance(name string) corev1alpha1.Instance {
	return corev1alpha1.Instance{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "db"}}
}

func skewPSMDB(name, crVersion string) *psmdbv1.PerconaServerMongoDB {
	return &psmdbv1.PerconaServerMongoDB{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "db"},
		Spec:       psmdbv1.PerconaServerMongoDBSpec{CRVersion: crVersion},
	}
}

func TestCheckUpgrade(t *testing.T) {
	p := &PSMDBProvider{}

	t.Run("outside the hook nothing is checked", func(t *testing.T) {
		t.Setenv(targetOperatorVersionEnv, "")

		issues := p.CheckUpgrade(skewContext(t), nil, []corev1alpha1.Instance{skewInstance("mongo-1")})

		assert.Empty(t, issues)
	})

	t.Run("deferred convergence outside the window blocks", func(t *testing.T) {
		t.Setenv(targetOperatorVersionEnv, "1.24.0")
		c := skewContext(t, skewPSMDB("mongo-1", "1.21.0"))

		issues := p.CheckUpgrade(c, nil, []corev1alpha1.Instance{skewInstance("mongo-1")})

		require.Len(t, issues, 1)
		assert.Equal(t, controller.UpgradeError, issues[0].Severity)
		assert.Equal(t, "SkewWindowExceeded", issues[0].Reason)
		assert.Contains(t, issues[0].Message, "approve the pending restart")
		assert.Equal(t, "mongo-1", issues[0].InstanceName)
	})

	t.Run("lag within the window passes", func(t *testing.T) {
		t.Setenv(targetOperatorVersionEnv, "1.24.0")
		c := skewContext(t, skewPSMDB("mongo-1", "1.22.0"))

		issues := p.CheckUpgrade(c, nil, []corev1alpha1.Instance{skewInstance("mongo-1")})

		assert.Empty(t, issues)
	})

	t.Run("converged cluster passes", func(t *testing.T) {
		t.Setenv(targetOperatorVersionEnv, "1.24.0")
		c := skewContext(t, skewPSMDB("mongo-1", "1.24.0"))

		issues := p.CheckUpgrade(c, nil, []corev1alpha1.Instance{skewInstance("mongo-1")})

		assert.Empty(t, issues)
	})

	t.Run("missing engine CR or unset crVersion is skipped", func(t *testing.T) {
		t.Setenv(targetOperatorVersionEnv, "1.24.0")
		c := skewContext(t, skewPSMDB("mongo-2", ""))

		issues := p.CheckUpgrade(c, nil, []corev1alpha1.Instance{
			skewInstance("mongo-1"), // no engine CR at all
			skewInstance("mongo-2"), // crVersion not yet pinned
		})

		assert.Empty(t, issues)
	})

	t.Run("unparseable versions degrade to warnings", func(t *testing.T) {
		t.Setenv(targetOperatorVersionEnv, "not-a-version")

		issues := p.CheckUpgrade(skewContext(t), nil, []corev1alpha1.Instance{skewInstance("mongo-1")})

		require.Len(t, issues, 1)
		assert.Equal(t, controller.UpgradeWarning, issues[0].Severity)
		assert.Equal(t, "SkewUnverified", issues[0].Reason)
	})

	t.Run("major version change is always outside the window", func(t *testing.T) {
		t.Setenv(targetOperatorVersionEnv, "2.0.0")
		c := skewContext(t, skewPSMDB("mongo-1", "1.24.0"))

		issues := p.CheckUpgrade(c, nil, []corev1alpha1.Instance{skewInstance("mongo-1")})

		require.Len(t, issues, 1)
		assert.Equal(t, controller.UpgradeError, issues[0].Severity)
	})
}
