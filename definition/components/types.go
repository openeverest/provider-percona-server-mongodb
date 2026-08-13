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
// This struct is converted to OpenAPI schema and served via the /schema endpoint.
// Provider users can specify these fields in the Instance's component Parameters.
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

// PMMParameters defines structured parameters for PMM monitoring.
type PMMParameters struct {
	// MonitoringConfigName specifies the name of the MonitoringConfig resource
	// to use for configuring PMM monitoring.
	// If not specified, monitoring will not be configured.
	MonitoringConfigName *string `json:"monitoringConfigName,omitempty"`
}

// BackupParameters defines structured parameters for backup agents.
type BackupParameters struct{}
