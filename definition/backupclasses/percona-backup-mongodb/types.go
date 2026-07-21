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
