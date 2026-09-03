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

// Package user contains the schema-bearing Go types for the
// "user" secret type. Each struct here is converted to an OpenAPI
// v3 schema by `provider-sdk generate` and inlined into the generated
// provider spec.
//
// +k8s:openapi-gen=true
package user

// UserSecretData describes the expected data keys for creating initial users.
type UserSecretData struct {
	MongoDBBackupUser             string `json:"MONGODB_BACKUP_USER"`
	MongoDBBackupPassword         string `json:"MONGODB_BACKUP_PASSWORD"`
	MongoDBClusterAdminUser       string `json:"MONGODB_CLUSTER_ADMIN_USER"`
	MongoDBClusterAdminPassword   string `json:"MONGODB_CLUSTER_ADMIN_PASSWORD"`
	MongoDBClusterMonitorUser     string `json:"MONGODB_CLUSTER_MONITOR_USER"`
	MongoDBClusterMonitorPassword string `json:"MONGODB_CLUSTER_MONITOR_PASSWORD"`
	MongoDBDatabaseAdminUser      string `json:"MONGODB_DATABASE_ADMIN_USER"`
	MongoDBDatabaseAdminPassword  string `json:"MONGODB_DATABASE_ADMIN_PASSWORD"`
	MongoDBUserAdminUser          string `json:"MONGODB_USER_ADMIN_USER"`
	MongoDBUserAdminPassword      string `json:"MONGODB_USER_ADMIN_PASSWORD"`
}
