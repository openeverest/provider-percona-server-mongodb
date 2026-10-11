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
	"os"
	"path/filepath"
	"testing"

	"github.com/AlekSi/pointer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	openeverestcommon "github.com/openeverest/openeverest/v2/pkg/common"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
)

// userSecretDefinition is the secret definition name the provider actually
// ships in charts/provider-percona-server-mongodb/generated/provider-spec.yaml.
const userSecretDefinition = "user"

const providerSpecPath = "../../charts/provider-percona-server-mongodb/generated/provider-spec.yaml"

// loadProviderSpec reads the shipped provider spec so user-secret
// validation exercises the schema real users hit.
func loadProviderSpec(t *testing.T) corev1alpha1.ProviderSpec {
	t.Helper()

	raw, err := os.ReadFile(filepath.Clean(providerSpecPath))
	require.NoError(t, err)

	var providerSpec struct {
		Spec corev1alpha1.ProviderSpec `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &providerSpec))

	return providerSpec.Spec
}

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

func TestValidateReferences(t *testing.T) {
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

	userSecretData := map[string]string{
		"MONGODB_DATABASE_ADMIN_USER":     "admin",
		"MONGODB_DATABASE_ADMIN_PASSWORD": "secret",
	}

	tests := []struct {
		name      string
		instance  *corev1alpha1.Instance
		backup    *backupv1alpha1.Backup
		secret    *corev1.Secret
		expectErr string
	}{
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
			expectErr: "get user secret",
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
			secret:    userSecret("user-secret", map[string]string{"UNKNOWN_KEY": "value"}),
			expectErr: "validation failed",
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
			secret: userSecret("user-secret", map[string]string{
				"MONGODB_BACKUP_USER":              "backup",
				"MONGODB_BACKUP_PASSWORD":          "backup-secret",
				"MONGODB_CLUSTER_ADMIN_USER":       "clusterAdmin",
				"MONGODB_CLUSTER_ADMIN_PASSWORD":   "cluster-admin-secret",
				"MONGODB_CLUSTER_MONITOR_USER":     "clusterMonitor",
				"MONGODB_CLUSTER_MONITOR_PASSWORD": "cluster-monitor-secret",
				"MONGODB_DATABASE_ADMIN_USER":      "databaseAdmin",
				"MONGODB_DATABASE_ADMIN_PASSWORD":  "database-admin-secret",
				"MONGODB_USER_ADMIN_USER":          "userAdmin",
				"MONGODB_USER_ADMIN_PASSWORD":      "user-admin-secret",
			}),
		},
		{
			name: "external backup with userSecretRef missing MONGODB_USER_ADMIN_USER",
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
			secret: userSecret("user-secret", map[string]string{
				"MONGODB_BACKUP_USER":              "backup",
				"MONGODB_BACKUP_PASSWORD":          "backup-secret",
				"MONGODB_CLUSTER_ADMIN_USER":       "clusterAdmin",
				"MONGODB_CLUSTER_ADMIN_PASSWORD":   "cluster-admin-secret",
				"MONGODB_CLUSTER_MONITOR_USER":     "clusterMonitor",
				"MONGODB_CLUSTER_MONITOR_PASSWORD": "cluster-monitor-secret",
				"MONGODB_DATABASE_ADMIN_USER":      "databaseAdmin",
				"MONGODB_DATABASE_ADMIN_PASSWORD":  "database-admin-secret",
				"MONGODB_USER_ADMIN_PASSWORD":      "user-admin-secret",
			}),
			expectErr: `user secret "user-secret" is missing "MONGODB_USER_ADMIN_USER"`,
		},
		{
			name: "external backup with userSecretRef empty MONGODB_USER_ADMIN_USER",
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
			secret: userSecret("user-secret", map[string]string{
				"MONGODB_BACKUP_USER":              "backup",
				"MONGODB_BACKUP_PASSWORD":          "backup-secret",
				"MONGODB_CLUSTER_ADMIN_USER":       "clusterAdmin",
				"MONGODB_CLUSTER_ADMIN_PASSWORD":   "cluster-admin-secret",
				"MONGODB_CLUSTER_MONITOR_USER":     "clusterMonitor",
				"MONGODB_CLUSTER_MONITOR_PASSWORD": "cluster-monitor-secret",
				"MONGODB_DATABASE_ADMIN_USER":      "databaseAdmin",
				"MONGODB_DATABASE_ADMIN_PASSWORD":  "database-admin-secret",
				"MONGODB_USER_ADMIN_USER":          "",
				"MONGODB_USER_ADMIN_PASSWORD":      "user-admin-secret",
			}),
			expectErr: `user secret "user-secret" is missing "MONGODB_USER_ADMIN_USER"`,
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
				Spec:       loadProviderSpec(t),
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
			err := (&PSMDBProvider{}).ValidateReferences(ctx)

			if tt.expectErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectErr)

				return
			}

			require.NoError(t, err)
		})
	}
}
