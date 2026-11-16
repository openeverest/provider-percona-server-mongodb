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

	"github.com/AlekSi/pointer"
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

	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
)

func shardedContext(t *testing.T, configServerStorageClass *string, objs ...client.Object) *controller.Context {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1alpha1.AddToScheme(scheme))
	require.NoError(t, psmdbv1.SchemeBuilder.AddToScheme(scheme))

	in := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "my-mongo", Namespace: "db"},
		Spec: corev1alpha1.InstanceSpec{
			Topology: &corev1alpha1.TopologySpec{Type: "sharded"},
			Components: map[string]corev1alpha1.ComponentSpec{
				common.ComponentConfigServer: {
					Replicas: pointer.ToInt32(3),
					Storage: &corev1alpha1.Storage{
						Size:         resource.MustParse("10Gi"),
						StorageClass: configServerStorageClass,
					},
				},
			},
		},
	}
	objs = append(objs, in)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return controller.NewContext(context.Background(), fakeClient, in, "psmdb")
}

func existingShardedPSMDB(configServerStorageClass *string) *psmdbv1.PerconaServerMongoDB {
	return &psmdbv1.PerconaServerMongoDB{
		ObjectMeta: metav1.ObjectMeta{Name: "my-mongo", Namespace: "db"},
		Spec: psmdbv1.PerconaServerMongoDBSpec{
			Sharding: psmdbv1.Sharding{
				Enabled: true,
				ConfigsvrReplSet: &psmdbv1.ReplsetSpec{
					VolumeSpec: &psmdbv1.VolumeSpec{
						PersistentVolumeClaim: psmdbv1.PVCSpec{
							PersistentVolumeClaimSpec: &corev1.PersistentVolumeClaimSpec{
								StorageClassName: configServerStorageClass,
							},
						},
					},
				},
			},
		},
	}
}

func TestConfigureExpose(t *testing.T) {
	t.Parallel()

	t.Run("nil service defaults to ClusterIP", func(t *testing.T) {
		t.Parallel()
		ex := configureExpose(nil)

		assert.Equal(t, corev1.ServiceTypeClusterIP, ex.ExposeType)
		assert.Empty(t, ex.ServiceAnnotations)
		assert.Empty(t, ex.LoadBalancerSourceRanges)
	})

	t.Run("service type and annotations are passed through", func(t *testing.T) {
		t.Parallel()
		ex := configureExpose(&corev1alpha1.Service{
			ServiceType: corev1.ServiceTypeNodePort,
			Annotations: map[string]string{"foo": "bar"},
		})

		assert.Equal(t, corev1.ServiceTypeNodePort, ex.ExposeType)
		assert.Equal(t, map[string]string{"foo": "bar"}, ex.ServiceAnnotations)
		assert.Empty(t, ex.LoadBalancerSourceRanges)
	})

	t.Run("LoadBalancer source ranges are normalized", func(t *testing.T) {
		t.Parallel()
		ex := configureExpose(&corev1alpha1.Service{
			ServiceType: corev1.ServiceTypeLoadBalancer,
			LoadBalancerService: &corev1alpha1.LoadBalancerService{
				SourceRanges: corev1alpha1.SourceRanges{"10.0.0.0/8", "192.168.1.100"},
			},
		})

		assert.Equal(t, corev1.ServiceTypeLoadBalancer, ex.ExposeType)
		assert.Equal(t, []string{"10.0.0.0/8", "192.168.1.100/32"}, ex.LoadBalancerSourceRanges)
	})

	t.Run("source ranges are ignored unless LoadBalancer", func(t *testing.T) {
		t.Parallel()
		ex := configureExpose(&corev1alpha1.Service{
			ServiceType: corev1.ServiceTypeClusterIP,
			LoadBalancerService: &corev1alpha1.LoadBalancerService{
				SourceRanges: corev1alpha1.SourceRanges{"10.0.0.0/8"},
			},
		})

		assert.Equal(t, corev1.ServiceTypeClusterIP, ex.ExposeType)
		assert.Empty(t, ex.LoadBalancerSourceRanges)
	})
}

// replicaSetContext builds a replicaSet-topology Context with the given
// engine component, for exercising configureReplsets in isolation.
func replicaSetContext(t *testing.T, engine corev1alpha1.ComponentSpec) *controller.Context {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1alpha1.AddToScheme(scheme))
	require.NoError(t, psmdbv1.SchemeBuilder.AddToScheme(scheme))

	in := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "my-mongo", Namespace: "db"},
		Spec: corev1alpha1.InstanceSpec{
			Components: map[string]corev1alpha1.ComponentSpec{
				common.ComponentEngine: engine,
			},
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(in).Build()
	return controller.NewContext(context.Background(), fakeClient, in, "psmdb")
}

func TestConfigureReplsetsExpose(t *testing.T) {
	t.Parallel()

	t.Run("replicaSet topology exposes the engine", func(t *testing.T) {
		t.Parallel()
		c := replicaSetContext(t, corev1alpha1.ComponentSpec{
			Replicas: pointer.ToInt32(3),
			Service:  &corev1alpha1.Service{ServiceType: corev1.ServiceTypeLoadBalancer},
		})

		replsets, err := configureReplsets(c)

		require.NoError(t, err)
		require.Len(t, replsets, 1)
		assert.True(t, replsets[0].Expose.Enabled)
		assert.Equal(t, corev1.ServiceTypeLoadBalancer, replsets[0].Expose.ExposeType)
	})

	t.Run("sharded topology never exposes shard replsets", func(t *testing.T) {
		t.Parallel()
		c := shardedContext(t, nil)
		c.Instance().Spec.Components[common.ComponentEngine] = corev1alpha1.ComponentSpec{
			Replicas: pointer.ToInt32(3),
			Service:  &corev1alpha1.Service{ServiceType: corev1.ServiceTypeLoadBalancer},
		}

		replsets, err := configureReplsets(c)

		require.NoError(t, err)
		for _, rs := range replsets {
			assert.False(t, rs.Expose.Enabled, "shard replsets must not be exposed directly")
		}
	})
}

func TestConfigureConfigServerReplsetStorageClass(t *testing.T) {
	tests := []struct {
		name      string
		requested *string
		existing  *psmdbv1.PerconaServerMongoDB
		want      *string
	}{
		{
			name:      "new cluster uses the requested storage class",
			requested: pointer.ToString("fast"),
			want:      pointer.ToString("fast"),
		},
		{
			name:      "cluster created on the default class keeps it",
			requested: pointer.ToString("fast"),
			existing:  existingShardedPSMDB(nil),
			want:      nil,
		},
		{
			name:      "cluster keeps the class it was created with",
			requested: pointer.ToString("fast"),
			existing:  existingShardedPSMDB(pointer.ToString("slow")),
			want:      pointer.ToString("slow"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objs []client.Object
			if tt.existing != nil {
				objs = append(objs, tt.existing)
			}
			c := shardedContext(t, tt.requested, objs...)

			rs, err := configureConfigServerReplset(c)

			require.NoError(t, err)
			assert.Equal(t, tt.want, rs.VolumeSpec.PersistentVolumeClaim.StorageClassName)
		})
	}
}
