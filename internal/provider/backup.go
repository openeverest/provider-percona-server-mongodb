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
	"fmt"

	"github.com/AlekSi/pointer"
	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
)

const (
	// psmdbDeleteBackupFinalizer is the PSMDB operator finalizer
	// that protects S3 storage during backup deletion.
	psmdbDeleteBackupFinalizer = "percona.com/delete-backup"

	// psmdbBackupAncestorLabel is the label key that PSMDB stamps on
	// PerconaServerMongoDBBackup objects produced by a scheduled task. The
	// value is the BackupTaskSpec.Name. Backups created on demand (no task)
	// do not carry this label. Mirrors `naming.LabelBackupAncestor` in the
	// PSMDB operator (kept as a string constant here to avoid pulling in the
	// operator's internal naming package).
	psmdbBackupAncestorLabel = "percona.com/backup-ancestor"
)

// =============================================================================
// BACKUP SPEC — complete engine configuration built at Sync time
// =============================================================================

// buildBackupSpec returns the complete psmdbv1.BackupSpec that SyncPSMDB
// stamps onto every PerconaServerMongoDB in a single Apply call. The PSMDB
// operator requires Backup.Image to be set even when backups are disabled
// (the backup-agent image is referenced by the regular DB pods). When the
// Instance has .spec.backup enabled, this function also resolves storages,
// schedules, and the PITR flag so all backup-related fields are written
// together, avoiding the reconcile loop that would occur from multiple writes
// to the same engine CR.
func buildBackupSpec(c *controller.Context) (psmdbv1.BackupSpec, error) {
	spec, err := c.ProviderSpec()
	if err != nil {
		return psmdbv1.BackupSpec{}, fmt.Errorf("get provider spec: %w", err)
	}
	bs := psmdbv1.BackupSpec{
		Image: controller.GetDefaultImageForComponent(spec, common.ComponentBackupAgent),
		Configuration: psmdbv1.BackupConfig{
			BackupOptions: &psmdbv1.BackupOptions{
				Timeouts: &psmdbv1.BackupTimeouts{Starting: pointer.ToUint32(defaultBackupStartingTimeout)},
			},
		},
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("1G"),
				corev1.ResourceCPU:    resource.MustParse("300m"),
			},
		},
	}

	backupCfg := c.Instance().Spec.Backup
	if backupCfg == nil || !backupCfg.Enabled {
		return bs, nil
	}

	storages, err := buildPSMDBStorages(c, backupCfg.Storages)
	if err != nil {
		return psmdbv1.BackupSpec{}, &controller.BackupConfigError{
			Reason:  "StorageResolutionFailed",
			Message: err.Error(),
		}
	}
	pitrEnabled, err := resolvePSMDBPITR(backupCfg.Storages)
	if err != nil {
		return psmdbv1.BackupSpec{}, &controller.BackupConfigError{
			Reason:  "PITRConfigInvalid",
			Message: err.Error(),
		}
	}
	bs.Enabled = true
	bs.Storages = storages
	bs.Tasks = buildPSMDBTasks(backupCfg.Storages)
	bs.PITR.Enabled = pitrEnabled
	return bs, nil
}

// resolvePSMDBPITR returns true when exactly one storage has PITR enabled.
// PSMDB only supports a single PITR stream per cluster, so configuring more
// than one PITR-enabled storage is rejected as a configuration error.
func resolvePSMDBPITR(storages []corev1alpha1.InstanceBackupStorage) (bool, error) {
	var enabled []string
	for _, s := range storages {
		if s.PITR != nil && s.PITR.Enabled {
			enabled = append(enabled, s.Name)
		}
	}
	if len(enabled) > 1 {
		return false, fmt.Errorf("PSMDB supports at most one PITR-enabled storage, got %d: %v", len(enabled), enabled)
	}
	return len(enabled) == 1, nil
}

// =============================================================================
// BACKUPPROVIDER — runtime hooks for ProviderManaged BackupClasses
// =============================================================================

