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

// Package perconabackupmongodb contains the schema-bearing Go types
// for the "percona-backup-mongodb" BackupClass. Each struct here is
// converted to an OpenAPI v3 schema by `provider-sdk generate` and inlined
// into the generated BackupClass manifest.
//
// +k8s:openapi-gen=true
package perconabackupmongodb

// PerconaBackupParameters describes the parameters accepted by Backup CRs
// and InstanceBackupSchedule entries that target this class
// (Backup.spec.parameters / InstanceBackupSchedule.parameters). Mirrors the
// fields surfaced by ui.yaml's `backup` section. Applied by the provider in
// buildPSMDBTasks (scheduled backups) and SyncBackup (on-demand backups).
type PerconaBackupParameters struct {
	// Type selects between logical, physical and external (volume-snapshot)
	// PBM backups.
	// +kubebuilder:validation:Enum=logical;physical;external
	Type string `json:"type,omitempty"`
	// CompressionType selects the PBM compression algorithm.
	// +kubebuilder:validation:Enum=none;gzip;snappy;lz4;zstd
	CompressionType string `json:"compressionType,omitempty"`
	// CompressionLevel is the algorithm-specific compression level.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=22
	CompressionLevel *int32 `json:"compressionLevel,omitempty"`
	// VolumeSnapshotClass names the VolumeSnapshotClass used to take the
	// snapshot. Only meaningful when Type is "external"; ignored otherwise.
	// +optional
	VolumeSnapshotClass string `json:"volumeSnapshotClass,omitempty"`
}

// PerconaPITRParameters describes the per-storage point-in-time recovery
// tuning accepted via InstanceBackupStoragePITR.parameters, validated against
// this BackupClass's .spec.providerManaged.pitrParametersSchema. Applied by
// the provider in buildPSMDBPITRSpec.
type PerconaPITRParameters struct {
	// OplogSpanMin is the size, in minutes, of the oplog slices PBM buffers
	// before uploading them to storage. Accepts a decimal number (e.g. "10"
	// or "2.5"). Unset uses the PBM default.
	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]+)?$`
	// +optional
	OplogSpanMin string `json:"oplogSpanMin,omitempty"`
	// OplogOnly restricts PITR to streaming the oplog without periodic base
	// snapshots. Requires an existing full backup to be meaningful.
	// +optional
	OplogOnly bool `json:"oplogOnly,omitempty"`
	// CompressionType selects the PBM compression algorithm for oplog chunks.
	// +kubebuilder:validation:Enum=none;gzip;snappy;lz4;zstd
	// +optional
	CompressionType string `json:"compressionType,omitempty"`
	// CompressionLevel is the algorithm-specific compression level.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=22
	// +optional
	CompressionLevel *int32 `json:"compressionLevel,omitempty"`
}

// PerconaRestoreParameters describes the parameters accepted by Restore CRs
// that target this class (spec.parameters). Applied by the provider in
// SyncRestore.
type PerconaRestoreParameters struct {
	// Selective restricts the restore to selected namespaces, optionally
	// remapping them to different names, and controls whether users and
	// roles are restored along with the data.
	// +optional
	Selective *PerconaRestoreSelective `json:"selective,omitempty"`
	// ReplsetRemapping maps source replica set names to the target cluster's
	// replica set names, for restoring into a cluster whose replset names
	// differ from the source (e.g. a renamed or re-sharded cluster). Keys
	// are source replset names, values are target replset names.
	// +optional
	ReplsetRemapping map[string]string `json:"replsetRemapping,omitempty"`
}

// PerconaRestoreSelective configures a selective (partial) restore: only the
// named namespaces are restored, optionally renamed, and users/roles are
// included only when requested.
//
// +kubebuilder:validation:XValidation:rule="(!has(self.nsFrom) && !has(self.nsTo)) || (has(self.nsFrom) && has(self.nsTo))",message="nsFrom and nsTo must be set together"
type PerconaRestoreSelective struct {
	// WithUsersAndRoles restores users and roles along with the selected data.
	// +optional
	WithUsersAndRoles bool `json:"withUsersAndRoles,omitempty"`
	// Namespaces lists the "database.collection" namespaces to restore. When
	// empty, every namespace in the backup is restored.
	// +optional
	Namespaces []string `json:"namespaces,omitempty"`
	// NamespaceFrom is the source namespace to rename during restore. Must
	// be set together with NamespaceTo.
	// +optional
	NamespaceFrom string `json:"nsFrom,omitempty"`
	// NamespaceTo is the namespace the source namespace is renamed to.
	// +optional
	NamespaceTo string `json:"nsTo,omitempty"`
}
