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
	"github.com/percona/percona-server-mongodb-operator/pkg/naming"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
)

const (
	hiddenTestName      = "my-mongo"
	hiddenTestNamespace = "db"
)

func hiddenTestEngine() corev1alpha1.ComponentSpec {
	return corev1alpha1.ComponentSpec{
		Replicas: pointer.ToInt32(3),
		Storage: &corev1alpha1.Storage{
			Size:         resource.MustParse("25Gi"),
			StorageClass: pointer.ToString("engine-class"),
		},
		Resources: &corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("4Gi"),
			},
		},
	}
}

func hiddenContext(t *testing.T, topology string, hidden *corev1alpha1.ComponentSpec, objs ...client.Object) *controller.Context {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, psmdbv1.SchemeBuilder.AddToScheme(scheme))

	components := map[string]corev1alpha1.ComponentSpec{
		common.ComponentEngine: hiddenTestEngine(),
	}
	if hidden != nil {
		components[common.ComponentHidden] = *hidden
	}
	in := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: hiddenTestName, Namespace: hiddenTestNamespace},
		Spec: corev1alpha1.InstanceSpec{
			Topology:   &corev1alpha1.TopologySpec{Type: topology},
			Components: components,
		},
	}
	objs = append(objs, in)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return controller.NewContext(context.Background(), fakeClient, in, "psmdb")
}

func liveWithHidden(storageClass *string) *psmdbv1.PerconaServerMongoDB {
	return &psmdbv1.PerconaServerMongoDB{
		ObjectMeta: metav1.ObjectMeta{Name: hiddenTestName, Namespace: hiddenTestNamespace},
		Spec: psmdbv1.PerconaServerMongoDBSpec{
			Replsets: []*psmdbv1.ReplsetSpec{{
				Name: "rs0",
				Hidden: psmdbv1.HiddenSpec{
					Enabled: true,
					Size:    2,
					VolumeSpec: &psmdbv1.VolumeSpec{
						PersistentVolumeClaim: psmdbv1.PVCSpec{
							PersistentVolumeClaimSpec: &corev1.PersistentVolumeClaimSpec{
								StorageClassName: storageClass,
							},
						},
					},
				},
			}},
		},
	}
}

func storageRequest(t *testing.T, volume *psmdbv1.VolumeSpec) resource.Quantity {
	t.Helper()
	require.NotNil(t, volume)
	return volume.PersistentVolumeClaim.Resources.Requests[corev1.ResourceStorage]
}