// buildPSMDBTasks flattens the per-storage schedule lists into the PSMDB
// operator's BackupTaskSpec list. Each task's StorageName is taken from the
// parent storage entry. Disabled schedules are still recorded (the operator
// honors the Enabled flag) so that toggling a schedule off does not lose its
// definition. Retention=0 means "keep all".
func buildPSMDBTasks(storages []corev1alpha1.InstanceBackupStorage) []psmdbv1.BackupTaskSpec {
	var out []psmdbv1.BackupTaskSpec
	for _, st := range storages {
		for _, s := range st.Schedules {
			task := psmdbv1.BackupTaskSpec{
				Name:        s.Name,
				Enabled:     s.Enabled,
				Schedule:    s.Cron,
				StorageName: st.Name,
			}
			if s.RetentionCopies > 0 {
				task.Retention = &psmdbv1.BackupTaskSpecRetention{
					Type:              psmdbv1.BackupTaskSpecRetentionTypeCount,
					Count:             int(s.RetentionCopies),
					DeleteFromStorage: true,
				}
			}
			out = append(out, task)
		}
	}
	return out
}

// Mirror implements controller.BackupMirror. The runtime invokes Mirror once
// per PerconaServerMongoDBBackup event; returning a non-nil Backup causes the
// runtime to create it (idempotently). For on-demand backups (no task
// annotation) and for operator backups whose parent Instance is missing or
// owned by a different provider, Mirror returns nil to skip.
func (p *PSMDBProvider) Mirror(ctx context.Context, c client.Client, obj client.Object) (*backupv1alpha1.Backup, error) {
	ub, ok := obj.(*psmdbv1.PerconaServerMongoDBBackup)
	if !ok {
		return nil, nil
	}
	taskName := ub.Labels[psmdbBackupAncestorLabel]
	if taskName == "" {
		// On-demand backups originate from a Backup CR; no mirroring needed.
		return nil, nil
	}
	instance := &corev1alpha1.Instance{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: ub.Namespace, Name: ub.Spec.ClusterName}, instance); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get parent Instance %q: %w", ub.Spec.ClusterName, err)
	}
	if instance.Spec.Provider != p.Name() {
		return nil, nil
	}
	if instance.Spec.Backup == nil || instance.Spec.Backup.ClassRef.Name == "" {
		return nil, nil
	}
	return &backupv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ub.Name,
			Namespace: ub.Namespace,
		},
		Spec: backupv1alpha1.BackupSpec{
			InstanceName:    ub.Spec.ClusterName,
			BackupClassName: instance.Spec.Backup.ClassRef.Name,
			StorageName:     ub.Spec.StorageName,
			ScheduleName:    taskName,
		},
	}, nil
}

// OperatorBackupType implements controller.BackupMirror.
func (p *PSMDBProvider) OperatorBackupType() client.Object {
	return &psmdbv1.PerconaServerMongoDBBackup{}
}

// selectMainStorageName returns the logical name of the storage that should be
// designated as the PBM main storage. The PITR-enabled storage is always
// preferred because PSMDB requires PITR to write to the main storage. When no
// storage has PITR enabled the first entry in the slice is used as the
// fallback. An empty slice returns "".
func selectMainStorageName(storages []corev1alpha1.InstanceBackupStorage) string {
	for _, s := range storages {
		if s.PITR != nil && s.PITR.Enabled {
			return s.Name
		}
	}
	if len(storages) > 0 {
		return storages[0].Name
	}
	return ""
}

// buildPSMDBStorages resolves each storage entry on the Instance into a
// psmdbv1.BackupStorageSpec keyed by the storage's logical name. The main
// storage is inferred via selectMainStorageName: the PITR-enabled storage wins;
// the first entry is used as fallback when no storage has PITR enabled.
func buildPSMDBStorages(
	c *controller.Context,
	storages []corev1alpha1.InstanceBackupStorage,
) (map[string]psmdbv1.BackupStorageSpec, error) {
	out := make(map[string]psmdbv1.BackupStorageSpec, len(storages))
	mainName := selectMainStorageName(storages)
	for _, entry := range storages {
		bs, err := c.BackupStorage(entry.StorageRef.Name)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil, controller.WaitFor(fmt.Sprintf(
					"BackupStorage %q not yet present", entry.StorageRef.Name))
			}
			return nil, err
		}
		if bs.Spec.S3 == nil {
			// Skip non-S3 storages; PSMDB only supports S3-compatible backends.
			continue
		}
		s3 := bs.Spec.S3
		out[entry.Name] = psmdbv1.BackupStorageSpec{
			Type: psmdbv1.BackupStorageS3,
			Main: entry.Name == mainName,
			S3: psmdbv1.BackupStorageS3Spec{
				Bucket:                s3.Bucket,
				Region:                s3.Region,
				EndpointURL:           s3.EndpointURL,
				CredentialsSecret:     s3.CredentialsSecretName,
				InsecureSkipTLSVerify: !pointer.Get(s3.VerifyTLS),
			},
		}
	}
	return out, nil
}

