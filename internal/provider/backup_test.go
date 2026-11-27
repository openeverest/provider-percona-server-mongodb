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
	"encoding/json"
	"testing"
	"time"

	"github.com/AlekSi/pointer"
	"github.com/percona/percona-backup-mongodb/pbm/compress"
	"github.com/percona/percona-backup-mongodb/pbm/defs"
	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	"github.com/percona/percona-server-mongodb-operator/pkg/util/numstr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/backupstorageclasses"
	"github.com/openeverest/openeverest/v2/backupstorageclasses/s3"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mongodb/definition/backupclasses"
	perconabackupmongodb "github.com/openeverest/provider-percona-server-mongodb/definition/backupclasses/percona-backup-mongodb"
	"github.com/openeverest/provider-percona-server-mongodb/definition/components"
	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
)

func TestBackupAgentImage(t *testing.T) {
	t.Parallel()
	spec := &corev1alpha1.ProviderSpec{
		Components: map[string]corev1alpha1.Component{common.ComponentBackupAgent: {Type: "backup"}},
		ComponentTypes: map[string]corev1alpha1.ComponentType{"backup": {
			DefaultVersion: "new",
			Versions:       []corev1alpha1.ComponentVersion{{Version: "old", Image: "pbm:old"}, {Version: "new", Image: "pbm:new"}},
		}},
		DefaultVersion: "latest",
		Versions: []corev1alpha1.VersionBundle{
			{Name: "latest", Components: map[string]string{common.ComponentBackupAgent: "new"}},
			{Name: "previous", Components: map[string]string{common.ComponentBackupAgent: "old"}},
		},
	}
	tests := map[string]struct {
		instance corev1alpha1.Instance
		want     string
	}{
		"default bundle": {want: "pbm:new"},
		"bundle frozen in status": {
			instance: corev1alpha1.Instance{Status: corev1alpha1.InstanceStatus{Version: "previous"}},
			want:     "pbm:old",
		},
		"bundle chosen in spec": {
			instance: corev1alpha1.Instance{Spec: corev1alpha1.InstanceSpec{Version: "previous"}},
			want:     "pbm:old",
		},
		"version resolved on the component": {
			instance: corev1alpha1.Instance{Spec: corev1alpha1.InstanceSpec{
				Version:    "latest",
				Components: map[string]corev1alpha1.ComponentSpec{common.ComponentBackupAgent: {Version: "old"}},
			}},
			want: "pbm:old",
		},
		"unknown bundle falls back to the default": {
			instance: corev1alpha1.Instance{Spec: corev1alpha1.InstanceSpec{Version: "gone"}},
			want:     "pbm:new",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, backupAgentImage(spec, &tc.instance))
		})
	}
}

func TestSelectMainStorageName(t *testing.T) {
	tests := []struct {
		name     string
		storages []corev1alpha1.InstanceBackupStorage
		want     string
	}{
		{
			name:     "empty slice returns empty string",
			storages: nil,
			want:     "",
		},
		{
			name: "single storage without PITR returns its name",
			storages: []corev1alpha1.InstanceBackupStorage{
				{StorageRef: commonv1alpha1.ObjectRef{Name: "s3-main"}},
			},
			want: "s3-main",
		},
		{
			name: "no PITR storage — first entry wins",
			storages: []corev1alpha1.InstanceBackupStorage{
				{StorageRef: commonv1alpha1.ObjectRef{Name: "first"}},
				{StorageRef: commonv1alpha1.ObjectRef{Name: "second"}},
			},
			want: "first",
		},
		{
			name: "second storage has PITR — it wins over first",
			storages: []corev1alpha1.InstanceBackupStorage{
				{StorageRef: commonv1alpha1.ObjectRef{Name: "first"}},
				{StorageRef: commonv1alpha1.ObjectRef{Name: "second"}, PITR: &corev1alpha1.InstanceBackupStoragePITR{Enabled: true}},
			},
			want: "second",
		},
		{
			name: "first storage has PITR — it wins",
			storages: []corev1alpha1.InstanceBackupStorage{
				{StorageRef: commonv1alpha1.ObjectRef{Name: "first"}, PITR: &corev1alpha1.InstanceBackupStoragePITR{Enabled: true}},
				{StorageRef: commonv1alpha1.ObjectRef{Name: "second"}},
			},
			want: "first",
		},
		{
			name: "PITR present but disabled — falls back to first",
			storages: []corev1alpha1.InstanceBackupStorage{
				{StorageRef: commonv1alpha1.ObjectRef{Name: "first"}},
				{StorageRef: commonv1alpha1.ObjectRef{Name: "second"}, PITR: &corev1alpha1.InstanceBackupStoragePITR{Enabled: false}},
			},
			want: "first",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := selectMainStorageName(tc.storages)
			assert.Equal(t, tc.want, got)
		})
	}
}

func pitrStorageEntry(name string) corev1alpha1.InstanceBackupStorage {
	return corev1alpha1.InstanceBackupStorage{
		StorageRef: commonv1alpha1.ObjectRef{Name: name},
		PITR:       &corev1alpha1.InstanceBackupStoragePITR{Enabled: true},
	}
}

