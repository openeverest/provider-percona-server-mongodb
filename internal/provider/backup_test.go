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

	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
)

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