func TestConfigureReplsetsHidden(t *testing.T) {
	t.Run("off without the component", func(t *testing.T) {
		c := hiddenContext(t, "replicaSet", nil)

		replsets, err := configureReplsets(c)

		require.NoError(t, err)
		require.Len(t, replsets, 1)
		assert.False(t, replsets[0].Hidden.Enabled)
	})

	t.Run("off while replicas are unset", func(t *testing.T) {
		// A form that switched hidden members off can leave the component
		// behind with other fields, such as a scheduling policy.
		c := hiddenContext(t, "replicaSet", &corev1alpha1.ComponentSpec{
			Storage: &corev1alpha1.Storage{Size: resource.MustParse("50Gi")},
		})

		replsets, err := configureReplsets(c)

		require.NoError(t, err)
		assert.False(t, replsets[0].Hidden.Enabled)
	})

	t.Run("unset resources and storage match the engine's", func(t *testing.T) {
		c := hiddenContext(t, "replicaSet", &corev1alpha1.ComponentSpec{Replicas: pointer.ToInt32(2)})

		replsets, err := configureReplsets(c)

		require.NoError(t, err)
		hidden := replsets[0].Hidden
		assert.True(t, hidden.Enabled)
		assert.Equal(t, int32(2), hidden.Size)
		assert.True(t, hidden.Resources.Limits.Cpu().Equal(resource.MustParse("1")))
		assert.True(t, hidden.Resources.Limits.Memory().Equal(resource.MustParse("4Gi")))
		size := storageRequest(t, hidden.VolumeSpec)
		assert.True(t, size.Equal(resource.MustParse("25Gi")))
		assert.Equal(t, pointer.ToString("engine-class"), hidden.VolumeSpec.PersistentVolumeClaim.StorageClassName)
		require.NotNil(t, hidden.PodDisruptionBudget)
		assert.Equal(t, 1, hidden.PodDisruptionBudget.MaxUnavailable.IntValue())
	})

	t.Run("own resources and storage", func(t *testing.T) {
		c := hiddenContext(t, "replicaSet", &corev1alpha1.ComponentSpec{
			Replicas: pointer.ToInt32(4),
			Resources: &corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("2"),
					corev1.ResourceMemory: resource.MustParse("8Gi"),
				},
			},
			Storage: &corev1alpha1.Storage{
				Size:         resource.MustParse("100Gi"),
				StorageClass: pointer.ToString("cheap"),
			},
		})

		replsets, err := configureReplsets(c)

		require.NoError(t, err)
		hidden := replsets[0].Hidden
		assert.Equal(t, int32(4), hidden.Size)
		assert.True(t, hidden.Resources.Limits.Cpu().Equal(resource.MustParse("2")))
		assert.True(t, hidden.Resources.Limits.Memory().Equal(resource.MustParse("8Gi")))
		size := storageRequest(t, hidden.VolumeSpec)
		assert.True(t, size.Equal(resource.MustParse("100Gi")))
		assert.Equal(t, pointer.ToString("cheap"), hidden.VolumeSpec.PersistentVolumeClaim.StorageClassName)
		// The replica set's own members keep the engine's.
		engineSize := storageRequest(t, replsets[0].VolumeSpec)
		assert.True(t, engineSize.Equal(resource.MustParse("25Gi")))
		assert.Equal(t, pointer.ToString("engine-class"), replsets[0].VolumeSpec.PersistentVolumeClaim.StorageClassName)
	})

	t.Run("only the size set keeps the engine's storage class", func(t *testing.T) {
		c := hiddenContext(t, "replicaSet", &corev1alpha1.ComponentSpec{
			Replicas: pointer.ToInt32(2),
			Storage:  &corev1alpha1.Storage{Size: resource.MustParse("40Gi")},
		})

		replsets, err := configureReplsets(c)

		require.NoError(t, err)
		hidden := replsets[0].Hidden
		size := storageRequest(t, hidden.VolumeSpec)
		assert.True(t, size.Equal(resource.MustParse("40Gi")))
		assert.Equal(t, pointer.ToString("engine-class"), hidden.VolumeSpec.PersistentVolumeClaim.StorageClassName)
	})

	t.Run("running hidden members keep their storage class", func(t *testing.T) {
		c := hiddenContext(t, "replicaSet", &corev1alpha1.ComponentSpec{
			Replicas: pointer.ToInt32(2),
			Storage:  &corev1alpha1.Storage{StorageClass: pointer.ToString("fast")},
		}, liveWithHidden(pointer.ToString("slow")))

		replsets, err := configureReplsets(c)

		require.NoError(t, err)
		assert.Equal(t, pointer.ToString("slow"), replsets[0].Hidden.VolumeSpec.PersistentVolumeClaim.StorageClassName)
	})

	t.Run("hidden members turned on again take the requested class", func(t *testing.T) {
		live := liveWithHidden(pointer.ToString("slow"))
		live.Spec.Replsets[0].Hidden.Enabled = false
		c := hiddenContext(t, "replicaSet", &corev1alpha1.ComponentSpec{
			Replicas: pointer.ToInt32(2),
			Storage:  &corev1alpha1.Storage{StorageClass: pointer.ToString("fast")},
		}, live)

		replsets, err := configureReplsets(c)

		require.NoError(t, err)
		assert.Equal(t, pointer.ToString("fast"), replsets[0].Hidden.VolumeSpec.PersistentVolumeClaim.StorageClassName)
	})

	t.Run("not added to shards", func(t *testing.T) {
		c := hiddenContext(t, "sharded", &corev1alpha1.ComponentSpec{Replicas: pointer.ToInt32(2)})

		replsets, err := configureReplsets(c)

		require.NoError(t, err)
		for _, rs := range replsets {
			assert.False(t, rs.Hidden.Enabled, rs.Name)
		}
	})
}

