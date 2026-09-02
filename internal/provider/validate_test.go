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
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/pkg/common"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
)

const userSecretDefinition = "database-credentials"

func userSecret(name string, data map[string]string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{common.OpenEverestDefinitionLabel: userSecretDefinition},
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

func TestValidateUserSecret(t *testing.T) {
	validData := map[string]string{"username": "admin", "password": "secret"}

	tests := []struct {
		name          string
		dataSource    *backupv1alpha1.DataSource
		userSecretRef *commonv1alpha1.SecretRef
		secret        *corev1.Secret
		backup        *backupv1alpha1.Backup
		expectErr     string
	}{
		{
			name: "no dataSource and no userSecretRef",
		},
		{
			name:          "no dataSource with valid userSecretRef",
			userSecretRef: &commonv1alpha1.SecretRef{Name: "user-secret"},
			secret:        userSecret("user-secret", validData),
		},
		{
			name:          "missing secret",
			userSecretRef: &commonv1alpha1.SecretRef{Name: "user-secret"},
			expectErr:     "failed to get user secret",
		},
		{
			name:          "schema validation fails",
			userSecretRef: &commonv1alpha1.SecretRef{Name: "user-secret"},
			secret:        userSecret("user-secret", map[string]string{"username": "admin"}),
			expectErr:     "validation failed",
		},
		{
			name:          "pointInTime with userSecretRef",
			dataSource:    &backupv1alpha1.DataSource{Type: backupv1alpha1.DataSourceTypePointInTime},
			userSecretRef: &commonv1alpha1.SecretRef{Name: "user-secret"},
			secret:        userSecret("user-secret", validData),
			expectErr:     "must not be set when seeding from an Instance",
		},
		{
			name:          "instance backup forbids userSecretRef",
			dataSource:    dataSourceBackup("live-backup"),
			userSecretRef: &commonv1alpha1.SecretRef{Name: "user-secret"},
			backup:        backupWithOrigin("live-backup", backupv1alpha1.BackupOriginTypeInstance),
			secret:        userSecret("user-secret", validData),
			expectErr:     "must not be set when seeding from an Instance",
		},
		{
			name:       "instance backup without userSecretRef",
			dataSource: dataSourceBackup("live-backup"),
			backup:     backupWithOrigin("live-backup", backupv1alpha1.BackupOriginTypeInstance),
		},
		{
			name:       "external backup without userSecretRef",
			dataSource: dataSourceBackup("imported-backup"),
			backup:     backupWithOrigin("imported-backup", backupv1alpha1.BackupOriginTypeExternal),
			expectErr:  "required when seeding from an external backup",
		},
		{
			name:          "external backup with userSecretRef",
			dataSource:    dataSourceBackup("imported-backup"),
			userSecretRef: &commonv1alpha1.SecretRef{Name: "user-secret"},
			backup:        backupWithOrigin("imported-backup", backupv1alpha1.BackupOriginTypeExternal),
			secret:        userSecret("user-secret", validData),
		},
		{
			name:          "backup not found",
			dataSource:    dataSourceBackup("missing-backup"),
			userSecretRef: &commonv1alpha1.SecretRef{Name: "user-secret"},
			secret:        userSecret("user-secret", validData),
			expectErr:     `"missing-backup" not found`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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

			instance := &corev1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance"},
				Spec: corev1alpha1.InstanceSpec{
					DataSource:    tt.dataSource,
					UserSecretRef: tt.userSecretRef,
				},
			}

			scheme := runtime.NewScheme()
			require.NoError(t, corev1.AddToScheme(scheme))
			require.NoError(t, corev1alpha1.AddToScheme(scheme))
			require.NoError(t, backupv1alpha1.AddToScheme(scheme))

			objects := []ctrlclient.Object{provider, instance}
			if tt.secret != nil {
				objects = append(objects, tt.secret)
			}
			if tt.backup != nil {
				objects = append(objects, tt.backup)
			}
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			ctx := controller.NewContext(context.Background(), fakeClient, instance, "psmdb")

			err := validateUserSecret(ctx)

			if tt.expectErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectErr)
				return
			}

			require.NoError(t, err)
		})
	}
}