func TestBuildPSMDBPITRSpec(t *testing.T) {
	rawParams := func(p perconabackupmongodb.PerconaPITRParameters) *runtime.RawExtension {
		return &runtime.RawExtension{Raw: mustMarshal(t, p)}
	}

	tests := []struct {
		name     string
		storages []corev1alpha1.InstanceBackupStorage
		want     psmdbv1.PITRSpec
		wantErr  string
	}{
		{
			name:     "no storages — PITR disabled",
			storages: nil,
			want:     psmdbv1.PITRSpec{},
		},
		{
			name: "no PITR-enabled storage — PITR disabled",
			storages: []corev1alpha1.InstanceBackupStorage{
				{StorageRef: commonv1alpha1.ObjectRef{Name: "plain"}},
				{StorageRef: commonv1alpha1.ObjectRef{Name: "disabled"}, PITR: &corev1alpha1.InstanceBackupStoragePITR{Enabled: false}},
			},
			want: psmdbv1.PITRSpec{},
		},
		{
			name: "single PITR-enabled storage — PITR enabled",
			storages: []corev1alpha1.InstanceBackupStorage{
				{StorageRef: commonv1alpha1.ObjectRef{Name: "plain"}},
				pitrStorageEntry("s1"),
			},
			want: psmdbv1.PITRSpec{Enabled: true},
		},
		{
			name: "multiple PITR-enabled storages are rejected",
			storages: []corev1alpha1.InstanceBackupStorage{
				pitrStorageEntry("s1"),
				pitrStorageEntry("s2"),
			},
			wantErr: "at most one PITR-enabled storage",
		},
		{
			name: "PITR parameters are applied onto the cluster-global spec",
			storages: []corev1alpha1.InstanceBackupStorage{{
				StorageRef: commonv1alpha1.ObjectRef{Name: "s1"},
				PITR: &corev1alpha1.InstanceBackupStoragePITR{
					Enabled: true,
					Parameters: rawParams(perconabackupmongodb.PerconaPITRParameters{
						OplogSpanMin:     "10",
						OplogOnly:        true,
						CompressionType:  "zstd",
						CompressionLevel: pointer.ToInt32(5),
					}),
				},
			}},
			want: psmdbv1.PITRSpec{
				Enabled:          true,
				OplogSpanMin:     numstr.NumberString("10"),
				OplogOnly:        true,
				CompressionType:  compress.CompressionType("zstd"),
				CompressionLevel: pointer.ToInt(5),
			},
		},
		{
			name: "invalid PITR parameters payload is rejected",
			storages: []corev1alpha1.InstanceBackupStorage{{
				StorageRef: commonv1alpha1.ObjectRef{Name: "s1"},
				PITR:       &corev1alpha1.InstanceBackupStoragePITR{Enabled: true, Parameters: &runtime.RawExtension{Raw: []byte(`{"oplogSpanMin":`)}},
			}},
			wantErr: `storage "s1"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildPSMDBPITRSpec(tc.storages)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// mustMarshal marshals v to JSON, failing the test on error. Used to build
// *runtime.RawExtension payloads for parameter-decoding tests.
func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func TestBuildPSMDBTasksRetention(t *testing.T) {
	tests := []struct {
		name      string
		retention *corev1alpha1.BackupScheduleRetention
		want      *psmdbv1.BackupTaskSpecRetention
		wantErr   string
	}{
		{
			name: "unset retention keeps all backups",
		},
		{
			name: "count retention maps to the operator's count retention",
			retention: &corev1alpha1.BackupScheduleRetention{
				Type:  corev1alpha1.BackupScheduleRetentionTypeCount,
				Count: new(int32(7)),
			},
			want: &psmdbv1.BackupTaskSpecRetention{
				Type:              psmdbv1.BackupTaskSpecRetentionTypeCount,
				Count:             7,
				DeleteFromStorage: true,
			},
		},
		{
			name: "time retention is rejected",
			retention: &corev1alpha1.BackupScheduleRetention{
				Type:     corev1alpha1.BackupScheduleRetentionTypeTime,
				Duration: "30d",
			},
			wantErr: `schedule "daily": time retention is not supported by PSMDB`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			storages := []corev1alpha1.InstanceBackupStorage{{
				StorageRef: commonv1alpha1.ObjectRef{Name: "s3"},
				Schedules: []corev1alpha1.InstanceBackupSchedule{
					{Name: "daily", Enabled: true, Cron: "0 2 * * *", Retention: tc.retention},
				},
			}}

			got, err := buildPSMDBTasks(storages)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, tc.want, got[0].Retention)
		})
	}
}

func TestBuildPSMDBTasksParameters(t *testing.T) {
	rawParams := func(p perconabackupmongodb.PerconaBackupParameters) *runtime.RawExtension {
		return &runtime.RawExtension{Raw: mustMarshal(t, p)}
	}

	tests := []struct {
		name       string
		parameters *runtime.RawExtension
		want       psmdbv1.BackupTaskSpec
		wantErr    string
	}{
		{
			name: "unset parameters leave the operator's defaults",
			want: psmdbv1.BackupTaskSpec{Name: "daily", Enabled: true, Schedule: "0 2 * * *", StorageName: "s3"},
		},
		{
			name: "type, compression and volumeSnapshotClass are applied",
			parameters: rawParams(perconabackupmongodb.PerconaBackupParameters{
				Type:                "physical",
				CompressionType:     "zstd",
				CompressionLevel:    pointer.ToInt32(9),
				VolumeSnapshotClass: "csi-snapclass",
			}),
			want: psmdbv1.BackupTaskSpec{
				Name: "daily", Enabled: true, Schedule: "0 2 * * *", StorageName: "s3",
				Type:                defs.BackupType("physical"),
				CompressionType:     compress.CompressionType("zstd"),
				CompressionLevel:    pointer.ToInt(9),
				VolumeSnapshotClass: pointer.ToString("csi-snapclass"),
			},
		},
		{
			name:       "invalid parameters payload is rejected",
			parameters: &runtime.RawExtension{Raw: []byte(`{"type":`)},
			wantErr:    `schedule "daily"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			storages := []corev1alpha1.InstanceBackupStorage{{
				StorageRef: commonv1alpha1.ObjectRef{Name: "s3"},
				Schedules: []corev1alpha1.InstanceBackupSchedule{
					{Name: "daily", Enabled: true, Cron: "0 2 * * *", Parameters: tc.parameters},
				},
			}}

			got, err := buildPSMDBTasks(storages)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, tc.want, got[0])
		})
	}
}

func TestBackupAgentResources(t *testing.T) {
	defaultResources := corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("1G"),
			corev1.ResourceCPU:    resource.MustParse("300m"),
		},
	}

	tests := []struct {
		name      string
		resources *corev1.ResourceRequirements
		want      corev1.ResourceRequirements
	}{
		{
			name: "nil resources use the default",
			want: defaultResources,
		},
		{
			name:      "limits override the default per-field",
			resources: &corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}},
			want: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1G"),
				},
			},
		},
		{
			name: "requests are carried over when set",
			resources: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512M")},
			},
			want: corev1.ResourceRequirements{
				Limits:   defaultResources.Limits,
				Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512M")},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, backupAgentResources(tc.resources))
		})
	}
}

