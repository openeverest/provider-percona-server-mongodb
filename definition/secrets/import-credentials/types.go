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

// Package importcredentials contains the schema-bearing Go types for the
// "import-credentials" secret type. Each struct here is converted to an
// OpenAPI v3 schema by `provider-sdk generate` and inlined into the generated
// provider spec.
//
// +k8s:openapi-gen=true
package importcredentials

// ImportCredentialsSecretData describes the expected data keys for the
// database credentials secret. Because PBM backups embed password
// hashes, an external data import must reuse the source cluster's per-user
// credentials. The keys mirror the standard PSMDB users secret so the
// provider can hand this secret directly to the engine.
type ImportCredentialsSecretData struct {
	// MongodbBackupUser is the backup user's username.
	MongodbBackupUser string `json:"MONGODB_BACKUP_USER"`
	// MongodbBackupPassword is the backup user's password.
	MongodbBackupPassword string `json:"MONGODB_BACKUP_PASSWORD"`

	// MongodbClusterAdminUser is the cluster admin user's username.
	MongodbClusterAdminUser string `json:"MONGODB_CLUSTER_ADMIN_USER"`
	// MongodbClusterAdminPassword is the cluster admin user's password.
	MongodbClusterAdminPassword string `json:"MONGODB_CLUSTER_ADMIN_PASSWORD"`

	// MongodbClusterMonitorUser is the cluster monitor user's username.
	MongodbClusterMonitorUser string `json:"MONGODB_CLUSTER_MONITOR_USER"`
	// MongodbClusterMonitorPassword is the cluster monitor user's password.
	MongodbClusterMonitorPassword string `json:"MONGODB_CLUSTER_MONITOR_PASSWORD"`

	// MongodbDatabaseAdminUser is the database admin user's username.
	MongodbDatabaseAdminUser string `json:"MONGODB_DATABASE_ADMIN_USER"`
	// MongodbDatabaseAdminPassword is the database admin user's password.
	MongodbDatabaseAdminPassword string `json:"MONGODB_DATABASE_ADMIN_PASSWORD"`

	// MongodbUserAdminUser is the user admin user's username.
	MongodbUserAdminUser string `json:"MONGODB_USER_ADMIN_USER"`
	// MongodbUserAdminPassword is the user admin user's password.
	MongodbUserAdminPassword string `json:"MONGODB_USER_ADMIN_PASSWORD"`
}
