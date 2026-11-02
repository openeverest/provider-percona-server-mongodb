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