// SyncBackup creates or updates a PerconaServerMongoDBBackup that references
// the operator-registered storage matching backup.spec.storageName, then maps
// operator status into the BackupExecutionStatus the runtime expects.
func (p *PSMDBProvider) SyncBackup(c *controller.Context, backup *backupv1alpha1.Backup) (controller.BackupExecutionStatus, error) {
	psmdb := &psmdbv1.PerconaServerMongoDB{}
	if err := c.Get(psmdb, c.Name()); err != nil {
		if apierrors.IsNotFound(err) {
			return controller.BackupExecutionStatus{
				State:   backupv1alpha1.BackupStatePending,
				Message: "Waiting for PerconaServerMongoDB cluster to exist",
			}, nil
		}
		return controller.BackupExecutionStatus{}, fmt.Errorf("get PSMDB: %w", err)
	}

	// The storage name on the PSMDB cluster matches the logical storage name on the
	// Instance and on the Backup CR. Reject up-front if the user pointed at a
	// storage the cluster doesn't know about.
	if _, ok := psmdb.Spec.Backup.Storages[backup.Spec.StorageName]; !ok {
		return controller.BackupExecutionStatus{
			State:   backupv1alpha1.BackupStatePending,
			Message: fmt.Sprintf("Waiting for storage %q to be registered on PSMDB cluster", backup.Spec.StorageName),
		}, nil
	}

	psmdbBackup := &psmdbv1.PerconaServerMongoDBBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      backup.Name,
			Namespace: backup.Namespace,
		},
	}

	if _, err := controllerutil.CreateOrUpdate(c.Context(), c.Client(), psmdbBackup, func() error {
		psmdbBackup.Spec.ClusterName = c.Name()
		psmdbBackup.Spec.StorageName = backup.Spec.StorageName
		controllerutil.AddFinalizer(psmdbBackup, psmdbDeleteBackupFinalizer)
		return controllerutil.SetControllerReference(backup, psmdbBackup, c.Client().Scheme())
	}); err != nil {
		return controller.BackupExecutionStatus{}, fmt.Errorf("create or update PSMDB backup: %w", err)
	}

	exec := controller.BackupExecutionStatus{
		OperatorBackupRef: &corev1.TypedLocalObjectReference{
			APIGroup: pointer.ToString(psmdbv1.SchemeGroupVersion.Group),
			Kind:     "PerconaServerMongoDBBackup",
			Name:     psmdbBackup.Name,
		},
	}
	switch psmdbBackup.Status.State {
	case psmdbv1.BackupStateReady:
		exec.State = backupv1alpha1.BackupStateSucceeded
		now := metav1.Now()
		exec.CompletedAt = &now
	case psmdbv1.BackupStateError:
		exec.State = backupv1alpha1.BackupStateFailed
		exec.Message = psmdbBackup.Status.Error
	case psmdbv1.BackupStateRequested, psmdbv1.BackupStateRunning, psmdbv1.BackupStateWaiting:
		exec.State = backupv1alpha1.BackupStateRunning
	default:
		exec.State = backupv1alpha1.BackupStatePending
	}
	return exec, nil
}

