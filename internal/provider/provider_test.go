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
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	openeverestcommon "github.com/openeverest/openeverest/v2/pkg/common"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
)

const userSecretDefinition = "database-credentials"

func userSecret(name string, data map[string]string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{openeverestcommon.OpenEverestDefinitionLabel: userSecretDefinition},
		},
		StringData: data,
	}
}

func backupWithOrigin(name string, originType backupv1alpha1.BackupOriginType) *backupv1alpha1.Backup {
	origin := backupv1alpha1.BackupOrigin{Type: originType}
	switch originType {
	case backupv1alpha1.BackupOriginTypeInstance:
		origin.InstanceRef = &commonv1alpha1.ObjectRef{Name: "source-instance"}
	case backupv1alpha1.BackupOriginTypeExternal:
		origin.External = &backupv1alpha1.BackupOriginExternal{Path: "some/path"}
	}
	return &backupv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       backupv1alpha1.BackupSpec{Origin: origin},
	}
}

func dataSourceBackup(backupName string) *backupv1alpha1.DataSource {
	return &backupv1alpha1.DataSource{
		Type: backupv1alpha1.DataSourceTypeBackup,
		Backup: &backupv1alpha1.DataSourceBackup{
			BackupRef: commonv1alpha1.ObjectRef{Name: backupName},
		},
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

	validSpec := corev1alpha1.InstanceSpec{
		Components: map[string]corev1alpha1.ComponentSpec{
			common.ComponentEngine: validEngine,
		},
	}

	userSecretData := map[string]string{"username": "admin", "password": "secret"}

	tests := []struct {
		name      string
		instance  *corev1alpha1.Instance
		backup    *backupv1alpha1.Backup
		secret    *corev1.Secret
		expectErr string
	}{
		{
			name: "empty name",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: ""},
				Spec:       validSpec,
			},
			expectErr: "name cannot be empty",
		},
		{
			name: "name too long",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name: "this-is-a-very-long-cluster-name-that-exceeds-limits",
				},
				Spec: validSpec,
			},
			expectErr: "exceeds max length",
		},
		{
			name: "name with uppercase",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "MyCluster"},
				Spec:       validSpec,
			},
			expectErr: "must be RFC-1035 compliant",
		},
		{
			name: "name starting with hyphen",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "-invalid"},
				Spec:       validSpec,
			},
			expectErr: "must be RFC-1035 compliant",
		},
		{
			name: "name ending with hyphen",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "invalid-"},
				Spec:       validSpec,
			},
			expectErr: "must be RFC-1035 compliant",
		},
		{
			name: "name with special characters",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster_name"},
				Spec:       validSpec,
			},
			expectErr: "must be RFC-1035 compliant",
		},
		{
			name: "dataSource unset",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
				Spec: corev1alpha1.InstanceSpec{
					DataSource: nil,
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
					},
				},
			},
		},
		{
			name: "valid dataSource",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
				Spec: corev1alpha1.InstanceSpec{
					DataSource: &backupv1alpha1.DataSource{
						Type: backupv1alpha1.DataSourceTypeBackup,
						Backup: &backupv1alpha1.DataSourceBackup{
							BackupRef: commonv1alpha1.ObjectRef{Name: "my-backup"},
						},
					},
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
					},
				},
			},
			backup: backupWithOrigin("my-backup", backupv1alpha1.BackupOriginTypeInstance),
		},
		{
			name: "dataSource missing backup details",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
				Spec: corev1alpha1.InstanceSpec{
					DataSource: &backupv1alpha1.DataSource{
						Type: backupv1alpha1.DataSourceTypeBackup,
					},
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
					},
				},
			},
			expectErr: "missing spec.dataSource.backup",
		},
		{
			name: "dataSource missing backup name",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
				Spec: corev1alpha1.InstanceSpec{
					DataSource: &backupv1alpha1.DataSource{
						Type:   backupv1alpha1.DataSourceTypeBackup,
						Backup: &backupv1alpha1.DataSourceBackup{},
					},
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
					},
				},
			},
			expectErr: "missing spec.dataSource.backup.backupRef.name",
		},
		{
			name: "missing engine component",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
				Spec: corev1alpha1.InstanceSpec{
					Components: map[string]corev1alpha1.ComponentSpec{},
				},
			},
			expectErr: "missing spec.components.engine",
		},
		{
			name: "engine missing replicas",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
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
			name: "engine even number of replicas",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
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
			name: "engine zero replicas",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
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
			name: "engine missing storage",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
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
			name: "engine storage too small",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
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
			name: "engine missing resources",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
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
			name: "engine CPU too small",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
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
			name: "engine memory too small",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
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
			name: "engine missing cpu",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
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
			name: "engine missing memory",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
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
		{
			name: "engine unsupported service type",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
				Spec: corev1alpha1.InstanceSpec{
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: {
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
							Service: &corev1alpha1.Service{
								ServiceType: corev1.ServiceTypeExternalName,
							},
						},
					},
				},
			},
			expectErr: "spec.components.engine.service.serviceType must be one of ClusterIP, LoadBalancer or NodePort",
		},
		{
			name: "valid sharded topology",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
				Spec: corev1alpha1.InstanceSpec{
					Topology: &corev1alpha1.TopologySpec{Type: "sharded"},
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
						common.ComponentConfigServer: {
							Replicas: pointer.ToInt32(3),
						},
						common.ComponentProxy: {Replicas: pointer.ToInt32(2)},
					},
				},
			},
		},
		{
			name: "sharded missing proxy component",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
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
			name: "sharded proxy unsupported service type",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
				Spec: corev1alpha1.InstanceSpec{
					Topology: &corev1alpha1.TopologySpec{Type: "sharded"},
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
						common.ComponentConfigServer: {
							Replicas: pointer.ToInt32(3),
						},
						common.ComponentProxy: {
							Replicas: pointer.ToInt32(2),
							Service: &corev1alpha1.Service{
								ServiceType: corev1.ServiceTypeExternalName,
							},
						},
					},
				},
			},
			expectErr: "spec.components.proxy.service.serviceType must be one of ClusterIP, LoadBalancer or NodePort",
		},
		{
			name: "sharded missing configServer component",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
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
		{
			name: "valid configServer with single engine replica",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
				Spec: corev1alpha1.InstanceSpec{
					Topology: &corev1alpha1.TopologySpec{Type: "sharded"},
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: {
							Replicas:  pointer.ToInt32(1),
							Storage:   validEngine.Storage,
							Resources: validEngine.Resources,
						},
						common.ComponentConfigServer: {Replicas: pointer.ToInt32(1)},
						common.ComponentProxy:        {Replicas: pointer.ToInt32(2)},
					},
				},
			},
		},
		{
			name: "sharded valid configServer with multiple engine replicas",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
				Spec: corev1alpha1.InstanceSpec{
					Topology: &corev1alpha1.TopologySpec{Type: "sharded"},
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
						common.ComponentConfigServer: {
							Replicas: pointer.ToInt32(3),
						},
						common.ComponentProxy: {Replicas: pointer.ToInt32(2)},
					},
				},
			},
		},
		{
			name: "sharded missing configServer",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
				Spec: corev1alpha1.InstanceSpec{
					Topology: &corev1alpha1.TopologySpec{Type: "sharded"},
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
						common.ComponentProxy:  {Replicas: pointer.ToInt32(2)},
					},
				},
			},
			expectErr: "missing spec.components.configServer",
		},
		{
			name: "sharded missing configServer replicas",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
				Spec: corev1alpha1.InstanceSpec{
					Topology: &corev1alpha1.TopologySpec{Type: "sharded"},
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine:       validEngine,
						common.ComponentConfigServer: {},
						common.ComponentProxy:        {Replicas: pointer.ToInt32(2)},
					},
				},
			},
			expectErr: "missing spec.components.configServer.replicas",
		},
		{
			name: "sharded even number of configServer replicas",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
				Spec: corev1alpha1.InstanceSpec{
					Topology: &corev1alpha1.TopologySpec{Type: "sharded"},
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
						common.ComponentConfigServer: {
							Replicas: pointer.ToInt32(4),
						},
						common.ComponentProxy: {Replicas: pointer.ToInt32(2)},
					},
				},
			},
			expectErr: "must be a positive odd number",
		},
		{
			name: "sharded configServer replicas less than 3 for multi-node",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
				Spec: corev1alpha1.InstanceSpec{
					Topology: &corev1alpha1.TopologySpec{Type: "sharded"},
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
						common.ComponentConfigServer: {
							Replicas: pointer.ToInt32(1),
						},
						common.ComponentProxy: {Replicas: pointer.ToInt32(2)},
					},
				},
			},
			expectErr: "must be >= 3 for multi-node",
		},
		{
			name: "no dataSource with valid userSecretRef",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
				Spec: corev1alpha1.InstanceSpec{
					UserSecretRef: &commonv1alpha1.SecretRef{Name: "user-secret"},
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
					},
				},
			},
			secret: userSecret("user-secret", userSecretData),
		},
		{
			name: "userSecretRef missing secret",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
				Spec: corev1alpha1.InstanceSpec{
					UserSecretRef: &commonv1alpha1.SecretRef{Name: "user-secret"},
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
					},
				},
			},
			expectErr: "failed to get user secret",
		},
		{
			name: "userSecret schema validation fails",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
				Spec: corev1alpha1.InstanceSpec{
					UserSecretRef: &commonv1alpha1.SecretRef{Name: "user-secret"},
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
					},
				},
			},
			secret:    userSecret("user-secret", map[string]string{"username": "admin"}),
			expectErr: "validation failed",
		},
		{
			name: "pointInTime with userSecretRef",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
				Spec: corev1alpha1.InstanceSpec{
					DataSource:    &backupv1alpha1.DataSource{Type: backupv1alpha1.DataSourceTypePointInTime},
					UserSecretRef: &commonv1alpha1.SecretRef{Name: "user-secret"},
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
					},
				},
			},
			secret:    userSecret("user-secret", userSecretData),
			expectErr: "must not be set when seeding from an Instance",
		},
		{
			name: "instance backup forbids userSecretRef",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
				Spec: corev1alpha1.InstanceSpec{
					DataSource:    dataSourceBackup("live-backup"),
					UserSecretRef: &commonv1alpha1.SecretRef{Name: "user-secret"},
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
					},
				},
			},
			backup:    backupWithOrigin("live-backup", backupv1alpha1.BackupOriginTypeInstance),
			secret:    userSecret("user-secret", userSecretData),
			expectErr: "must not be set when seeding from an Instance",
		},
		{
			name: "instance backup without userSecretRef",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
				Spec: corev1alpha1.InstanceSpec{
					DataSource: dataSourceBackup("live-backup"),
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
					},
				},
			},
			backup: backupWithOrigin("live-backup", backupv1alpha1.BackupOriginTypeInstance),
		},
		{
			name: "external backup without userSecretRef",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
				Spec: corev1alpha1.InstanceSpec{
					DataSource: dataSourceBackup("imported-backup"),
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
					},
				},
			},
			backup:    backupWithOrigin("imported-backup", backupv1alpha1.BackupOriginTypeExternal),
			expectErr: "required when seeding from an external backup",
		},
		{
			name: "external backup with userSecretRef",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
				Spec: corev1alpha1.InstanceSpec{
					DataSource:    dataSourceBackup("imported-backup"),
					UserSecretRef: &commonv1alpha1.SecretRef{Name: "user-secret"},
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
					},
				},
			},
			backup: backupWithOrigin("imported-backup", backupv1alpha1.BackupOriginTypeExternal),
			secret: userSecret("user-secret", userSecretData),
		},
		{
			name: "backup not found",
			instance: &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
				Spec: corev1alpha1.InstanceSpec{
					DataSource:    dataSourceBackup("missing-backup"),
					UserSecretRef: &commonv1alpha1.SecretRef{Name: "user-secret"},
					Components: map[string]corev1alpha1.ComponentSpec{
						common.ComponentEngine: validEngine,
					},
				},
			},
			secret:    userSecret("user-secret", userSecretData),
			expectErr: `"missing-backup" not found`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, corev1.AddToScheme(scheme))
			require.NoError(t, corev1alpha1.AddToScheme(scheme))
			require.NoError(t, backupv1alpha1.AddToScheme(scheme))

			provider := &corev1alpha1.Provider{
				ObjectMeta: metav1.ObjectMeta{Name: "psmdb"},
				Spec: corev1alpha1.ProviderSpec{
					Secrets: map[string]corev1alpha1.SecretDefinition{
						userSecretDefinition: {
							ParametersSchema: &commonv1alpha1.ParametersSchema{
								OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
									Type: "object",
									Properties: map[string]apiextensionsv1.JSONSchemaProps{
										"username": {Type: "string"},
										"password": {Type: "string"},
									},
									Required: []string{"username", "password"},
								},
							},
						},
					},
				},
			}

			objects := []client.Object{provider, tt.instance}

			if tt.backup != nil {
				objects = append(objects, tt.backup)
			}

			if tt.secret != nil {
				objects = append(objects, tt.secret)
			}

			fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			ctx := controller.NewContext(context.Background(), fakeClient, tt.instance, "psmdb")
			err := ValidatePSMDB(ctx)

			if tt.expectErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectErr)

				return
			}

			require.NoError(t, err)
		})
	}
}
