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

import (
	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
)

// PerconaBackupParameters describes the parameters accepted by Backup CRs
// that target this class (spec.parameters). Mirrors the fields surfaced by
// ui.yaml's `backup` section.
type PerconaBackupParameters struct {
	// Type selects between logical and physical PBM backups.
	// +kubebuilder:validation:Enum=logical;physical
	Type string `json:"type,omitempty"`
	// CompressionType selects the PBM compression algorithm.
	// +kubebuilder:validation:Enum=none;gzip;snappy;lz4;zstd
	CompressionType string `json:"compressionType,omitempty"`
	// CompressionLevel is the algorithm-specific compression level.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=22
	CompressionLevel *int32 `json:"compressionLevel,omitempty"`
}

// PerconaRestoreParameters describes the parameters accepted by Restore CRs
// that target this class (spec.parameters). PSMDB does not currently expose
// restore-time options through OpenEverest; the struct is intentionally
// empty and ships an empty schema until requirements crystallize.
type PerconaRestoreParameters struct{}

// PerconaImportConfig describes the configuration accepted when an Instance
// is created with spec.dataSource.type=External referencing this BackupClass.
//
// PBM/mongodump backups embed credential hashes. When restoring,
// the target cluster's users secret MUST contain the same credentials as the
// source cluster that created the backup. Mismatched credentials render the
// restored data inaccessible because MongoDB will reject authentication
// attempts with the wrong password hashes.
//
// Users must provide a CredentialsSecretName containing the MongoDB credentials
// from the source database. The provider copies these credentials to the target
// Instance's users secret BEFORE initiating the restore.
type PerconaImportParameters struct {
	// Path is the S3 path (prefix) where the PBM/mongodump backup data resides.
	// This is relative to the bucket root configured in the referenced BackupStorage.
	// Example: "backups/2026-07-15/my-cluster" for a backup at
	// s3://my-bucket/backups/2026-07-15/my-cluster
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Path string `json:"path"`

	// CredentialsSecretRef references a Secret in the Instance's namespace
	// containing the MongoDB credentials from the source database that created
	// the backup being imported.
	//
	// This is REQUIRED because PBM backups embed password hashes. The target
	// Instance must use the same credentials as the source to access the
	// restored data. The provider uses this secret directly as the PSMDB
	// users secret.
	//
	// The Secret must contain the standard PSMDB users secret keys:
	//   - MONGODB_BACKUP_USER / MONGODB_BACKUP_PASSWORD
	//   - MONGODB_CLUSTER_ADMIN_USER / MONGODB_CLUSTER_ADMIN_PASSWORD
	//   - MONGODB_CLUSTER_MONITOR_USER / MONGODB_CLUSTER_MONITOR_PASSWORD
	//   - MONGODB_DATABASE_ADMIN_USER / MONGODB_DATABASE_ADMIN_PASSWORD
	//   - MONGODB_USER_ADMIN_USER / MONGODB_USER_ADMIN_PASSWORD
	//
	// +kubebuilder:validation:Required
	CredentialsSecretRef commonv1alpha1.ObjectRef `json:"credentialsSecretRef"`
}