// SyncRestore creates or updates a PerconaServerMongoDBRestore that points at
// the operator backup produced by the source Backup CR.
//
// Two cases are handled:
//
//   - Same-cluster restore (source Backup.spec.instanceName == this Instance):
//     the operator backup lives on the same PSMDB cluster, so we set
//     .spec.backupName to its name.
//   - Cross-cluster restore (e.g. seeding a new Instance from another
//     Instance's Backup via .spec.dataSource): the source operator backup
//     belongs to a different PSMDB CR. The PSMDB operator cannot resolve it
//     by name on this cluster, so we copy its .status into
//     .spec.backupSource and set .spec.storageName to the target Instance's
//     matching storage entry so credentials are taken from the target
//     cluster's registered storages.
func (p *PSMDBProvider) SyncRestore(c *controller.Context, restore *backupv1alpha1.Restore) (controller.RestoreExecutionStatus, error) {
	sourceBackup, exec, err := resolveSourceBackup(c, restore)
	if err != nil {
		return controller.RestoreExecutionStatus{}, err
	}
	if sourceBackup == nil {
		return exec, nil
	}

	operatorBackupName := sourceBackup.Name
	if sourceBackup.Status.OperatorBackupRef != nil && sourceBackup.Status.OperatorBackupRef.Name != "" {
		operatorBackupName = sourceBackup.Status.OperatorBackupRef.Name
	}

	// Detect cross-cluster restores. When the source Backup was produced by a
	// different Instance, fetch its PerconaServerMongoDBBackup so we can copy
	// the destination/storage spec into Spec.BackupSource.
	crossCluster := sourceBackup.Spec.InstanceName != c.Name()
	var sourceOpBackup *psmdbv1.PerconaServerMongoDBBackup
	if crossCluster {
		sourceOpBackup = &psmdbv1.PerconaServerMongoDBBackup{}
		if err := c.Get(sourceOpBackup, operatorBackupName); err != nil {
			if apierrors.IsNotFound(err) {
				return controller.RestoreExecutionStatus{
					State:   backupv1alpha1.RestoreStatePending,
					Message: fmt.Sprintf("waiting for source PerconaServerMongoDBBackup %q", operatorBackupName),
				}, nil
			}
			return controller.RestoreExecutionStatus{}, fmt.Errorf("get source PSMDB backup %q: %w", operatorBackupName, err)
		}
		if sourceOpBackup.Status.Destination == "" {
			return controller.RestoreExecutionStatus{
				State:   backupv1alpha1.RestoreStatePending,
				Message: fmt.Sprintf("source PerconaServerMongoDBBackup %q has no destination yet", operatorBackupName),
			}, nil
		}
	}

	psmdbRestore := &psmdbv1.PerconaServerMongoDBRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      restore.Name,
			Namespace: restore.Namespace,
		},
	}

	if _, err := controllerutil.CreateOrUpdate(c.Context(), c.Client(), psmdbRestore, func() error {
		psmdbRestore.Spec.ClusterName = c.Name()
		if crossCluster {
			// Copy the source operator backup's storage descriptor verbatim
			// (Destination + S3/Azure/GCS/Minio/Filesystem spec) so PSMDB can
			// read the dump without consulting its own backup list. The
			// StorageName on the restore selects credentials from this
			// cluster's registered storages, which the runtime guarantees
			// includes a matching entry (validated upstream by
			// ReconcileDataSource).
			src := sourceOpBackup.Status
			psmdbRestore.Spec.BackupSource = &psmdbv1.PerconaServerMongoDBBackupStatus{
				Destination: src.Destination,
				StorageName: sourceBackup.Spec.StorageName,
				S3:          src.S3,
				Azure:       src.Azure,
				GCS:         src.GCS,
				Minio:       src.Minio,
				Filesystem:  src.Filesystem,
				Type:        src.Type,
				PBMname:     src.PBMname,
			}
			psmdbRestore.Spec.StorageName = sourceBackup.Spec.StorageName
			psmdbRestore.Spec.BackupName = ""
		} else {
			psmdbRestore.Spec.BackupName = operatorBackupName
			psmdbRestore.Spec.BackupSource = nil
		}
		// PITR support: the runtime reconciler validates that PITR is only
		// requested when the BackupClass advertises it; we just translate.
		if restore.Spec.DataSource.Backup.PITR != nil {
			psmdbRestore.Spec.PITR = &psmdbv1.PITRestoreSpec{
				Type: psmdbv1.PITRestoreType(restore.Spec.DataSource.Backup.PITR.Type),
			}
			if restore.Spec.DataSource.Backup.PITR.Date != nil {
				psmdbRestore.Spec.PITR.Date = &psmdbv1.PITRestoreDate{Time: *restore.Spec.DataSource.Backup.PITR.Date}
			}
		}
		return controllerutil.SetControllerReference(restore, psmdbRestore, c.Client().Scheme())
	}); err != nil {
		return controller.RestoreExecutionStatus{}, fmt.Errorf("create or update PSMDB restore: %w", err)
	}

	out := controller.RestoreExecutionStatus{
		OperatorRestoreRef: &corev1.TypedLocalObjectReference{
			APIGroup: pointer.ToString(psmdbv1.SchemeGroupVersion.Group),
			Kind:     "PerconaServerMongoDBRestore",
			Name:     psmdbRestore.Name,
		},
	}
	switch psmdbRestore.Status.State {
	case psmdbv1.RestoreStateReady:
		out.State = backupv1alpha1.RestoreStateSucceeded
		now := metav1.Now()
		out.CompletedAt = &now
	case psmdbv1.RestoreStateError:
		out.State = backupv1alpha1.RestoreStateFailed
		out.Message = psmdbRestore.Status.Error
	case psmdbv1.RestoreStateRequested, psmdbv1.RestoreStateRunning, psmdbv1.RestoreStateWaiting:
		out.State = backupv1alpha1.RestoreStateRunning
	default:
		out.State = backupv1alpha1.RestoreStatePending
	}
	return out, nil
}

