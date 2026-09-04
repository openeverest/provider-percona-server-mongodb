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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
)

func testImport() *backupv1alpha1.BackupImport {
	return &backupv1alpha1.BackupImport{
		ObjectMeta: metav1.ObjectMeta{Name: "imp", Namespace: "team-a"},
		Spec: backupv1alpha1.BackupImportSpec{
			ClassRef:   commonv1alpha1.ObjectRef{Name: "percona-backup-mongodb"},
			StorageRef: commonv1alpha1.ObjectRef{Name: "my-s3"},
		},
	}
}

func TestParsePBM(t *testing.T) {
	imp := testImport()

	tests := []struct {
		name    string
		key     string
		data    string
		wantErr bool
		want    *backupv1alpha1.Backup
	}{
		{
			name: "backup is discovered",
			key:  "2026-09-01T05:55:39Z.pbm.json",
			data: `{"name":"2026-09-01T05:55:39Z","type":"logical","status":"done","start_ts":1756705000,"last_transition_ts":1756705060}`,
			want: &backupv1alpha1.Backup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "import-d973d28935d6e0ab",
					Namespace: "team-a",
				},
				Spec: backupv1alpha1.BackupSpec{
					Origin: backupv1alpha1.BackupOrigin{
						Type: backupv1alpha1.BackupOriginTypeExternal,
						External: &backupv1alpha1.BackupOriginExternal{
							Path:        "2026-09-01T05:55:39Z",
							StartedAt:   metav1.Unix(1756705000, 0),
							CompletedAt: metav1.Unix(1756705060, 0),
						},
					},
					ClassRef:       commonv1alpha1.ObjectRef{Name: "percona-backup-mongodb"},
					StorageRef:     commonv1alpha1.ObjectRef{Name: "my-s3"},
					DeletionPolicy: backupv1alpha1.BackupDeletionPolicyRetain,
				},
			},
		},
		{
			name: "path preserves bucket-relative prefix from the key",
			key:  "team-a/backups/2026-09-01T05:55:39Z.pbm.json",
			data: `{"name":"2026-09-01T05:55:39Z","type":"logical","status":"done","start_ts":100,"last_transition_ts":200}`,
			want: &backupv1alpha1.Backup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "import-38407b322bd52c1b",
					Namespace: "team-a",
				},
				Spec: backupv1alpha1.BackupSpec{
					Origin: backupv1alpha1.BackupOrigin{
						Type: backupv1alpha1.BackupOriginTypeExternal,
						External: &backupv1alpha1.BackupOriginExternal{
							Path:        "team-a/backups/2026-09-01T05:55:39Z",
							StartedAt:   metav1.Unix(100, 0),
							CompletedAt: metav1.Unix(200, 0),
						},
					},
					ClassRef:       commonv1alpha1.ObjectRef{Name: "percona-backup-mongodb"},
					StorageRef:     commonv1alpha1.ObjectRef{Name: "my-s3"},
					DeletionPolicy: backupv1alpha1.BackupDeletionPolicyRetain,
				},
			},
		},
		{
			name: "failed backup is skipped",
			key:  "bad.pbm.json",
			data: `{"name":"bad","type":"logical","status":"error","start_ts":100,"last_transition_ts":200}`,
			want: nil,
		},
		{
			name: "running backup is skipped",
			key:  "run.pbm.json",
			data: `{"name":"run","type":"logical","status":"running","start_ts":100}`,
			want: nil,
		},
		{
			name:    "malformed json errors",
			key:     "junk.pbm.json",
			data:    `{not json`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backup, err := parsePBM(imp, tt.key, []byte(tt.data))
			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, backup)
		})
	}
}

func TestParsePBMDeterministicName(t *testing.T) {
	imp := testImport()
	data := []byte(`{"name":"b1","status":"done","start_ts":10,"last_transition_ts":20}`)
	b1, err1 := parsePBM(imp, "b1.pbm.json", data)
	b2, err2 := parsePBM(imp, "b1.pbm.json", data)
	require.NoError(t, err1)
	require.NoError(t, err2)
	assert.Equal(t, b1.Name, b2.Name)
}