func TestMirrorReplsetOntoHidden(t *testing.T) {
	exporter := corev1.Container{Name: metricsExporterContainerName}
	withHidden := &psmdbv1.ReplsetSpec{
		Name:          "rs0",
		Configuration: "net:\n  tls:\n    allowConnectionsWithoutCertificates: true\n",
		MultiAZ:       psmdbv1.MultiAZ{Sidecars: []corev1.Container{exporter}},
		Hidden:        psmdbv1.HiddenSpec{Enabled: true, Size: 2},
	}
	withoutHidden := &psmdbv1.ReplsetSpec{
		Name:          "rs1",
		Configuration: "operationProfiling:\n  mode: slowOp\n",
		MultiAZ:       psmdbv1.MultiAZ{Sidecars: []corev1.Container{exporter}},
	}
	psmdb := &psmdbv1.PerconaServerMongoDB{
		Spec: psmdbv1.PerconaServerMongoDBSpec{Replsets: []*psmdbv1.ReplsetSpec{withHidden, withoutHidden}},
	}

	mirrorReplsetOntoHidden(psmdb)

	assert.Equal(t, withHidden.Configuration, withHidden.Hidden.Configuration)
	assert.Equal(t, []corev1.Container{exporter}, withHidden.Hidden.Sidecars)
	assert.Empty(t, withoutHidden.Hidden.Configuration)
	assert.Empty(t, withoutHidden.Hidden.Sidecars)

	// The copy is independent: a later change to the replica set's sidecars
	// must not leak into the hidden members'.
	withHidden.Sidecars[0].Name = "changed"
	assert.Equal(t, metricsExporterContainerName, withHidden.Hidden.Sidecars[0].Name)
}

func hiddenPVC(name, component string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: hiddenTestNamespace,
			Labels: map[string]string{
				naming.LabelKubernetesInstance:  hiddenTestName,
				naming.LabelKubernetesComponent: component,
				naming.LabelKubernetesReplset:   "rs0",
			},
		},
	}
}

func hiddenPod(name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: hiddenTestNamespace,
			Labels: map[string]string{
				naming.LabelKubernetesInstance:  hiddenTestName,
				naming.LabelKubernetesComponent: naming.ComponentHidden,
			},
		},
	}
}

func remainingPVCs(t *testing.T, c *controller.Context) []string {
	t.Helper()
	pvcs := &corev1.PersistentVolumeClaimList{}
	require.NoError(t, c.List(pvcs))
	names := make([]string, 0, len(pvcs.Items))
	for _, pvc := range pvcs.Items {
		names = append(names, pvc.Name)
	}
	return names
}