// resolveSourceBackup fetches the Backup CR referenced by the Restore's
// DataSource. Returns (nil, exec, nil) when a terminal exec status should be
// reported (e.g. missing data source field) and (backup, _, nil) when the
// source Backup is in scope.
func resolveSourceBackup(
	c *controller.Context,
	restore *backupv1alpha1.Restore,
) (*backupv1alpha1.Backup, controller.RestoreExecutionStatus, error) {
	if restore.Spec.DataSource.Backup == nil || restore.Spec.DataSource.Backup.BackupName == "" {
		return nil, controller.RestoreExecutionStatus{
			State:   backupv1alpha1.RestoreStateFailed,
			Message: "restore.spec.dataSource.backup is not set",
		}, nil
	}
	backupName := restore.Spec.DataSource.Backup.BackupName
	backup := &backupv1alpha1.Backup{}
	if err := c.Get(backup, backupName); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, controller.RestoreExecutionStatus{
				State:   backupv1alpha1.RestoreStateFailed,
				Message: fmt.Sprintf("source Backup %q not found", backupName),
			}, nil
		}
		return nil, controller.RestoreExecutionStatus{}, fmt.Errorf("get source Backup: %w", err)
	}
	return backup, controller.RestoreExecutionStatus{}, nil
}

// CleanupBackup removes the operator PerconaServerMongoDBBackup, honoring the
// Backup's DeletionPolicy:
//
//   - Delete (default): leave the percona.com/delete-backup finalizer in
//     place and issue a Delete on the PerconaServerMongoDBBackup. The PSMDB
//     operator will run PBM's delete-backup against the storage to purge
//     the underlying object, then release its own finalizer.
//   - Retain: strip the percona.com/delete-backup finalizer first so PSMDB
//     does NOT touch the storage, then delete the CR. The S3 object is
//     left in place for out-of-band recovery or manual cleanup.
//
// In both cases CleanupBackup returns done=true only once the
// PerconaServerMongoDBBackup has fully gone away.
func (p *PSMDBProvider) CleanupBackup(c *controller.Context, backup *backupv1alpha1.Backup) (bool, error) {
	psmdbBackup := &psmdbv1.PerconaServerMongoDBBackup{}
	err := c.Get(psmdbBackup, backup.Name)
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("get PSMDB backup for cleanup: %w", err)
	}

	if backup.Spec.DeletionPolicy == backupv1alpha1.BackupDeletionPolicyRetain {
		// Strip the storage-protection finalizer BEFORE deletion so the
		// PSMDB operator skips its delete-backup hook and the underlying
		// S3 object is preserved.
		if controllerutil.RemoveFinalizer(psmdbBackup, psmdbDeleteBackupFinalizer) {
			if err := c.Client().Update(c.Context(), psmdbBackup); err != nil {
				return false, fmt.Errorf("remove finalizer from PSMDB backup: %w", err)
			}
		}
	}

	if psmdbBackup.DeletionTimestamp.IsZero() {
		if err := c.Delete(psmdbBackup); err != nil {
			return false, fmt.Errorf("delete PSMDB backup: %w", err)
		}
	}
	// For the Delete path, requeue until PSMDB has run its delete-backup
	// finalizer and the CR is gone. For the Retain path, the CR will
	// disappear as soon as our finalizer-strip update commits.
	return false, nil
}

// CleanupRestore deletes the PerconaServerMongoDBRestore. The restore CR is
// run-to-completion and carries no protective finalizer, so a single delete
// is sufficient.
func (p *PSMDBProvider) CleanupRestore(c *controller.Context, restore *backupv1alpha1.Restore) (bool, error) {
	psmdbRestore := &psmdbv1.PerconaServerMongoDBRestore{}
	err := c.Get(psmdbRestore, restore.Name)
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("get PSMDB restore for cleanup: %w", err)
	}
	if psmdbRestore.DeletionTimestamp.IsZero() {
		if err := c.Delete(psmdbRestore); err != nil {
			return false, fmt.Errorf("delete PSMDB restore: %w", err)
		}
	}
	return false, nil
}
