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
	"testing"

	"github.com/AlekSi/pointer"
	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
)

// mongosConfiguration marshals a MongosParameters-compatible payload for use
// as a component's raw parameters in tests.
func mongosConfiguration(t *testing.T, configuration string) *runtime.RawExtension {
	t.Helper()

	raw, err := json.Marshal(map[string]any{"configuration": configuration})
	require.NoError(t, err)
	return &runtime.RawExtension{Raw: raw}
}

// mongosContext builds a sharded-topology Context with the given proxy
// component, for exercising configureMongos in isolation.
func mongosContext(t *testing.T, proxy corev1alpha1.ComponentSpec) *controller.Context {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1alpha1.AddToScheme(scheme))
	require.NoError(t, psmdbv1.SchemeBuilder.AddToScheme(scheme))

	in := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "my-mongo", Namespace: "db"},
		Spec: corev1alpha1.InstanceSpec{
			Topology: &corev1alpha1.TopologySpec{Type: "sharded"},
			Components: map[string]corev1alpha1.ComponentSpec{
				common.ComponentProxy: proxy,
			},
		},
	}
	var objs []client.Object
	objs = append(objs, in)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return controller.NewContext(context.Background(), fakeClient, in, "psmdb")
}

func TestConfigureMongosConfiguration(t *testing.T) {
	t.Parallel()

	t.Run("configuration is passed through verbatim", func(t *testing.T) {
		t.Parallel()
		c := mongosContext(t, corev1alpha1.ComponentSpec{
			Replicas:   pointer.ToInt32(3),
			Parameters: mongosConfiguration(t, "systemLog:\n  verbosity: 1\n"),
		})

		mongos, err := configureMongos(c)

		require.NoError(t, err)
		require.Equal(t, psmdbv1.MongoConfiguration("systemLog:\n  verbosity: 1\n"), mongos.Configuration)
	})

	t.Run("no parameters leaves configuration empty", func(t *testing.T) {
		t.Parallel()
		c := mongosContext(t, corev1alpha1.ComponentSpec{Replicas: pointer.ToInt32(3)})

		mongos, err := configureMongos(c)

		require.NoError(t, err)
		require.Empty(t, mongos.Configuration)
	})
}