func TestDeleteUnusedHiddenVolumes(t *testing.T) {
	engineVolume := hiddenPVC("mongod-data-my-mongo-rs0-0", naming.ComponentMongod)
	hidden0 := hiddenPVC("mongod-data-my-mongo-rs0-hidden-0", naming.ComponentHidden)
	hidden1 := hiddenPVC("mongod-data-my-mongo-rs0-hidden-1", naming.ComponentHidden)
	hidden2 := hiddenPVC("mongod-data-my-mongo-rs0-hidden-2", naming.ComponentHidden)
	hidden3 := hiddenPVC("mongod-data-my-mongo-rs0-hidden-3", naming.ComponentHidden)
	all := []string{engineVolume.Name, hidden0.Name, hidden1.Name, hidden2.Name, hidden3.Name}

	tests := []struct {
		name   string
		hidden *corev1alpha1.ComponentSpec
		pods   []string
		want   []string
	}{
		{
			name:   "volumes of current hidden members stay",
			hidden: &corev1alpha1.ComponentSpec{Replicas: pointer.ToInt32(4)},
			want:   all,
		},
		{
			name:   "scaled down: volumes beyond the size go once their pods are gone",
			hidden: &corev1alpha1.ComponentSpec{Replicas: pointer.ToInt32(2)},
			pods:   []string{"my-mongo-rs0-hidden-0", "my-mongo-rs0-hidden-1"},
			want:   []string{engineVolume.Name, hidden0.Name, hidden1.Name},
		},
		{
			name:   "scaled down: a volume stays while its pod is still running",
			hidden: &corev1alpha1.ComponentSpec{Replicas: pointer.ToInt32(2)},
			pods:   []string{"my-mongo-rs0-hidden-0", "my-mongo-rs0-hidden-1", "my-mongo-rs0-hidden-3"},
			want:   []string{engineVolume.Name, hidden0.Name, hidden1.Name, hidden3.Name},
		},
		{
			name: "turned off: every hidden volume goes once the pods are gone",
			want: []string{engineVolume.Name},
		},
		{
			name: "turned off: volumes stay while hidden pods are still running",
			pods: []string{"my-mongo-rs0-hidden-0", "my-mongo-rs0-hidden-1", "my-mongo-rs0-hidden-2", "my-mongo-rs0-hidden-3"},
			want: all,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := []client.Object{engineVolume.DeepCopy(), hidden0.DeepCopy(), hidden1.DeepCopy(), hidden2.DeepCopy(), hidden3.DeepCopy()}
			for _, pod := range tt.pods {
				objs = append(objs, hiddenPod(pod))
			}
			c := hiddenContext(t, "replicaSet", tt.hidden, objs...)

			require.NoError(t, deleteUnusedHiddenVolumes(c))

			assert.ElementsMatch(t, tt.want, remainingPVCs(t, c))
		})
	}
}

func TestValidateHidden(t *testing.T) {
	tests := []struct {
		name      string
		topology  string
		hidden    *corev1alpha1.ComponentSpec
		expectErr string
	}{
		{
			name:     "no hidden members",
			topology: "replicaSet",
		},
		{
			name:     "component left without replicas",
			topology: "replicaSet",
			hidden:   &corev1alpha1.ComponentSpec{Storage: &corev1alpha1.Storage{Size: resource.MustParse("100Mi")}},
		},
		{
			name:     "two hidden members",
			topology: "replicaSet",
			hidden:   &corev1alpha1.ComponentSpec{Replicas: pointer.ToInt32(2)},
		},
		{
			name:      "odd number of hidden members",
			topology:  "replicaSet",
			hidden:    &corev1alpha1.ComponentSpec{Replicas: pointer.ToInt32(1)},
			expectErr: "positive even number",
		},
		{
			name:      "negative replicas",
			topology:  "replicaSet",
			hidden:    &corev1alpha1.ComponentSpec{Replicas: pointer.ToInt32(-2)},
			expectErr: "positive even number",
		},
		{
			name:      "more than seven voting members",
			topology:  "replicaSet",
			hidden:    &corev1alpha1.ComponentSpec{Replicas: pointer.ToInt32(6)},
			expectErr: "must not exceed 7 voting members, got 9",
		},
		{
			name:      "sharded topology",
			topology:  "sharded",
			hidden:    &corev1alpha1.ComponentSpec{Replicas: pointer.ToInt32(2)},
			expectErr: "only in the replica set topology",
		},
		{
			name:     "storage below the minimum",
			topology: "replicaSet",
			hidden: &corev1alpha1.ComponentSpec{
				Replicas: pointer.ToInt32(2),
				Storage:  &corev1alpha1.Storage{Size: resource.MustParse("100Mi")},
			},
			expectErr: "hidden.storage.size must be >= 1Gi",
		},
		{
			name:     "cpu below the minimum",
			topology: "replicaSet",
			hidden: &corev1alpha1.ComponentSpec{
				Replicas: pointer.ToInt32(2),
				Resources: &corev1.ResourceRequirements{
					Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
				},
			},
			expectErr: "hidden.resources.limits.cpu must be >= 600m",
		},
		{
			name:     "memory below the minimum",
			topology: "replicaSet",
			hidden: &corev1alpha1.ComponentSpec{
				Replicas: pointer.ToInt32(2),
				Resources: &corev1.ResourceRequirements{
					Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")},
				},
			},
			expectErr: "hidden.resources.limits.memory must be >= 512Mi",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := hiddenContext(t, tt.topology, tt.hidden)

			err := validateHidden(c)

			if tt.expectErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectErr)
		})
	}
}

