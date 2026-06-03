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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
)

func newTestContext(t *testing.T, instance *corev1alpha1.Instance) *controller.Context {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1alpha1.AddToScheme(scheme))
	require.NoError(t, backupv1alpha1.AddToScheme(scheme))
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance).Build()
	return controller.NewContext(context.Background(), fakeClient, instance, "psmdb")
}

func TestValidateMetadata(t *testing.T) {
	tests := []struct {
		name      string
		instance  *corev1alpha1.Instance
		expectErr string
	}{
		{
			name: "valid name",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-cluster"},
			},
		},
		{
			name: "empty name",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: ""},
			},
			expectErr: "name cannot be empty",
		},
		{
			name: "name too long",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name: "this-is-a-very-long-cluster-name-that-exceeds-limits",
				},
			},
			expectErr: "exceeds max length",
		},
		{
			name: "name with uppercase",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "MyCluster"},
			},
			expectErr: "must be RFC-1035 compliant",
		},
		{
			name: "name starting with hyphen",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "-invalid"},
			},
			expectErr: "must be RFC-1035 compliant",
		},
		{
			name: "name ending with hyphen",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "invalid-"},
			},
			expectErr: "must be RFC-1035 compliant",
		},
		{
			name: "name with special characters",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster_name"},
			},
			expectErr: "must be RFC-1035 compliant",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestContext(t, tt.instance)
			err := validateMetadata(ctx)
			if tt.expectErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateEngine(t *testing.T) {
	validEngine := corev1alpha1.ComponentSpec{
		Replicas: pointer.ToInt32(3),
		Storage: &corev1alpha1.Storage{
			Size: resource.MustParse("1Gi"),
		},
		Resources: &corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("600m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
		},
	}

	tests := []struct {
		name      string
		instance  *corev1alpha1.Instance
		expectErr string
	}{
		{
			name: "valid engine component",
			instance: &corev1alpha1.Instance{
				Spec: corev1alpha1.InstanceSpec{
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
					},
				},
			},
		},
		{
			name: "missing engine component",
			instance: &corev1alpha1.Instance{
				Spec: corev1alpha1.InstanceSpec{
					Components: map[string]corev1alpha1.ComponentSpec{},
				},
			},
			expectErr: "missing spec.components.engine",
		},
		{
			name: "missing replicas",
			instance: &corev1alpha1.Instance{
				Spec: corev1alpha1.InstanceSpec{
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: {
							Storage:   validEngine.Storage,
							Resources: validEngine.Resources,
						},
					},
				},
			},
			expectErr: "missing spec.components.engine.replicas",
		},
		{
			name: "even number of replicas",
			instance: &corev1alpha1.Instance{
				Spec: corev1alpha1.InstanceSpec{
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: {
							Replicas:  pointer.ToInt32(4),
							Storage:   validEngine.Storage,
							Resources: validEngine.Resources,
						},
					},
				},
			},
			expectErr: "must be a positive odd number",
		},
		{
			name: "zero replicas",
			instance: &corev1alpha1.Instance{
				Spec: corev1alpha1.InstanceSpec{
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: {
							Replicas:  pointer.ToInt32(0),
							Storage:   validEngine.Storage,
							Resources: validEngine.Resources,
						},
					},
				},
			},
			expectErr: "must be a positive odd number",
		},
		{
			name: "missing storage",
			instance: &corev1alpha1.Instance{
				Spec: corev1alpha1.InstanceSpec{
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: {
							Replicas:  validEngine.Replicas,
							Resources: validEngine.Resources,
						},
					},
				},
			},
			expectErr: "missing spec.components.engine.storage",
		},
		{
			name: "storage too small",
			instance: &corev1alpha1.Instance{
				Spec: corev1alpha1.InstanceSpec{
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: {
							Replicas: validEngine.Replicas,
							Storage: &corev1alpha1.Storage{
								Size: resource.MustParse("500Mi"),
							},
							Resources: validEngine.Resources,
						},
					},
				},
			},
			expectErr: "must be >= 1Gi",
		},
		{
			name: "missing resources",
			instance: &corev1alpha1.Instance{
				Spec: corev1alpha1.InstanceSpec{
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: {
							Replicas: validEngine.Replicas,
							Storage:  validEngine.Storage,
						},
					},
				},
			},
			expectErr: "missing spec.components.engine.resources.limits",
		},
		{
			name: "CPU too small",
			instance: &corev1alpha1.Instance{
				Spec: corev1alpha1.InstanceSpec{
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: {
							Replicas: validEngine.Replicas,
							Storage:  validEngine.Storage,
							Resources: &corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("500m"),
									corev1.ResourceMemory: resource.MustParse("512Mi"),
								},
							},
						},
					},
				},
			},
			expectErr: "cpu must be >= 600m",
		},
		{
			name: "memory too small",
			instance: &corev1alpha1.Instance{
				Spec: corev1alpha1.InstanceSpec{
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: {
							Replicas: validEngine.Replicas,
							Storage:  validEngine.Storage,
							Resources: &corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("600m"),
									corev1.ResourceMemory: resource.MustParse("256Mi"),
								},
							},
						},
					},
				},
			},
			expectErr: "memory must be >= 512Mi",
		},
		{
			name: "missing cpu",
			instance: &corev1alpha1.Instance{
				Spec: corev1alpha1.InstanceSpec{
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: {
							Replicas: validEngine.Replicas,
							Storage:  validEngine.Storage,
							Resources: &corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceMemory: resource.MustParse("512Mi"),
								},
							},
						},
					},
				},
			},
			expectErr: "cpu must be >= 600m",
		},
		{
			name: "missing memory",
			instance: &corev1alpha1.Instance{
				Spec: corev1alpha1.InstanceSpec{
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: {
							Replicas: validEngine.Replicas,
							Storage:  validEngine.Storage,
							Resources: &corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("600m"),
								},
							},
						},
					},
				},
			},
			expectErr: "memory must be >= 512Mi",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestContext(t, tt.instance)
			err := validateEngine(ctx)
			if tt.expectErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateDataSource(t *testing.T) {
	tests := []struct {
		name       string
		dataSource *backupv1alpha1.DataSource
		expectErr  string
	}{
		{
			name:       "unset dataSource",
			dataSource: nil,
		},
		{
			name: "valid backup dataSource",
			dataSource: &backupv1alpha1.DataSource{
				Type: backupv1alpha1.DataSourceTypeBackup,
				Backup: &backupv1alpha1.DataSourceBackup{
					BackupName: "my-backup",
				},
			},
		},
		{
			name: "missing backup details",
			dataSource: &backupv1alpha1.DataSource{
				Type: backupv1alpha1.DataSourceTypeBackup,
			},
			expectErr: "missing spec.dataSource.backup",
		},
		{
			name: "missing backup name",
			dataSource: &backupv1alpha1.DataSource{
				Type:   backupv1alpha1.DataSourceTypeBackup,
				Backup: &backupv1alpha1.DataSourceBackup{},
			},
			expectErr: "missing spec.dataSource.backup.backupName",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDataSource(tt.dataSource)
			if tt.expectErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateConfigServer(t *testing.T) {
	validEngine := corev1alpha1.ComponentSpec{
		Replicas: pointer.ToInt32(3),
		Storage: &corev1alpha1.Storage{
			Size: resource.MustParse("1Gi"),
		},
		Resources: &corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("600m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
		},
	}

	tests := []struct {
		name      string
		instance  *corev1alpha1.Instance
		expectErr string
	}{
		{
			name: "valid configServer with single engine replica",
			instance: &corev1alpha1.Instance{
				Spec: corev1alpha1.InstanceSpec{
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: {
							Replicas:  pointer.ToInt32(1),
							Storage:   validEngine.Storage,
							Resources: validEngine.Resources,
						},
						common.ComponentConfigServer: {
							Replicas: pointer.ToInt32(1),
						},
					},
				},
			},
		},
		{
			name: "valid configServer with multiple engine replicas",
			instance: &corev1alpha1.Instance{
				Spec: corev1alpha1.InstanceSpec{
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
						common.ComponentConfigServer: {
							Replicas: pointer.ToInt32(3),
						},
					},
				},
			},
		},
		{
			name: "missing configServer component",
			instance: &corev1alpha1.Instance{
				Spec: corev1alpha1.InstanceSpec{
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
					},
				},
			},
			expectErr: "missing spec.components.configServer",
		},
		{
			name: "missing configServer replicas",
			instance: &corev1alpha1.Instance{
				Spec: corev1alpha1.InstanceSpec{
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine:       validEngine,
						common.ComponentConfigServer: {},
					},
				},
			},
			expectErr: "missing spec.components.configServer.replicas",
		},
		{
			name: "even number of configServer replicas",
			instance: &corev1alpha1.Instance{
				Spec: corev1alpha1.InstanceSpec{
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
						common.ComponentConfigServer: {
							Replicas: pointer.ToInt32(4),
						},
					},
				},
			},
			expectErr: "must be a positive odd number",
		},
		{
			name: "configServer replicas less than 3 for multi-node",
			instance: &corev1alpha1.Instance{
				Spec: corev1alpha1.InstanceSpec{
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
						common.ComponentConfigServer: {
							Replicas: pointer.ToInt32(1),
						},
					},
				},
			},
			expectErr: "must be >= 3 for multi-node",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestContext(t, tt.instance)
			err := validateConfigServer(ctx)
			if tt.expectErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateShardedTopology(t *testing.T) {
	validEngine := corev1alpha1.ComponentSpec{
		Replicas: pointer.ToInt32(3),
		Storage: &corev1alpha1.Storage{
			Size: resource.MustParse("1Gi"),
		},
		Resources: &corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("600m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
		},
	}

	tests := []struct {
		name      string
		instance  *corev1alpha1.Instance
		expectErr string
	}{
		{
			name: "valid sharded topology",
			instance: &corev1alpha1.Instance{
				Spec: corev1alpha1.InstanceSpec{
					Topology: &corev1alpha1.TopologySpec{Type: "sharded"},
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
						common.ComponentConfigServer: {
							Replicas: pointer.ToInt32(3),
						},
						common.ComponentProxy: {
							Replicas: pointer.ToInt32(2),
						},
					},
				},
			},
		},
		{
			name: "missing proxy component",
			instance: &corev1alpha1.Instance{
				Spec: corev1alpha1.InstanceSpec{
					Topology: &corev1alpha1.TopologySpec{Type: "sharded"},
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
						common.ComponentConfigServer: {
							Replicas: pointer.ToInt32(3),
						},
					},
				},
			},
			expectErr: "component \"proxy\" required for sharded topology",
		},
		{
			name: "missing configServer component",
			instance: &corev1alpha1.Instance{
				Spec: corev1alpha1.InstanceSpec{
					Topology: &corev1alpha1.TopologySpec{Type: "sharded"},
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
						common.ComponentProxy: {
							Replicas: pointer.ToInt32(2),
						},
					},
				},
			},
			expectErr: "missing spec.components.configServer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestContext(t, tt.instance)
			err := validateShardedTopology(ctx)
			if tt.expectErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateReplicaSetTopology(t *testing.T) {
	validEngine := corev1alpha1.ComponentSpec{
		Replicas: pointer.ToInt32(3),
		Storage: &corev1alpha1.Storage{
			Size: resource.MustParse("1Gi"),
		},
		Resources: &corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("600m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
		},
	}

	tests := []struct {
		name      string
		instance  *corev1alpha1.Instance
		expectErr string
	}{
		{
			name: "valid replicaSet topology",
			instance: &corev1alpha1.Instance{
				Spec: corev1alpha1.InstanceSpec{
					Topology: &corev1alpha1.TopologySpec{Type: "replicaSet"},
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
					},
				},
			},
		},
		{
			name: "configServer in replicaSet topology",
			instance: &corev1alpha1.Instance{
				Spec: corev1alpha1.InstanceSpec{
					Topology: &corev1alpha1.TopologySpec{Type: "replicaSet"},
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
						common.ComponentConfigServer: {
							Replicas: pointer.ToInt32(3),
						},
					},
				},
			},
			expectErr: "only valid for sharded topology",
		},
		{
			name: "proxy in replicaSet topology",
			instance: &corev1alpha1.Instance{
				Spec: corev1alpha1.InstanceSpec{
					Topology: &corev1alpha1.TopologySpec{Type: "replicaSet"},
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
						common.ComponentProxy: {
							Replicas: pointer.ToInt32(2),
						},
					},
				},
			},
			expectErr: "only valid for sharded topology",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestContext(t, tt.instance)
			err := validateReplicaSetTopology(ctx)
			if tt.expectErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateComponents(t *testing.T) {
	validEngine := corev1alpha1.ComponentSpec{
		Replicas: pointer.ToInt32(3),
		Storage: &corev1alpha1.Storage{
			Size: resource.MustParse("1Gi"),
		},
		Resources: &corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("600m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
		},
	}

	tests := []struct {
		name      string
		instance  *corev1alpha1.Instance
		expectErr string
	}{
		{
			name: "valid replicaSet components",
			instance: &corev1alpha1.Instance{
				Spec: corev1alpha1.InstanceSpec{
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
					},
				},
			},
		},
		{
			name: "valid sharded components",
			instance: &corev1alpha1.Instance{
				Spec: corev1alpha1.InstanceSpec{
					Topology: &corev1alpha1.TopologySpec{Type: "sharded"},
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
						common.ComponentConfigServer: {
							Replicas: pointer.ToInt32(3),
						},
						common.ComponentProxy: {
							Replicas: pointer.ToInt32(2),
						},
					},
				},
			},
		},
		{
			name: "invalid engine resources",
			instance: &corev1alpha1.Instance{
				Spec: corev1alpha1.InstanceSpec{
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: {
							Replicas: validEngine.Replicas,
							Storage:  validEngine.Storage,
							Resources: &corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("512Mi"),
								},
							},
						},
					},
				},
			},
			expectErr: "engine validation failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestContext(t, tt.instance)
			err := validateComponents(ctx)
			if tt.expectErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidatePSMDB(t *testing.T) {
	validEngine := corev1alpha1.ComponentSpec{
		Replicas: pointer.ToInt32(3),
		Storage: &corev1alpha1.Storage{
			Size: resource.MustParse("1Gi"),
		},
		Resources: &corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("600m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
		},
	}

	tests := []struct {
		name      string
		instance  *corev1alpha1.Instance
		expectErr string
	}{
		{
			name: "valid instance",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Spec: corev1alpha1.InstanceSpec{
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
					},
				},
			},
		},
		{
			name: "invalid metadata",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "Test_Cluster",
					Namespace: "default",
				},
				Spec: corev1alpha1.InstanceSpec{
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
					},
				},
			},
			expectErr: "metadata validation failed",
		},
		{
			name: "invalid dataSource",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Spec: corev1alpha1.InstanceSpec{
					DataSource: &backupv1alpha1.DataSource{
						Type: backupv1alpha1.DataSourceTypeBackup,
					},
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
					},
				},
			},
			expectErr: "dataSource validation failed",
		},
		{
			name: "invalid components",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Spec: corev1alpha1.InstanceSpec{
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: {
							Replicas:  pointer.ToInt32(2), // even number - invalid
							Storage:   validEngine.Storage,
							Resources: validEngine.Resources,
						},
					},
				},
			},
			expectErr: "components validation failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestContext(t, tt.instance)
			err := ValidatePSMDB(ctx)
			if tt.expectErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
