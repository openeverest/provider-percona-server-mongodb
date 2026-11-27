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

// Package components contains parameters types for provider component types.
//
// Each struct here corresponds to a component type defined in versions.yaml
// and is converted to an OpenAPI schema during generation.
// Add fields when a component type needs structured parameters beyond
// what the base Instance spec provides.
//
// +k8s:openapi-gen=true
package components

// MongodParameters defines structured parameters for mongod components.
type MongodParameters struct {
	// Configuration is the mongod configuration file content (YAML).
	// `configuration` is the conventional property name for the engine
	// configuration file inside a component's parameters (mirrors the
	// upstream Percona `configuration` field).
	Configuration string `json:"configuration,omitempty"`
}

// MongosParameters defines structured parameters for mongos (proxy) components.
type MongosParameters struct {
	// Configuration is the mongos configuration file content (YAML).
	// `configuration` is the conventional property name for the engine
	// configuration file inside a component's parameters (mirrors the
	// upstream Percona `configuration` field).
	Configuration string `json:"configuration,omitempty"`
}

// BackupParameters defines structured parameters for backup agents. These
// are advanced PBM settings for this Instance's PBM deployment
// (spec.components.backupAgent.parameters): they take effect for every
// backup or restore this Instance runs, unlike the per-schedule/per-run parameters on
// Backup/Restore CRs and InstanceBackupSchedule entries (see the
// percona-backup-mongodb BackupClass).
type BackupParameters struct {
	// NumParallelCollections sets how many collections PBM backs up
	// concurrently (PBM's `backup.numParallelCollections` setting).
	// Unset uses PBM's default, half the backup-agent's visible CPU count
	// (minimum 1). Only takes effect for logical backups (Backup type
	// "logical"/"incremental"); PBM ignores it for "physical" and
	// "external" backups, which do not dump collections individually.
	// +kubebuilder:validation:Minimum=1
	// +optional
	NumParallelCollections *int32 `json:"numParallelCollections,omitempty"`
	// RestoreNumParallelCollections sets how many collections PBM restores
	// concurrently (PBM's `restore.numParallelCollections` setting).
	// Unset uses PBM's default, half the backup-agent's visible CPU count
	// (minimum 1). Only takes effect when restoring from a logical backup;
	// PBM ignores it when restoring a physical backup.
	// +kubebuilder:validation:Minimum=1
	// +optional
	RestoreNumParallelCollections *int32 `json:"restoreNumParallelCollections,omitempty"`

	// BackupOplogSpanMin sets, in minutes, the interval at which PBM slices
	// the oplog while a logical backup is running (PBM's
	// `backup.oplogSpanMin` setting). This is distinct from the
	// per-storage PITR oplog span (storages[].pitr.parameters.oplogSpanMin,
	// PBM's `pitr.oplogSpanMin`): when unset, PBM falls back to the
	// effective PITR span (or its own 10-minute default when PITR is not
	// configured either), so setting PITR's span without this one also
	// affects the backup slicer. Only takes effect for logical backups.
	// +kubebuilder:validation:Minimum=0
	// +optional
	BackupOplogSpanMin *float64 `json:"backupOplogSpanMin,omitempty"`

	// Priority maps a replica set member's host:port (or "*" for any member)
	// to a priority score PBM uses to pick which node runs a logical backup
	// (PBM's `backup.priority` setting). Higher values are
	// preferred; a value of 0 excludes a node from being selected. Unset
	// uses PBM's default selection (secondaries preferred over the
	// primary).
	// +optional
	Priority map[string]float64 `json:"priority,omitempty"`

	// RestoreBatchSize sets the batch size (number of documents) PBM
	// inserts at a time during a logical restore (PBM's
	// `restore.batchSize` setting). Unset uses PBM's default. Only
	// takes effect when restoring from a logical backup.
	// +kubebuilder:validation:Minimum=1
	// +optional
	RestoreBatchSize *int32 `json:"restoreBatchSize,omitempty"`

	// RestoreNumInsertionWorkers sets how many parallel workers insert
	// documents during a logical restore (PBM's
	// `restore.numInsertionWorkers` setting). Unset uses PBM's
	// default. Only takes effect when restoring from a logical backup.
	// +kubebuilder:validation:Minimum=1
	// +optional
	RestoreNumInsertionWorkers *int32 `json:"restoreNumInsertionWorkers,omitempty"`

	// RestoreNumDownloadWorkers sets how many parallel workers download
	// backup data during a restore (PBM's `restore.numDownloadWorkers`
	// setting). Unset uses PBM's default.
	// +kubebuilder:validation:Minimum=1
	// +optional
	RestoreNumDownloadWorkers *int32 `json:"restoreNumDownloadWorkers,omitempty"`

	// RestoreMaxDownloadBufferMB caps, in MiB, the in-memory download
	// buffer PBM uses while restoring (PBM's
	// `restore.maxDownloadBufferMb` setting). Unset uses PBM's
	// default.
	// +kubebuilder:validation:Minimum=1
	// +optional
	RestoreMaxDownloadBufferMB *int32 `json:"restoreMaxDownloadBufferMb,omitempty"`

	// RestoreDownloadChunkMB sets, in MiB, the chunk size PBM downloads at a
	// time while restoring (PBM's `restore.downloadChunkMb`
	// setting). Unset uses PBM's default.
	// +kubebuilder:validation:Minimum=1
	// +optional
	RestoreDownloadChunkMB *int32 `json:"restoreDownloadChunkMb,omitempty"`
}
