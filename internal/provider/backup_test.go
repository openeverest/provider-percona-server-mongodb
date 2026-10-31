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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	"github.com/openeverest/openeverest/v2/api/backup/v1alpha1/storageclasses"
	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

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
			BackupStorageClassRef: commonv1alpha1.ObjectRef{Name: storageclasses.ClassS3},
			Location:              backupv1alpha1.BackupStorageLocation{Bucket: "backups", Prefix: "team"},
			Parameters:            &runtime.RawExtension{Raw: []byte(parameters)},
			Authentication: commonv1alpha1.Authentication{
				Mode:                 storageclasses.AuthModeAccessKey,
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
				storageclasses.KeyAWSAccessKeyID:     []byte("id"),
				storageclasses.KeyAWSSecretAccessKey: []byte("secret"),
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
			wantErr:    storageclasses.ErrUnsupportedParameter,
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