func TestHiddenScheduling(t *testing.T) {
	affinity := &corev1.Affinity{
		NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{{
					MatchExpressions: []corev1.NodeSelectorRequirement{{
						Key:      "pool",
						Operator: corev1.NodeSelectorOpIn,
						Values:   []string{"analytics"},
					}},
				}},
			},
		},
	}

	tests := []struct {
		name   string
		policy *commonv1alpha1.SchedulingPolicy
		check  func(t *testing.T, az psmdbv1.MultiAZ)
	}{
		{
			name: "one hidden member per node by default",
			check: func(t *testing.T, az psmdbv1.MultiAZ) {
				require.NotNil(t, az.Affinity)
				assert.Equal(t, pointer.ToString(corev1.LabelHostname), az.Affinity.TopologyKey)
			},
		},
		{
			name:   "own affinity",
			policy: &commonv1alpha1.SchedulingPolicy{Affinity: affinity},
			check: func(t *testing.T, az psmdbv1.MultiAZ) {
				require.NotNil(t, az.Affinity)
				assert.Equal(t, affinity, az.Affinity.Advanced)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := hiddenContext(t, "replicaSet", &corev1alpha1.ComponentSpec{
				Replicas:         pointer.ToInt32(2),
				SchedulingPolicy: tt.policy,
			})
			replsets, err := configureReplsets(c)
			require.NoError(t, err)
			psmdb := &psmdbv1.PerconaServerMongoDB{
				ObjectMeta: metav1.ObjectMeta{Name: hiddenTestName, Namespace: hiddenTestNamespace},
				Spec:       psmdbv1.PerconaServerMongoDBSpec{Replsets: replsets},
			}

			applyScheduling(c, psmdb)
			labelPods(c, psmdb)

			hidden := psmdb.Spec.Replsets[0].Hidden
			tt.check(t, hidden.MultiAZ)
			assert.Equal(t, common.ComponentHidden, hidden.Labels[controller.ComponentLabel])
			assert.Equal(t, common.ComponentEngine, psmdb.Spec.Replsets[0].Labels[controller.ComponentLabel])
			assert.Contains(t, c.LabelledComponents(), common.ComponentHidden)
		})
	}

	t.Run("hidden members off are neither scheduled nor counted", func(t *testing.T) {
		c := hiddenContext(t, "replicaSet", nil)
		replsets, err := configureReplsets(c)
		require.NoError(t, err)
		psmdb := &psmdbv1.PerconaServerMongoDB{
			ObjectMeta: metav1.ObjectMeta{Name: hiddenTestName, Namespace: hiddenTestNamespace},
			Spec:       psmdbv1.PerconaServerMongoDBSpec{Replsets: replsets},
		}

		applyScheduling(c, psmdb)
		labelPods(c, psmdb)

		assert.Nil(t, psmdb.Spec.Replsets[0].Hidden.Affinity)
		assert.Empty(t, psmdb.Spec.Replsets[0].Hidden.Labels)
		assert.NotContains(t, c.LabelledComponents(), common.ComponentHidden)
	})
}

func loadBalancerService(name string, ingress corev1.LoadBalancerIngress) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: hiddenTestNamespace},
		Status: corev1.ServiceStatus{
			LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{ingress}},
		},
	}
}

