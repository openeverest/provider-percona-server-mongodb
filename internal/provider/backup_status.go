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
	"sort"

	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
)

// Reasons published on InstanceBackupStoragePITRStatus.
const (
	pitrReasonWindowAvailable = "WindowAvailable"
	pitrReasonNoBackups       = "NoRestorableBackup"
)

var _ controller.InstanceBackupStatusReporter = (*PSMDBProvider)(nil)

// BackupStorageStatuses publishes the point-in-time recovery window observed on
// each PITR-enabled storage of the Instance.
func (p *PSMDBProvider) BackupStorageStatuses(c *controller.Context) ([]corev1alpha1.InstanceBackupStorageStatus, error) {
	backupCfg := c.Instance().Spec.Backup
	if backupCfg == nil || !backupCfg.Enabled {
		return nil, nil
	}

	list := &psmdbv1.PerconaServerMongoDBBackupList{}
	if err := c.List(list); err != nil {
		return nil, fmt.Errorf("list PSMDB backups: %w", err)
	}

	out := make([]corev1alpha1.InstanceBackupStorageStatus, 0, len(backupCfg.Storages))
	for _, s := range backupCfg.Storages {
		entry := corev1alpha1.InstanceBackupStorageStatus{Name: s.StorageRef.Name}
		if s.PITR != nil && s.PITR.Enabled {
			entry.PITR = pitrWindow(collectStorageBackups(list.Items, c.Name(), s.StorageRef.Name))
		}
		out = append(out, entry)
	}
	return out, nil
}

// enqueueOperatorBackupInstance maps a PerconaServerMongoDBBackup event to a
// reconcile request for the Instance named by spec.clusterName, so that status
// PBM stamps on operator backups reaches instance.status.backup.storages.
func enqueueOperatorBackupInstance() func(ctx context.Context, obj client.Object) []reconcile.Request {
	return func(_ context.Context, obj client.Object) []reconcile.Request {
		ub, ok := obj.(*psmdbv1.PerconaServerMongoDBBackup)
		if !ok || ub.Spec.ClusterName == "" {
			return nil
		}
		return []reconcile.Request{{
			NamespacedName: types.NamespacedName{
				Namespace: ub.Namespace,
				Name:      ub.Spec.ClusterName,
			},
		}}
	}
}

// collectStorageBackups returns the ready backups of one cluster on one
// storage, oldest first.
func collectStorageBackups(all []psmdbv1.PerconaServerMongoDBBackup, cluster, storage string) []psmdbv1.PerconaServerMongoDBBackup {
	var out []psmdbv1.PerconaServerMongoDBBackup
	for i := range all {
		b := all[i]
		storageName := b.Spec.StorageName
		if storageName == "" {
			storageName = b.Status.StorageName
		}
		if b.Spec.ClusterName != cluster ||
			storageName != storage ||
			b.Status.State != psmdbv1.BackupStateReady ||
			b.Status.CompletedAt == nil {
			continue
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Status.CompletedAt.Before(out[j].Status.CompletedAt)
	})
	return out
}

// pitrWindow derives the recovery window from the operator's backups.
//
// PSMDB does not report a window. PBM computes an oplog timeline with both a
// start and an end, but the operator persists only the end -- as
// status.latestRestorableTime on the newest backup -- and discards the start
// (see K8SPSMDB: updateLatestRestorableTime stores tl.End only). No gap signal
// is exposed on the CRD surface either, so the window is derived as:
//
//	earliest = completedAt of the oldest ready backup on the storage
//	latest   = newest latestRestorableTime published on the storage
//
// The latest is taken as the maximum across backups rather than off the newest
// backup only: PBM refreshes the newest backup, so older values are stale-lower
// and the maximum is equivalent, while staying correct in the interval before a
// brand-new backup has been stamped.
func pitrWindow(backups []psmdbv1.PerconaServerMongoDBBackup) *corev1alpha1.InstanceBackupStoragePITRStatus {
	if len(backups) == 0 {
		return &corev1alpha1.InstanceBackupStoragePITRStatus{
			State:   corev1alpha1.PITRStateUnavailable,
			Reason:  pitrReasonNoBackups,
			Message: "No ready backup on this storage yet",
		}
	}

	var latest *metav1.Time
	for i := range backups {
		if t := backups[i].Status.LatestRestorableTime; t != nil && (latest == nil || t.After(latest.Time)) {
			latest = t
		}
	}

	// PBM has not published an end for the oplog stream yet, so nothing is
	// restorable by time even though a base exists.
	if latest == nil {
		return &corev1alpha1.InstanceBackupStoragePITRStatus{
			State:   corev1alpha1.PITRStateUnavailable,
			Reason:  pitrReasonNoBackups,
			Message: "Waiting for PBM to report a restorable time",
		}
	}

	return &corev1alpha1.InstanceBackupStoragePITRStatus{
		EarliestRestorableTime: backups[0].Status.CompletedAt,
		LatestRestorableTime:   latest,
		State:                  corev1alpha1.PITRStateAvailable,
		Reason:                 pitrReasonWindowAvailable,
	}
}
