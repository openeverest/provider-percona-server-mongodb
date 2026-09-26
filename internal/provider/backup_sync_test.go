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
	"time"

	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
)

func backupSyncContext(t *testing.T, objs ...client.Object) *controller.Context {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1alpha1.AddToScheme(scheme))
	require.NoError(t, backupv1alpha1.AddToScheme(scheme))
	require.NoError(t, psmdbv1.SchemeBuilder.AddToScheme(scheme))

	in := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "my-mongo", Namespace: "db"},
	}
	psmdb := &psmdbv1.PerconaServerMongoDB{
		ObjectMeta: metav1.ObjectMeta{Name: "my-mongo", Namespace: "db"},
		Spec: psmdbv1.PerconaServerMongoDBSpec{
			Backup: psmdbv1.BackupSpec{
				Storages: map[string]psmdbv1.BackupStorageSpec{
					"my-storage": {},
				},
			},
		},
	}

	objs = append(objs, in, psmdb)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return controller.NewContext(context.Background(), fakeClient, in, "psmdb")
}

func TestSyncBackup_TimestampsComeFromOperatorStatus(t *testing.T) {
	p := &PSMDBProvider{}
	backup := &backupv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "my-backup", Namespace: "db"},
		Spec:       backupv1alpha1.BackupSpec{StorageRef: commonv1alpha1.ObjectRef{Name: "my-storage"}},
	}

	t.Run("completed backup reports the operator's real start and completion time", func(t *testing.T) {
		// Truncated to second precision: the fake client round-trips objects
		// through the same wire format a real API server would, and
		// metav1.Time only carries second-level resolution over the wire.
		started := metav1.NewTime(metav1.Now().Add(-5 * time.Minute).Truncate(time.Second))
		completed := metav1.NewTime(metav1.Now().Truncate(time.Second))
		psmdbBackup := &psmdbv1.PerconaServerMongoDBBackup{
			ObjectMeta: metav1.ObjectMeta{Name: "my-backup", Namespace: "db"},
			Status: psmdbv1.PerconaServerMongoDBBackupStatus{
				State:       psmdbv1.BackupStateReady,
				StartAt:     &started,
				CompletedAt: &completed,
			},
		}
		c := backupSyncContext(t, psmdbBackup)

		exec, err := p.SyncBackup(c, backup)

		require.NoError(t, err)
		assert.Equal(t, backupv1alpha1.BackupStateSucceeded, exec.State)
		require.NotNil(t, exec.StartedAt)
		assert.Equal(t, started, *exec.StartedAt)
		require.NotNil(t, exec.CompletedAt)
		assert.Equal(t, completed, *exec.CompletedAt)
	})

	t.Run("running backup has a start time but no fabricated completion time", func(t *testing.T) {
		started := metav1.NewTime(metav1.Now().Add(-1 * time.Minute).Truncate(time.Second))
		psmdbBackup := &psmdbv1.PerconaServerMongoDBBackup{
			ObjectMeta: metav1.ObjectMeta{Name: "my-backup", Namespace: "db"},
			Status: psmdbv1.PerconaServerMongoDBBackupStatus{
				State:   psmdbv1.BackupStateRunning,
				StartAt: &started,
			},
		}
		c := backupSyncContext(t, psmdbBackup)

		exec, err := p.SyncBackup(c, backup)

		require.NoError(t, err)
		assert.Equal(t, backupv1alpha1.BackupStateRunning, exec.State)
		require.NotNil(t, exec.StartedAt)
		assert.Equal(t, started, *exec.StartedAt)
		assert.Nil(t, exec.CompletedAt)
	})
}
