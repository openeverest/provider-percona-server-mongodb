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
	"fmt"

	"github.com/AlekSi/pointer"
	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
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
	bs.Enabled = true
	bs.Storages = storages
	bs.Tasks = buildPSMDBTasks(backupCfg.Schedules)
	bs.PITR.Enabled = backupCfg.PITR != nil && backupCfg.PITR.Enabled
	return bs, nil
}

// =============================================================================
// BACKUPPROVIDER — runtime hooks for ProviderManaged BackupClasses
// =============================================================================

// buildPSMDBTasks translates the Instance's schedule list into the PSMDB
// operator's BackupTaskSpec list. Disabled schedules are still recorded
// (the operator honors the Enabled flag) so that toggling a schedule off
// does not lose its definition. Retention=0 means "keep all".
func buildPSMDBTasks(schedules []corev1alpha1.InstanceBackupSchedule) []psmdbv1.BackupTaskSpec {
	if len(schedules) == 0 {
		return nil
	}
	out := make([]psmdbv1.BackupTaskSpec, 0, len(schedules))
	for _, s := range schedules {
		task := psmdbv1.BackupTaskSpec{
			Name:        s.Name,
			Enabled:     s.Enabled,
			Schedule:    s.Cron,
			StorageName: s.StorageName,
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
	return out
}

// buildPSMDBStorages resolves each storage entry on the Instance into a
// psmdbv1.BackupStorageSpec keyed by the storage's logical name. The first
// entry, or the one explicitly marked Main, is flagged as the PBM main
// storage.
func buildPSMDBStorages(
	c *controller.Context,
	storages []corev1alpha1.InstanceBackupStorage,
) (map[string]psmdbv1.BackupStorageSpec, error) {
	out := make(map[string]psmdbv1.BackupStorageSpec, len(storages))
	mainSet := false
	for i, entry := range storages {
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
		isMain := entry.Main || (i == 0 && !mainSet)
		if isMain {
			mainSet = true
		}
		out[entry.Name] = psmdbv1.BackupStorageSpec{
			Type: psmdbv1.BackupStorageS3,
			Main: isMain,
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