func TestApplyRestoreParams(t *testing.T) {
	tests := []struct {
		name   string
		params perconabackupmongodb.PerconaRestoreParameters
		want   psmdbv1.PerconaServerMongoDBRestoreSpec
	}{
		{
			name: "empty parameters leave the spec untouched",
		},
		{
			name: "selective restore options are copied",
			params: perconabackupmongodb.PerconaRestoreParameters{
				Selective: &perconabackupmongodb.PerconaRestoreSelective{
					WithUsersAndRoles: true,
					Namespaces:        []string{"db.coll"},
					NamespaceFrom:     "old.coll",
					NamespaceTo:       "new.coll",
				},
			},
			want: psmdbv1.PerconaServerMongoDBRestoreSpec{
				Selective: &psmdbv1.SelectiveRestoreOpts{
					WithUsersAndRoles: true,
					Namespaces:        []string{"db.coll"},
					NamespaceFrom:     "old.coll",
					NamespaceTo:       "new.coll",
				},
			},
		},
		{
			name:   "replset remapping is copied",
			params: perconabackupmongodb.PerconaRestoreParameters{ReplsetRemapping: map[string]string{"rs0": "rs1"}},
			want:   psmdbv1.PerconaServerMongoDBRestoreSpec{RSMap: map[string]string{"rs0": "rs1"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got psmdbv1.PerconaServerMongoDBRestoreSpec
			applyRestoreParams(&got, tc.params)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestBackupStorageStatuses(t *testing.T) {
	mkTime := func(s string) *metav1.Time {
		ts, err := time.Parse(time.RFC3339, s)
		require.NoError(t, err)
		return &metav1.Time{Time: ts}
	}
	mkOpBackup := func(name, cluster, storage string, completed, lrt *metav1.Time) *psmdbv1.PerconaServerMongoDBBackup {
		return &psmdbv1.PerconaServerMongoDBBackup{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"},
			Spec:       psmdbv1.PerconaServerMongoDBBackupSpec{ClusterName: cluster, StorageName: storage},
			Status: psmdbv1.PerconaServerMongoDBBackupStatus{
				State:                psmdbv1.BackupStateReady,
				CompletedAt:          completed,
				LatestRestorableTime: lrt,
			},
		}
	}
	instance := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "ns"},
		Spec: corev1alpha1.InstanceSpec{
			ProviderRef: commonv1alpha1.ObjectRef{Name: "percona-server-mongodb"},
			Backup: &corev1alpha1.InstanceBackupSpec{
				Enabled:  true,
				ClassRef: commonv1alpha1.ObjectRef{Name: "pbm"},
				Storages: []corev1alpha1.InstanceBackupStorage{
					pitrStorageEntry("s1"),
					{StorageRef: commonv1alpha1.ObjectRef{Name: "s2"}},
				},
			},
		},
	}

	tests := []struct {
		name    string
		objects []client.Object
		want    []corev1alpha1.InstanceBackupStorageStatus
	}{
		{
			name:    "no operator backups — PITR-enabled storage reports Unavailable",
			objects: nil,
			want: []corev1alpha1.InstanceBackupStorageStatus{
				{Name: "s1", PITR: &corev1alpha1.InstanceBackupStoragePITRStatus{
					State:  corev1alpha1.PITRStateUnavailable,
					Reason: pitrReasonNoBackups,
				}},
				{Name: "s2"},
			},
		},
		{
			name: "window spans oldest completion to newest restorable time",
			objects: []client.Object{
				mkOpBackup("b1", "db", "s1", mkTime("2026-07-01T09:00:00Z"), mkTime("2026-07-01T10:00:00Z")),
				mkOpBackup("b2", "db", "s1", mkTime("2026-07-02T09:00:00Z"), mkTime("2026-07-02T10:00:00Z")),
				mkOpBackup("b3", "db", "s2", mkTime("2026-07-03T09:00:00Z"), mkTime("2026-07-03T10:00:00Z")),
			},
			want: []corev1alpha1.InstanceBackupStorageStatus{
				{Name: "s1", PITR: &corev1alpha1.InstanceBackupStoragePITRStatus{
					EarliestRestorableTime: mkTime("2026-07-01T09:00:00Z"),
					LatestRestorableTime:   mkTime("2026-07-02T10:00:00Z"),
					State:                  corev1alpha1.PITRStateAvailable,
					Reason:                 pitrReasonWindowAvailable,
				}},
				// s2 has backups but PITR is not enabled on it: no PITR block.
				{Name: "s2"},
			},
		},
		{
			name: "other clusters ignored; no restorable time yet is Unavailable",
			objects: []client.Object{
				mkOpBackup("b1", "other-db", "s1", mkTime("2026-07-05T09:00:00Z"), mkTime("2026-07-05T10:00:00Z")),
				mkOpBackup("b2", "db", "s1", mkTime("2026-07-06T09:00:00Z"), nil),
			},
			want: []corev1alpha1.InstanceBackupStorageStatus{
				{Name: "s1", PITR: &corev1alpha1.InstanceBackupStoragePITRStatus{
					State:  corev1alpha1.PITRStateUnavailable,
					Reason: pitrReasonNoBackups,
				}},
				{Name: "s2"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, corev1alpha1.AddToScheme(scheme))
			require.NoError(t, psmdbv1.SchemeBuilder.AddToScheme(scheme))
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(append([]client.Object{instance}, tc.objects...)...).
				Build()
			c := controller.NewContext(context.Background(), fakeClient, instance, "percona-server-mongodb")

			p := &PSMDBProvider{}
			got, err := p.BackupStorageStatuses(c)
			require.NoError(t, err)
			require.Len(t, got, len(tc.want))
			for i, want := range tc.want {
				assert.Equal(t, want.Name, got[i].Name)
				if want.PITR == nil {
					assert.Nil(t, got[i].PITR, "storage %q: unexpected PITR block", want.Name)
					continue
				}
				require.NotNil(t, got[i].PITR, "storage %q: missing PITR block", want.Name)
				assert.Equal(t, want.PITR.State, got[i].PITR.State, "storage %q", want.Name)
				assert.Equal(t, want.PITR.Reason, got[i].PITR.Reason, "storage %q", want.Name)
				assertTimeEqual(t, want.PITR.EarliestRestorableTime, got[i].PITR.EarliestRestorableTime, "storage %q earliest", want.Name)
				assertTimeEqual(t, want.PITR.LatestRestorableTime, got[i].PITR.LatestRestorableTime, "storage %q latest", want.Name)
			}
		})
	}
}

func assertTimeEqual(t *testing.T, want, got *metav1.Time, msgAndArgs ...any) {
	t.Helper()
	if want == nil {
		assert.Nil(t, got, msgAndArgs...)
		return
	}
	require.NotNil(t, got, msgAndArgs...)
	assert.True(t, want.Equal(got), msgAndArgs...)
}

func TestSyncBackupWaitsForClusterRollout(t *testing.T) {
	const ns, cluster, storage = "db", "my-mongo", "s3"

	psmdbCluster := func(generation, observed int64, state psmdbv1.AppState) *psmdbv1.PerconaServerMongoDB {
		return &psmdbv1.PerconaServerMongoDB{
			ObjectMeta: metav1.ObjectMeta{Name: cluster, Namespace: ns, Generation: generation},
			Spec: psmdbv1.PerconaServerMongoDBSpec{Backup: psmdbv1.BackupSpec{
				Enabled:  true,
				Storages: map[string]psmdbv1.BackupStorageSpec{storage: {Type: psmdbv1.BackupStorageS3}},
			}},
			Status: psmdbv1.PerconaServerMongoDBStatus{ObservedGeneration: observed, State: state},
		}
	}
	runningOperatorBackup := &psmdbv1.PerconaServerMongoDBBackup{
		ObjectMeta: metav1.ObjectMeta{Name: "b1", Namespace: ns},
		Spec:       psmdbv1.PerconaServerMongoDBBackupSpec{ClusterName: cluster, StorageName: storage},
		Status:     psmdbv1.PerconaServerMongoDBBackupStatus{State: psmdbv1.BackupStateRunning},
	}

	tests := []struct {
		name          string
		cluster       *psmdbv1.PerconaServerMongoDB
		existing      []client.Object
		wantState     backupv1alpha1.BackupState
		wantRequested bool
	}{
		{
			name:      "backups just enabled: operator has not observed the change",
			cluster:   psmdbCluster(3, 2, psmdbv1.AppStateReady),
			wantState: backupv1alpha1.BackupStatePending,
		},
		{
			name:      "backup agent rollout pending (SmartUpdate)",
			cluster:   psmdbCluster(3, 3, psmdbv1.AppStateInit),
			wantState: backupv1alpha1.BackupStatePending,
		},
		{
			name:          "rolled out and ready",
			cluster:       psmdbCluster(3, 3, psmdbv1.AppStateReady),
			wantState:     backupv1alpha1.BackupStatePending,
			wantRequested: true,
		},
		{
			name:          "already requested backup keeps reporting while the cluster is busy",
			cluster:       psmdbCluster(4, 4, psmdbv1.AppStateInit),
			existing:      []client.Object{runningOperatorBackup},
			wantState:     backupv1alpha1.BackupStateRunning,
			wantRequested: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, corev1alpha1.AddToScheme(scheme))
			require.NoError(t, backupv1alpha1.AddToScheme(scheme))
			require.NoError(t, psmdbv1.SchemeBuilder.AddToScheme(scheme))

			instance := &corev1alpha1.Instance{ObjectMeta: metav1.ObjectMeta{Name: cluster, Namespace: ns}}
			backup := &backupv1alpha1.Backup{
				ObjectMeta: metav1.ObjectMeta{Name: "b1", Namespace: ns},
				Spec:       backupv1alpha1.BackupSpec{StorageRef: commonv1alpha1.ObjectRef{Name: storage}},
			}
			objects := append([]client.Object{instance, backup, tc.cluster}, tc.existing...)
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			c := controller.NewContext(context.Background(), fakeClient, instance, "percona-server-mongodb")

			got, err := (&PSMDBProvider{}).SyncBackup(c, backup)
			require.NoError(t, err)
			assert.Equal(t, tc.wantState, got.State)

			err = fakeClient.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "b1"}, &psmdbv1.PerconaServerMongoDBBackup{})
			if tc.wantRequested {
				require.NoError(t, err)
			} else {
				assert.True(t, apierrors.IsNotFound(err), "operator backup must not be requested yet, got %v", err)
			}
		})
	}
}