func TestWithoutHiddenHosts(t *testing.T) {
	cluster := &psmdbv1.PerconaServerMongoDB{
		ObjectMeta: metav1.ObjectMeta{Name: hiddenTestName, Namespace: hiddenTestNamespace},
		Spec: psmdbv1.PerconaServerMongoDBSpec{
			Replsets: []*psmdbv1.ReplsetSpec{{Name: "rs0", Hidden: psmdbv1.HiddenSpec{Enabled: true, Size: 2}}},
		},
	}
	internal := "my-mongo-rs0-0.my-mongo-rs0.db.svc.cluster.local:27017," +
		"my-mongo-rs0-1.my-mongo-rs0.db.svc.cluster.local:27017," +
		"my-mongo-rs0-hidden-0.my-mongo-rs0.db.svc.cluster.local:27017," +
		"my-mongo-rs0-hidden-1.my-mongo-rs0.db.svc.cluster.local:27017"

	tests := []struct {
		name  string
		hosts string
		objs  []client.Object
		want  string
	}{
		{
			name:  "no hidden pods",
			hosts: "my-mongo-rs0-0.my-mongo-rs0.db.svc.cluster.local:27017,my-mongo-rs0-1.my-mongo-rs0.db.svc.cluster.local:27017",
			want:  "my-mongo-rs0-0.my-mongo-rs0.db.svc.cluster.local:27017,my-mongo-rs0-1.my-mongo-rs0.db.svc.cluster.local:27017",
		},
		{
			name:  "pod DNS names",
			hosts: internal,
			objs:  []client.Object{hiddenPod("my-mongo-rs0-hidden-0"), hiddenPod("my-mongo-rs0-hidden-1")},
			want:  "my-mongo-rs0-0.my-mongo-rs0.db.svc.cluster.local:27017,my-mongo-rs0-1.my-mongo-rs0.db.svc.cluster.local:27017",
		},
		{
			name:  "load balancer addresses",
			hosts: "10.0.0.1:27017,10.0.0.2:27017,10.0.0.20:27017,lb-hidden-1.example.com:27017",
			objs: []client.Object{
				hiddenPod("my-mongo-rs0-hidden-0"), hiddenPod("my-mongo-rs0-hidden-1"),
				loadBalancerService("my-mongo-rs0-hidden-0", corev1.LoadBalancerIngress{IP: "10.0.0.2"}),
				loadBalancerService("my-mongo-rs0-hidden-1", corev1.LoadBalancerIngress{Hostname: "lb-hidden-1.example.com"}),
			},
			want: "10.0.0.1:27017,10.0.0.20:27017",
		},
		{
			name:  "a single replica set service host is kept",
			hosts: "my-mongo-rs0.db.svc.cluster.local",
			objs:  []client.Object{hiddenPod("my-mongo-rs0-hidden-0")},
			want:  "my-mongo-rs0.db.svc.cluster.local",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := hiddenContext(t, "replicaSet", nil, tt.objs...)

			got, err := withoutHiddenHosts(c, cluster, tt.hosts)

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPreferHiddenForBackups(t *testing.T) {
	cluster := func(hidden psmdbv1.HiddenSpec) *psmdbv1.PerconaServerMongoDB {
		return &psmdbv1.PerconaServerMongoDB{
			ObjectMeta: metav1.ObjectMeta{Name: hiddenTestName, Namespace: hiddenTestNamespace},
			Spec: psmdbv1.PerconaServerMongoDBSpec{
				Replsets: []*psmdbv1.ReplsetSpec{{Name: "rs0", Size: 3, Hidden: hidden}},
				Backup: psmdbv1.BackupSpec{
					Configuration: psmdbv1.BackupConfig{BackupOptions: &psmdbv1.BackupOptions{}},
				},
			},
		}
	}

	t.Run("hidden members are preferred", func(t *testing.T) {
		psmdb := cluster(psmdbv1.HiddenSpec{Enabled: true, Size: 2})

		preferHiddenForBackups(psmdb)

		assert.Equal(t, map[string]float64{
			"my-mongo-rs0-hidden-0.my-mongo-rs0.db.svc.cluster.local:27017": 2.0,
			"my-mongo-rs0-hidden-1.my-mongo-rs0.db.svc.cluster.local:27017": 2.0,
		}, psmdb.Spec.Backup.Configuration.BackupOptions.Priority)
	})

	t.Run("without hidden members the operator keeps choosing", func(t *testing.T) {
		psmdb := cluster(psmdbv1.HiddenSpec{})

		preferHiddenForBackups(psmdb)

		assert.Nil(t, psmdb.Spec.Backup.Configuration.BackupOptions.Priority)
	})
}

func TestHiddenResourcesFallBackPerField(t *testing.T) {
	tests := []struct {
		name       string
		resources  *corev1.ResourceRequirements
		wantCPU    string
		wantMemory string
	}{
		{
			name: "only the memory limit set",
			resources: &corev1.ResourceRequirements{
				Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("8Gi")},
			},
			wantCPU:    "1",
			wantMemory: "8Gi",
		},
		{
			name:       "empty limits left by a form",
			resources:  &corev1.ResourceRequirements{Limits: corev1.ResourceList{}},
			wantCPU:    "1",
			wantMemory: "4Gi",
		},
		{
			name:       "empty resources",
			resources:  &corev1.ResourceRequirements{},
			wantCPU:    "1",
			wantMemory: "4Gi",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := hiddenContext(t, "replicaSet", &corev1alpha1.ComponentSpec{
				Replicas:  pointer.ToInt32(2),
				Resources: tt.resources,
			})

			replsets, err := configureReplsets(c)

			require.NoError(t, err)
			limits := replsets[0].Hidden.Resources.Limits
			assert.True(t, limits.Cpu().Equal(resource.MustParse(tt.wantCPU)), "cpu %s", limits.Cpu())
			assert.True(t, limits.Memory().Equal(resource.MustParse(tt.wantMemory)), "memory %s", limits.Memory())
		})
	}
}

func TestTurningHiddenOnWaitsForOldVolumes(t *testing.T) {
	turnedOff := liveWithHidden(pointer.ToString("old-class"))
	turnedOff.Spec.Replsets[0].Hidden.Enabled = false
	hiddenOn := &corev1alpha1.ComponentSpec{
		Replicas: pointer.ToInt32(2),
		Storage:  &corev1alpha1.Storage{StorageClass: pointer.ToString("new-class")},
	}
	oldVolume := func() *corev1.PersistentVolumeClaim {
		return hiddenPVC("mongod-data-my-mongo-rs0-hidden-0", naming.ComponentHidden)
	}

	t.Run("an old volume without its pod is deleted and the start waits for it", func(t *testing.T) {
		c := hiddenContext(t, "replicaSet", hiddenOn, turnedOff, oldVolume())

		replsets, err := configureReplsets(c)

		require.NoError(t, err)
		assert.False(t, replsets[0].Hidden.Enabled)
		assert.Empty(t, remainingPVCs(t, c))
		psmdb := &psmdbv1.PerconaServerMongoDB{Spec: psmdbv1.PerconaServerMongoDBSpec{Replsets: replsets}}
		assert.True(t, hiddenStartPending(c, psmdb))

		replsets, err = configureReplsets(c)

		require.NoError(t, err)
		assert.True(t, replsets[0].Hidden.Enabled, "starts once the old volume is gone")
		assert.Equal(t, pointer.ToString("new-class"), replsets[0].Hidden.VolumeSpec.PersistentVolumeClaim.StorageClassName)
		psmdb = &psmdbv1.PerconaServerMongoDB{Spec: psmdbv1.PerconaServerMongoDBSpec{Replsets: replsets}}
		assert.False(t, hiddenStartPending(c, psmdb))
	})

	t.Run("an old volume still used by a terminating pod is kept and the start waits", func(t *testing.T) {
		c := hiddenContext(t, "replicaSet", hiddenOn, turnedOff, oldVolume(), hiddenPod("my-mongo-rs0-hidden-0"))

		replsets, err := configureReplsets(c)

		require.NoError(t, err)
		assert.False(t, replsets[0].Hidden.Enabled)
		assert.Equal(t, []string{"mongod-data-my-mongo-rs0-hidden-0"}, remainingPVCs(t, c))
	})

	t.Run("running hidden members keep their volumes", func(t *testing.T) {
		c := hiddenContext(t, "replicaSet", hiddenOn, liveWithHidden(pointer.ToString("old-class")),
			oldVolume(), hiddenPod("my-mongo-rs0-hidden-0"))

		replsets, err := configureReplsets(c)

		require.NoError(t, err)
		assert.True(t, replsets[0].Hidden.Enabled)
		assert.Equal(t, []string{"mongod-data-my-mongo-rs0-hidden-0"}, remainingPVCs(t, c))
	})
}
