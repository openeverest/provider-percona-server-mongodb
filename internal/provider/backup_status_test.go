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
	"time"

	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
)

var pitrBase = time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

// mkOpBackup builds a ready operator backup completed at base+offset.
// restorable sets status.latestRestorableTime to one hour past completion.
func mkOpBackup(name string, offset time.Duration, restorable bool) psmdbv1.PerconaServerMongoDBBackup {
	completed := metav1.NewTime(pitrBase.Add(offset))
	b := psmdbv1.PerconaServerMongoDBBackup{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "everest"},
		Spec: psmdbv1.PerconaServerMongoDBBackupSpec{
			ClusterName: "mongo-prod",
			StorageName: "minio-primary",
		},
		Status: psmdbv1.PerconaServerMongoDBBackupStatus{
			State:       psmdbv1.BackupStateReady,
			CompletedAt: &completed,
		},
	}
	if restorable {
		t := metav1.NewTime(pitrBase.Add(offset + time.Hour))
		b.Status.LatestRestorableTime = &t
	}
	return b
}

func TestPITRWindow_NoBackups(t *testing.T) {
	t.Parallel()

	got := pitrWindow(nil)
	require.NotNil(t, got)
	assert.Equal(t, corev1alpha1.PITRStateUnavailable, got.State)
	assert.Equal(t, pitrReasonNoBackups, got.Reason)
	assert.Nil(t, got.EarliestRestorableTime)
}

func TestPITRWindow_SpansAllBackups(t *testing.T) {
	t.Parallel()

	got := pitrWindow([]psmdbv1.PerconaServerMongoDBBackup{
		mkOpBackup("b1", 0, true),
		mkOpBackup("b2", time.Hour, true),
		mkOpBackup("b3", 2*time.Hour, true),
	})

	require.NotNil(t, got)
	assert.Equal(t, corev1alpha1.PITRStateAvailable, got.State)
	// Earliest is the oldest backup's completion; latest comes from the newest.
	assert.Equal(t, pitrBase, got.EarliestRestorableTime.Time)
	assert.Equal(t, pitrBase.Add(3*time.Hour), got.LatestRestorableTime.Time)
}

// PBM refreshes latestRestorableTime on the newest backup, so in the interval
// before a brand-new backup has been stamped the maximum across all backups is
// the correct end of the window.
func TestPITRWindow_NewestBackupNotYetStamped(t *testing.T) {
	t.Parallel()

	got := pitrWindow([]psmdbv1.PerconaServerMongoDBBackup{
		mkOpBackup("b1", 0, true),
		mkOpBackup("b2", time.Hour, false),
	})

	require.NotNil(t, got)
	assert.Equal(t, corev1alpha1.PITRStateAvailable, got.State)
	assert.Equal(t, pitrBase.Add(time.Hour), got.LatestRestorableTime.Time)
}

func TestPITRWindow_NoRestorableTimeYet(t *testing.T) {
	t.Parallel()

	got := pitrWindow([]psmdbv1.PerconaServerMongoDBBackup{
		mkOpBackup("b1", 0, false),
	})

	require.NotNil(t, got)
	assert.Equal(t, corev1alpha1.PITRStateUnavailable, got.State)
	assert.Equal(t, pitrReasonNoBackups, got.Reason)
	assert.Nil(t, got.LatestRestorableTime)
}

func TestCollectStorageBackups_FiltersAndSorts(t *testing.T) {
	t.Parallel()

	other := mkOpBackup("other-cluster", 0, true)
	other.Spec.ClusterName = "mongo-staging"

	otherStorage := mkOpBackup("other-storage", 0, true)
	otherStorage.Spec.StorageName = "minio-secondary"

	running := mkOpBackup("running", 0, true)
	running.Status.State = psmdbv1.BackupStateRunning

	// Scheduled backups may carry the storage only on status.storageName.
	statusStorage := mkOpBackup("status-storage", 2*time.Hour, true)
	statusStorage.Spec.StorageName = ""
	statusStorage.Status.StorageName = "minio-primary"

	got := collectStorageBackups([]psmdbv1.PerconaServerMongoDBBackup{
		mkOpBackup("newer", time.Hour, true),
		other,
		otherStorage,
		running,
		statusStorage,
		mkOpBackup("older", 0, true),
	}, "mongo-prod", "minio-primary")

	require.Len(t, got, 3)
	assert.Equal(t, "older", got[0].Name, "results must be oldest first")
	assert.Equal(t, "newer", got[1].Name)
	assert.Equal(t, "status-storage", got[2].Name)
}