// readySyncBackupCluster returns a PerconaServerMongoDB that has rolled out
// (ObservedGeneration == Generation, AppStateReady) and has storage
// registered, so SyncBackup proceeds straight to requesting the operator
// backup instead of reporting a Pending rollout wait.
func readySyncBackupCluster(ns, cluster, storage string) *psmdbv1.PerconaServerMongoDB {
	return &psmdbv1.PerconaServerMongoDB{
		ObjectMeta: metav1.ObjectMeta{Name: cluster, Namespace: ns, Generation: 1},
		Spec: psmdbv1.PerconaServerMongoDBSpec{Backup: psmdbv1.BackupSpec{
			Enabled:  true,
			Storages: map[string]psmdbv1.BackupStorageSpec{storage: {Type: psmdbv1.BackupStorageS3}},
		}},
		Status: psmdbv1.PerconaServerMongoDBStatus{ObservedGeneration: 1, State: psmdbv1.AppStateReady},
	}
}

// TestSyncBackupParameterValidation covers the gap reported against v0.6.0:
// spec.parameters.type was neither validated against the BackupClass's
// parametersSchema nor applied onto the operator backup, so e.g. a
// requested "physical" backup silently ran as "logical". It validates
// against the actual shipped BackupClass (definition/backupclasses), not a
// hand-rolled copy of its enums, so a schema change cannot silently drift
// out of sync with this test, as happened to a hardcoded "external" gap in
// an earlier version of this fix.
func TestSyncBackupParameterValidation(t *testing.T) {
	const ns, cluster, storage = "db", "my-mongo", "s3"

	rawParams := func(raw string) *runtime.RawExtension {
		return &runtime.RawExtension{Raw: []byte(raw)}
	}

	tests := []struct {
		name          string
		parameters    *runtime.RawExtension
		classRef      string
		wantState     backupv1alpha1.BackupState
		wantMsg       string
		wantRequested bool
		wantType      defs.BackupType
	}{
		{
			name:          "no parameters: operator defaults apply",
			wantState:     backupv1alpha1.BackupStatePending,
			wantRequested: true,
		},
		{
			name:          "type physical is applied onto the operator backup",
			parameters:    rawParams(`{"type":"physical"}`),
			wantState:     backupv1alpha1.BackupStatePending,
			wantRequested: true,
			wantType:      defs.BackupType("physical"),
		},
		{
			name:          "type external is accepted by the class schema",
			parameters:    rawParams(`{"type":"external"}`),
			wantState:     backupv1alpha1.BackupStatePending,
			wantRequested: true,
			wantType:      defs.BackupType("external"),
		},
		{
			name:       "an unsupported type is rejected instead of silently defaulting",
			parameters: rawParams(`{"type":"foo"}`),
			wantState:  backupv1alpha1.BackupStateFailed,
			wantMsg:    `Invalid parameters for BackupClass "percona-backup-mongodb"`,
		},
		{
			name:       "a compressionLevel out of the class's declared range is rejected",
			parameters: rawParams(`{"compressionLevel":99}`),
			wantState:  backupv1alpha1.BackupStateFailed,
			wantMsg:    `Invalid parameters for BackupClass "percona-backup-mongodb"`,
		},
		{
			name:       "an unknown parameter is rejected",
			parameters: rawParams(`{"unknownField":true}`),
			wantState:  backupv1alpha1.BackupStateFailed,
			wantMsg:    `Invalid parameters for BackupClass "percona-backup-mongodb"`,
		},
		{
			name:       "a BackupClass that does not exist yet reports pending",
			parameters: rawParams(`{"type":"physical"}`),
			classRef:   "does-not-exist",
			wantState:  backupv1alpha1.BackupStatePending,
			wantMsg:    `Waiting for BackupClass "does-not-exist"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, corev1alpha1.AddToScheme(scheme))
			require.NoError(t, backupv1alpha1.AddToScheme(scheme))
			require.NoError(t, psmdbv1.SchemeBuilder.AddToScheme(scheme))

			classRef := tc.classRef
			if classRef == "" {
				classRef = backupClassName
			}
			instance := &corev1alpha1.Instance{ObjectMeta: metav1.ObjectMeta{Name: cluster, Namespace: ns}}
			backup := &backupv1alpha1.Backup{
				ObjectMeta: metav1.ObjectMeta{Name: "b1", Namespace: ns},
				Spec: backupv1alpha1.BackupSpec{
					StorageRef: commonv1alpha1.ObjectRef{Name: storage},
					ClassRef:   commonv1alpha1.ObjectRef{Name: classRef},
					Parameters: tc.parameters,
				},
			}
			shipped := backupclasses.Shipped()
			require.Len(t, shipped, 1)
			bc := shipped[0].DeepCopy()

			objects := []client.Object{instance, backup, readySyncBackupCluster(ns, cluster, storage), bc}
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			c := controller.NewContext(context.Background(), fakeClient, instance, "percona-server-mongodb")

			got, err := (&PSMDBProvider{}).SyncBackup(c, backup)
			require.NoError(t, err)
			assert.Equal(t, tc.wantState, got.State)
			if tc.wantMsg != "" {
				assert.Contains(t, got.Message, tc.wantMsg)
			}

			opBackup := &psmdbv1.PerconaServerMongoDBBackup{}
			err = fakeClient.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "b1"}, opBackup)
			if !tc.wantRequested {
				assert.True(t, apierrors.IsNotFound(err), "operator backup must not be requested, got %v", err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantType, opBackup.Spec.Type)
		})
	}
}

// TestSyncBackupSucceedsOnReadyRegardlessOfRequestedType documents why
// SyncBackup does not re-check the requested type against the operator
// backup once it is Ready: the PSMDB operator sets status.type directly
// from spec.type (pkg/controller/perconaservermongodbbackup/backup.go,
// managedBackups.Status: "status.Type = cr.Spec.Type") rather than
// deriving it from what PBM actually ran. Since SyncBackup itself writes
// spec.type from the requested parameters, status.type can never disagree
// with what was requested once the operator reports Ready, so a Ready
// backup is always reported Succeeded -- there is no "run it and then
// discover it was the wrong type" scenario to guard against post-hoc.
func TestSyncBackupSucceedsOnReadyRegardlessOfRequestedType(t *testing.T) {
	const ns, cluster, storage = "db", "my-mongo", "s3"

	scheme := runtime.NewScheme()
	require.NoError(t, corev1alpha1.AddToScheme(scheme))
	require.NoError(t, backupv1alpha1.AddToScheme(scheme))
	require.NoError(t, psmdbv1.SchemeBuilder.AddToScheme(scheme))

	instance := &corev1alpha1.Instance{ObjectMeta: metav1.ObjectMeta{Name: cluster, Namespace: ns}}
	backup := &backupv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "b1", Namespace: ns},
		Spec: backupv1alpha1.BackupSpec{
			StorageRef: commonv1alpha1.ObjectRef{Name: storage},
			ClassRef:   commonv1alpha1.ObjectRef{Name: backupClassName},
			Parameters: &runtime.RawExtension{Raw: []byte(`{"type":"physical"}`)},
		},
	}
	// As the real operator would report it: status.type mirrors spec.type.
	existingOpBackup := &psmdbv1.PerconaServerMongoDBBackup{
		ObjectMeta: metav1.ObjectMeta{Name: "b1", Namespace: ns},
		Spec:       psmdbv1.PerconaServerMongoDBBackupSpec{ClusterName: cluster, StorageName: storage, Type: "physical"},
		Status:     psmdbv1.PerconaServerMongoDBBackupStatus{State: psmdbv1.BackupStateReady, Type: "physical"},
	}

	shipped := backupclasses.Shipped()
	require.Len(t, shipped, 1)
	bc := shipped[0].DeepCopy()

	objects := []client.Object{instance, backup, readySyncBackupCluster(ns, cluster, storage), bc, existingOpBackup}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	c := controller.NewContext(context.Background(), fakeClient, instance, "percona-server-mongodb")

	got, err := (&PSMDBProvider{}).SyncBackup(c, backup)
	require.NoError(t, err)
	assert.Equal(t, backupv1alpha1.BackupStateSucceeded, got.State)
	require.NotNil(t, got.CompletedAt)
}

const (
	testStorageCredentials = "storage-credentials"
	awsParameters          = `{"region":"eu-west-1","endpointURL":"https://s3.eu-west-1.amazonaws.com"}`
	minioParameters        = `{"region":"us-east-1","endpointURL":"http://minio.minio.svc:9000","forcePathStyle":true,"implementation":"minio"}`
	gcsParameters          = `{"region":"auto","endpointURL":"https://storage.googleapis.com","implementation":"gcs"}`
)

func testBackupStorage(name, parameters string, tls *commonv1alpha1.TLSConfig) *backupv1alpha1.BackupStorage {
	return &backupv1alpha1.BackupStorage{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", Generation: 1},
		Spec: backupv1alpha1.BackupStorageSpec{
			BackupStorageClassRef: commonv1alpha1.ObjectRef{Name: s3.ClassName},
			Location:              backupv1alpha1.BackupStorageLocation{ObjectStore: &backupv1alpha1.ObjectStoreLocation{Bucket: "backups", Prefix: "team"}},
			Parameters:            &runtime.RawExtension{Raw: []byte(parameters)},
			Authentication: commonv1alpha1.Authentication{
				Mode:                 s3.AuthModeAccessKey,
				CredentialsSecretRef: &commonv1alpha1.SecretRef{Name: testStorageCredentials},
			},
			TLS: tls,
		},
		Status: backupv1alpha1.BackupStorageStatus{Conditions: []metav1.Condition{{
			Type:               backupv1alpha1.BackupStorageConditionAccepted,
			Status:             metav1.ConditionTrue,
			Reason:             backupv1alpha1.ReasonAccepted,
			ObservedGeneration: 1,
		}}},
	}
}

func testStorageSecrets() []client.Object {
	return []client.Object{
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: testStorageCredentials, Namespace: "ns"},
			Data: map[string][]byte{
				s3.KeyAWSAccessKeyID:     []byte("id"),
				s3.KeyAWSSecretAccessKey: []byte("secret"),
			},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "storage-ca", Namespace: "ns"},
			Data:       map[string][]byte{"ca.crt": []byte("-----BEGIN CERTIFICATE-----")},
		},
	}
}

func testBackupInstance(storages ...corev1alpha1.InstanceBackupStorage) *corev1alpha1.Instance {
	return &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "ns", UID: "uid-1"},
		Spec: corev1alpha1.InstanceSpec{
			ProviderRef: commonv1alpha1.ObjectRef{Name: "percona-server-mongodb"},
			Backup: &corev1alpha1.InstanceBackupSpec{
				Enabled:  true,
				ClassRef: commonv1alpha1.ObjectRef{Name: "percona-backup-mongodb"},
				Storages: storages,
			},
		},
	}
}

func backupTestContext(t *testing.T, instance *corev1alpha1.Instance, objs ...client.Object) *controller.Context {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, corev1alpha1.AddToScheme(scheme))
	require.NoError(t, backupv1alpha1.AddToScheme(scheme))
	require.NoError(t, psmdbv1.SchemeBuilder.AddToScheme(scheme))
	provider := &corev1alpha1.Provider{ObjectMeta: metav1.ObjectMeta{Name: "percona-server-mongodb"}}
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(append([]client.Object{instance, provider}, objs...)...).
		Build()
	return controller.NewContext(context.Background(), cl, instance, "percona-server-mongodb")
}

func TestPSMDBStorage(t *testing.T) {
	const prefix = "team/backups/ns/db/uid-1/s3"
	ca := &commonv1alpha1.TLSConfig{CA: &commonv1alpha1.CABundleSource{
		SecretKeyRef: &commonv1alpha1.SecretKeyRef{Name: "storage-ca", Key: "ca.crt"},
	}}
	insecure := &commonv1alpha1.TLSConfig{InsecureSkipVerify: true}

	tests := map[string]struct {
		parameters string
		tls        *commonv1alpha1.TLSConfig
		want       psmdbv1.BackupStorageSpec
		wantErr    error
	}{
		"amazon s3 uses the s3 type": {
			parameters: awsParameters,
			want: psmdbv1.BackupStorageSpec{Type: psmdbv1.BackupStorageS3, S3: psmdbv1.BackupStorageS3Spec{
				Bucket:            "backups",
				Prefix:            prefix,
				Region:            "eu-west-1",
				EndpointURL:       "https://s3.eu-west-1.amazonaws.com",
				CredentialsSecret: testStorageCredentials,
				ForcePathStyle:    new(false),
			}},
		},
		"amazon s3 without certificate verification": {
			parameters: awsParameters,
			tls:        insecure,
			want: psmdbv1.BackupStorageSpec{Type: psmdbv1.BackupStorageS3, S3: psmdbv1.BackupStorageS3Spec{
				Bucket:                "backups",
				Prefix:                prefix,
				Region:                "eu-west-1",
				EndpointURL:           "https://s3.eu-west-1.amazonaws.com",
				CredentialsSecret:     testStorageCredentials,
				InsecureSkipTLSVerify: true,
				ForcePathStyle:        new(false),
			}},
		},
		"a custom CA uses the minio type": {
			parameters: awsParameters,
			tls:        ca,
			want: psmdbv1.BackupStorageSpec{Type: psmdbv1.BackupStorageMinio, Minio: psmdbv1.BackupStorageMinioSpec{
				Bucket:            "backups",
				Prefix:            prefix,
				Region:            "eu-west-1",
				EndpointURL:       "s3.eu-west-1.amazonaws.com:443",
				Secure:            true,
				CredentialsSecret: testStorageCredentials,
				ForcePathStyle:    new(false),
				CABundle: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "storage-ca"},
					Key:                  "ca.crt",
				},
			}},
		},
		"minio over plain http uses the minio type": {
			parameters: minioParameters,
			want: psmdbv1.BackupStorageSpec{Type: psmdbv1.BackupStorageMinio, Minio: psmdbv1.BackupStorageMinioSpec{
				Bucket:            "backups",
				Prefix:            prefix,
				Region:            "us-east-1",
				EndpointURL:       "minio.minio.svc:9000",
				CredentialsSecret: testStorageCredentials,
				ForcePathStyle:    new(true),
			}},
		},
		"other s3-compatible store without certificate verification": {
			parameters: `{"region":"us-east-1","endpointURL":"https://seaweedfs.seaweedfs.svc","forcePathStyle":true,"implementation":"other"}`,
			tls:        insecure,
			want: psmdbv1.BackupStorageSpec{Type: psmdbv1.BackupStorageMinio, Minio: psmdbv1.BackupStorageMinioSpec{
				Bucket:                "backups",
				Prefix:                prefix,
				Region:                "us-east-1",
				EndpointURL:           "seaweedfs.seaweedfs.svc:443",
				Secure:                true,
				CredentialsSecret:     testStorageCredentials,
				InsecureSkipTLSVerify: true,
				ForcePathStyle:        new(true),
			}},
		},
		"ceph uses the minio type": {
			parameters: `{"region":"default","endpointURL":"https://rgw.example.com:7480","implementation":"ceph"}`,
			want: psmdbv1.BackupStorageSpec{Type: psmdbv1.BackupStorageMinio, Minio: psmdbv1.BackupStorageMinioSpec{
				Bucket:            "backups",
				Prefix:            prefix,
				Region:            "default",
				EndpointURL:       "rgw.example.com:7480",
				Secure:            true,
				CredentialsSecret: testStorageCredentials,
				ForcePathStyle:    new(false),
			}},
		},
		"gcs is not supported": {
			parameters: gcsParameters,
			wantErr:    controller.ErrImplementationUnsupported,
		},
		"an unknown parameter is not supported": {
			parameters: `{"region":"eu-west-1","endpointURL":"https://s3.eu-west-1.amazonaws.com","sse":"aws:kms"}`,
			wantErr:    backupstorageclasses.ErrUnsupportedParameter,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			bs := testBackupStorage("s3", tc.parameters, tc.tls)
			c := backupTestContext(t, testBackupInstance(), append(testStorageSecrets(), bs)...)

			got, err := psmdbStorage(c, bs, prefix)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestBuildBackupSpecStorages(t *testing.T) {
	storage := corev1alpha1.InstanceBackupStorage{
		StorageRef: commonv1alpha1.ObjectRef{Name: "s3"},
		PITR:       &corev1alpha1.InstanceBackupStoragePITR{Enabled: true},
		Schedules:  []corev1alpha1.InstanceBackupSchedule{{Name: "daily", Enabled: true, Cron: "0 2 * * *"}},
	}
	notAccepted := func() client.Object {
		bs := testBackupStorage("s3", awsParameters, nil)
		bs.Status.Conditions[0].Status = metav1.ConditionFalse
		bs.Status.Conditions[0].Reason = backupv1alpha1.ReasonCredentialsNotFound
		return bs
	}
	held := psmdbv1.BackupStorageSpec{
		Type: psmdbv1.BackupStorageS3,
		S3:   psmdbv1.BackupStorageS3Spec{Bucket: "backups", Prefix: "team/backups/ns/db/uid-1/s3", Region: "eu-west-1"},
	}
	live := func() client.Object {
		return &psmdbv1.PerconaServerMongoDB{
			ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "ns"},
			Spec: psmdbv1.PerconaServerMongoDBSpec{Backup: psmdbv1.BackupSpec{
				Storages: map[string]psmdbv1.BackupStorageSpec{"s3": held},
			}},
		}
	}
	heldMain := held
	heldMain.Main = true

	tests := map[string]struct {
		objects    []client.Object
		want       map[string]psmdbv1.BackupStorageSpec
		wantReason string
	}{
		"a usable storage is written under the instance path": {
			objects: []client.Object{testBackupStorage("s3", awsParameters, nil)},
			want: map[string]psmdbv1.BackupStorageSpec{"s3": {
				Type: psmdbv1.BackupStorageS3,
				Main: true,
				S3: psmdbv1.BackupStorageS3Spec{
					Bucket:            "backups",
					Prefix:            "team/backups/ns/db/uid-1/s3",
					Region:            "eu-west-1",
					EndpointURL:       "https://s3.eu-west-1.amazonaws.com",
					CredentialsSecret: testStorageCredentials,
					ForcePathStyle:    new(false),
				},
			}},
		},
		"a storage that is not accepted keeps what the engine has": {
			objects:    []client.Object{notAccepted(), live()},
			want:       map[string]psmdbv1.BackupStorageSpec{"s3": heldMain},
			wantReason: corev1alpha1.ReasonBackupStorageNotAccepted,
		},
		"a storage the engine cannot use keeps what the engine has": {
			objects:    []client.Object{testBackupStorage("s3", gcsParameters, nil), live()},
			want:       map[string]psmdbv1.BackupStorageSpec{"s3": heldMain},
			wantReason: corev1alpha1.ReasonImplementationUnsupported,
		},
		"a storage that is not accepted is not added": {
			objects:    []client.Object{notAccepted()},
			want:       map[string]psmdbv1.BackupStorageSpec{},
			wantReason: corev1alpha1.ReasonBackupStorageNotAccepted,
		},
		"a missing storage is not added": {
			want:       map[string]psmdbv1.BackupStorageSpec{},
			wantReason: corev1alpha1.ReasonBackupStorageNotFound,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			c := backupTestContext(t, testBackupInstance(storage), append(testStorageSecrets(), tc.objects...)...)

			got, err := buildBackupSpec(c)
			require.NoError(t, err)
			assert.True(t, got.Enabled)
			assert.Equal(t, tc.want, got.Storages)
			if len(tc.want) == 0 {
				assert.Empty(t, got.Tasks, "schedules of a storage the engine lacks")
				assert.False(t, got.PITR.Enabled, "PITR on a storage the engine lacks")
			} else {
				require.Len(t, got.Tasks, 1)
				assert.Equal(t, "s3", got.Tasks[0].StorageName)
				assert.True(t, got.PITR.Enabled)
			}

			resolved, err := c.BackupStorages()
			require.NoError(t, err)
			require.Len(t, resolved, 1)
			assert.Equal(t, tc.wantReason, resolved[0].Reason)
		})
	}
}

func TestBuildBackupSpecReportsConfigOfStoragesNotRendered(t *testing.T) {
	instance := testBackupInstance(pitrStorageEntry("s3"), pitrStorageEntry("missing"))
	c := backupTestContext(t, instance, append(testStorageSecrets(), testBackupStorage("s3", awsParameters, nil))...)

	_, err := buildBackupSpec(c)
	var configErr *controller.BackupConfigError
	require.ErrorAs(t, err, &configErr)
	assert.Equal(t, "PITRConfigInvalid", configErr.Reason)
}

func TestBuildBackupSpecNumParallelCollections(t *testing.T) {
	rawParams := func(p components.BackupParameters) *runtime.RawExtension {
		return &runtime.RawExtension{Raw: mustMarshal(t, p)}
	}

	tests := map[string]struct {
		parameters         *runtime.RawExtension
		wantBackupOptions  int
		wantRestoreOptions *int
		wantErr            string
	}{
		"unset parameters leave the operator's defaults": {},
		"numParallelCollections is applied for backups": {
			parameters:        rawParams(components.BackupParameters{NumParallelCollections: pointer.ToInt32(8)}),
			wantBackupOptions: 8,
		},
		"restoreNumParallelCollections configures restoreOptions": {
			parameters:         rawParams(components.BackupParameters{RestoreNumParallelCollections: pointer.ToInt32(6)}),
			wantRestoreOptions: pointer.ToInt(6),
		},
		"both are applied independently": {
			parameters: rawParams(components.BackupParameters{
				NumParallelCollections:        pointer.ToInt32(8),
				RestoreNumParallelCollections: pointer.ToInt32(6),
			}),
			wantBackupOptions:  8,
			wantRestoreOptions: pointer.ToInt(6),
		},
		"invalid parameters payload is rejected": {
			parameters: &runtime.RawExtension{Raw: []byte(`{"numParallelCollections":`)},
			wantErr:    "unmarshal backupAgent parameters",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			instance := testBackupInstance()
			instance.Spec.Components = map[string]corev1alpha1.ComponentSpec{
				common.ComponentBackupAgent: {Parameters: tc.parameters},
			}
			c := backupTestContext(t, instance)

			got, err := buildBackupSpec(c)
			if tc.wantErr != "" {
				var configErr *controller.BackupConfigError
				require.ErrorAs(t, err, &configErr)
				assert.Equal(t, "BackupAgentParametersInvalid", configErr.Reason)
				assert.Contains(t, configErr.Message, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, got.Configuration.BackupOptions)
			assert.Equal(t, tc.wantBackupOptions, got.Configuration.BackupOptions.NumParallelCollections)
			if tc.wantRestoreOptions == nil {
				assert.Nil(t, got.Configuration.RestoreOptions)
			} else {
				require.NotNil(t, got.Configuration.RestoreOptions)
				assert.Equal(t, *tc.wantRestoreOptions, got.Configuration.RestoreOptions.NumParallelCollections)
			}
		})
	}
}

// TestBuildBackupSpecValidatesBackupAgentParametersSchema covers the
// backupAgent component's "advanced" parameters (components.BackupParameters,
// e.g. numParallelCollections), the other RawExtension payload this provider
// reads besides Backup.spec.parameters. It carries the same kind of
// kubebuilder constraints (Minimum=1) as PerconaBackupParameters, published
// on Provider.spec.components.backupAgent.parametersSchema (see
// charts/.../generated/provider-spec.yaml) -- this test builds a Provider
// with that schema directly (there is no embedded Go accessor for it, unlike
// backupclasses.Shipped() for BackupClasses) to confirm buildBackupSpec
// actually validates against it instead of accepting any value PBM might
// reject at runtime.
func TestBuildBackupSpecValidatesBackupAgentParametersSchema(t *testing.T) {
	schema := &commonv1alpha1.ParametersSchema{
		OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"numParallelCollections": {Type: "integer", Minimum: pointer.ToFloat64(1)},
			},
		},
	}
	rawParams := func(raw string) *runtime.RawExtension {
		return &runtime.RawExtension{Raw: []byte(raw)}
	}

	tests := map[string]struct {
		parameters *runtime.RawExtension
		wantErr    string
		wantValue  int
	}{
		"a value within the schema's range is accepted": {
			parameters: rawParams(`{"numParallelCollections":4}`),
			wantValue:  4,
		},
		"a value below the schema's declared minimum is rejected": {
			parameters: rawParams(`{"numParallelCollections":-1}`),
			wantErr:    "validate backupAgent parameters",
		},
		"an unknown field is rejected": {
			parameters: rawParams(`{"bogus":true}`),
			wantErr:    "validate backupAgent parameters",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, corev1.AddToScheme(scheme))
			require.NoError(t, corev1alpha1.AddToScheme(scheme))
			require.NoError(t, backupv1alpha1.AddToScheme(scheme))
			require.NoError(t, psmdbv1.SchemeBuilder.AddToScheme(scheme))

			instance := testBackupInstance()
			instance.Spec.Components = map[string]corev1alpha1.ComponentSpec{
				common.ComponentBackupAgent: {Parameters: tc.parameters},
			}
			provider := &corev1alpha1.Provider{
				ObjectMeta: metav1.ObjectMeta{Name: "percona-server-mongodb"},
				Spec: corev1alpha1.ProviderSpec{
					Components: map[string]corev1alpha1.Component{
						common.ComponentBackupAgent: {Type: "backup", ParametersSchema: schema},
					},
				},
			}
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance, provider).Build()
			c := controller.NewContext(context.Background(), cl, instance, "percona-server-mongodb")

			got, err := buildBackupSpec(c)
			if tc.wantErr != "" {
				var configErr *controller.BackupConfigError
				require.ErrorAs(t, err, &configErr)
				assert.Equal(t, "BackupAgentParametersInvalid", configErr.Reason)
				assert.Contains(t, configErr.Message, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, got.Configuration.BackupOptions)
			assert.Equal(t, tc.wantValue, got.Configuration.BackupOptions.NumParallelCollections)
		})
	}
}

func TestBuildBackupSpecAdvancedBackupAgentOptions(t *testing.T) {
	rawParams := func(p components.BackupParameters) *runtime.RawExtension {
		return &runtime.RawExtension{Raw: mustMarshal(t, p)}
	}

	instance := testBackupInstance()
	instance.Spec.Components = map[string]corev1alpha1.ComponentSpec{
		common.ComponentBackupAgent: {Parameters: rawParams(components.BackupParameters{
			BackupOplogSpanMin:         pointer.ToFloat64(15),
			Priority:                   map[string]float64{"*": 1, "rs0/host:27017": 2.5},
			RestoreBatchSize:           pointer.ToInt32(500),
			RestoreNumInsertionWorkers: pointer.ToInt32(10),
			RestoreNumDownloadWorkers:  pointer.ToInt32(4),
			RestoreMaxDownloadBufferMB: pointer.ToInt32(256),
			RestoreDownloadChunkMB:     pointer.ToInt32(32),
		})},
	}
	c := backupTestContext(t, instance)

	got, err := buildBackupSpec(c)
	require.NoError(t, err)
	require.NotNil(t, got.Configuration.BackupOptions)
	assert.Equal(t, 15.0, got.Configuration.BackupOptions.OplogSpanMin)
	assert.Equal(t, map[string]float64{"*": 1, "rs0/host:27017": 2.5}, got.Configuration.BackupOptions.Priority)

	require.NotNil(t, got.Configuration.RestoreOptions)
	assert.Equal(t, 500, got.Configuration.RestoreOptions.BatchSize)
	assert.Equal(t, 10, got.Configuration.RestoreOptions.NumInsertionWorkers)
	assert.Equal(t, 4, got.Configuration.RestoreOptions.NumDownloadWorkers)
	assert.Equal(t, 256, got.Configuration.RestoreOptions.MaxDownloadBufferMb)
	assert.Equal(t, 32, got.Configuration.RestoreOptions.DownloadChunkMb)
}

func TestBuildExternalBackupSource(t *testing.T) {
	const (
		prefix      = "team/backups/ns/src/uid-0/s3"
		destination = "s3://backups/" + prefix + "/2026-09-01T05:55:39Z"
	)
	backup := &backupv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "imported", Namespace: "ns"},
		Spec: backupv1alpha1.BackupSpec{
			Origin: backupv1alpha1.BackupOrigin{
				Type: backupv1alpha1.BackupOriginTypeExternal,
				External: &backupv1alpha1.BackupOriginExternal{
					Path: "backups/ns/src/uid-0/s3/2026-09-01T05:55:39Z",
				},
			},
			StorageRef: commonv1alpha1.ObjectRef{Name: "s3"},
		},
	}

	tests := map[string]struct {
		parameters string
		secrets    []client.Object
		want       *psmdbv1.PerconaServerMongoDBBackupStatus
		wantState  backupv1alpha1.RestoreState
	}{
		"amazon s3 reads under the storage's location": {
			parameters: awsParameters,
			secrets:    testStorageSecrets(),
			want: &psmdbv1.PerconaServerMongoDBBackupStatus{
				Destination: destination,
				StorageName: "s3",
				S3: &psmdbv1.BackupStorageS3Spec{
					Bucket:            "backups",
					Prefix:            prefix,
					Region:            "eu-west-1",
					EndpointURL:       "https://s3.eu-west-1.amazonaws.com",
					CredentialsSecret: testStorageCredentials,
					ForcePathStyle:    new(false),
				},
			},
		},
		"an s3-compatible store reads through the minio type": {
			parameters: minioParameters,
			secrets:    testStorageSecrets(),
			want: &psmdbv1.PerconaServerMongoDBBackupStatus{
				Destination: destination,
				StorageName: "s3",
				Minio: &psmdbv1.BackupStorageMinioSpec{
					Bucket:            "backups",
					Prefix:            prefix,
					Region:            "us-east-1",
					EndpointURL:       "minio.minio.svc:9000",
					CredentialsSecret: testStorageCredentials,
					ForcePathStyle:    new(true),
				},
			},
		},
		"missing credentials wait": {
			parameters: awsParameters,
			wantState:  backupv1alpha1.RestoreStatePending,
		},
		"a storage the engine cannot use fails": {
			parameters: gcsParameters,
			secrets:    testStorageSecrets(),
			wantState:  backupv1alpha1.RestoreStateFailed,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			objs := append([]client.Object{testBackupStorage("s3", tc.parameters, nil)}, tc.secrets...)
			c := backupTestContext(t, testBackupInstance(), objs...)

			got, exec, err := buildExternalBackupSource(c, backup)
			require.NoError(t, err)
			assert.Equal(t, tc.wantState, exec.State)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestMirrorCarriesBackupType verifies that a Backup CR mirrored from a
// scheduled PerconaServerMongoDBBackup (one carrying the backup-ancestor
// label) reports the same backup type the operator assigned the scheduled
// backup, instead of leaving spec.parameters empty.
func TestMirrorCarriesBackupType(t *testing.T) {
	const ns, cluster, storage, task = "db", "my-mongo", "s3", "daily"

	scheme := runtime.NewScheme()
	require.NoError(t, corev1alpha1.AddToScheme(scheme))
	require.NoError(t, backupv1alpha1.AddToScheme(scheme))
	require.NoError(t, psmdbv1.SchemeBuilder.AddToScheme(scheme))

	instance := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: cluster, Namespace: ns},
		Spec: corev1alpha1.InstanceSpec{
			ProviderRef: commonv1alpha1.ObjectRef{Name: "percona-server-mongodb"},
			Backup:      &corev1alpha1.InstanceBackupSpec{ClassRef: commonv1alpha1.ObjectRef{Name: backupClassName}},
		},
	}
	level := 5
	vsc := "my-vsc"
	ub := &psmdbv1.PerconaServerMongoDBBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cron-my-mongo-20260101000000",
			Namespace: ns,
			Labels:    map[string]string{psmdbBackupAncestorLabel: task},
		},
		Spec: psmdbv1.PerconaServerMongoDBBackupSpec{
			ClusterName:         cluster,
			StorageName:         storage,
			Type:                defs.BackupType("physical"),
			Compression:         compress.CompressionType("zstd"),
			CompressionLevel:    &level,
			VolumeSnapshotClass: &vsc,
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance).Build()

	p := &PSMDBProvider{BaseProvider: controller.BaseProvider{ProviderName: "percona-server-mongodb"}}
	got, err := p.Mirror(context.Background(), fakeClient, ub)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.Spec.Parameters)

	var params perconabackupmongodb.PerconaBackupParameters
	require.NoError(t, json.Unmarshal(got.Spec.Parameters.Raw, &params))
	assert.Equal(t, "physical", params.Type)
	assert.Equal(t, "zstd", params.CompressionType)
	require.NotNil(t, params.CompressionLevel)
	assert.Equal(t, int32(5), *params.CompressionLevel)
	assert.Equal(t, "my-vsc", params.VolumeSnapshotClass)
	assert.Equal(t, task, got.Spec.ScheduleName)
}
