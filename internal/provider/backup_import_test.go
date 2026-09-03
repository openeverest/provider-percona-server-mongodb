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
	tests := []struct {
		name         string
		key          string
		data         string
		wantErr      bool
		wantPath     string
		wantStartTS  int64
		wantEndTSGTE int64
	}{
		{
			name:         "done logical backup is discovered",
			key:          "2026-09-01T05:55:39Z.pbm.json",
			data:         `{"name":"2026-09-01T05:55:39Z","type":"logical","status":"done","start_ts":1756705000,"last_transition_ts":1756705060}`,
			wantPath:     "2026-09-01T05:55:39Z",
			wantStartTS:  1756705000,
			wantEndTSGTE: 1756705060,
		},
		{
			name:     "done physical backup is discovered",
			key:      "phys.pbm.json",
			data:     `{"name":"phys","type":"physical","status":"done","start_ts":100,"last_transition_ts":200}`,
			wantPath: "phys",
		},
		{
			name:     "path preserves bucket-relative prefix from the key",
			key:      "team-a/backups/2026-09-01T05:55:39Z.pbm.json",
			data:     `{"name":"2026-09-01T05:55:39Z","type":"logical","status":"done","start_ts":100,"last_transition_ts":200}`,
			wantPath: "team-a/backups/2026-09-01T05:55:39Z",
		},
		{
			name: "failed backup is skipped",
			key:  "bad.pbm.json",
			data: `{"name":"bad","type":"logical","status":"error","start_ts":100,"last_transition_ts":200}`,
		},
		{
			name: "running backup is skipped",
			key:  "run.pbm.json",
			data: `{"name":"run","type":"logical","status":"running","start_ts":100}`,
		},
		{
			name:    "malformed json errors",
			key:     "junk.pbm.json",
			data:    `{not json`,
			wantErr: true,
		},
	}

	imp := testImport()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backup, err := parsePBM(imp, tt.key, []byte(tt.data))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if backup == nil {
				return
			}
			require.NotNil(t, backup)
			ext := backup.Spec.Origin.External
			require.NotNil(t, ext)
			assert.Equal(t, backupv1alpha1.BackupOriginTypeExternal, backup.Spec.Origin.Type)
			assert.Equal(t, tt.wantPath, ext.Path)
			assert.Equal(t, imp.Spec.ClassRef, backup.Spec.ClassRef)
			assert.Equal(t, imp.Spec.StorageRef, backup.Spec.StorageRef)
			assert.Equal(t, backupv1alpha1.BackupDeletionPolicyRetain, backup.Spec.DeletionPolicy)
			assert.Equal(t, imp.Namespace, backup.Namespace)
			assert.NotEmpty(t, backup.Name)
			assert.False(t, ext.CompletedAt.Time.Before(ext.StartedAt.Time),
				"completedAt must not precede startedAt")
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
