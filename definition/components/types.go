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

// Package components contains custom spec types for provider component types.
//
// Each struct here corresponds to a component type defined in versions.yaml
// and is converted to an OpenAPI schema during generation.
// Add fields when a component type needs custom configuration beyond
// what the base Instance spec provides.
//
// +k8s:openapi-gen=true
package components

// MongodCustomSpec defines custom configuration for mongod components.
// This struct is converted to OpenAPI schema and served via the /schema endpoint.
// Provider users can specify these fields in the Instance's component CustomSpec.
type MongodCustomSpec struct{}

// MongosCustomSpec defines custom configuration for mongos (proxy) components.
type MongosCustomSpec struct{}

// PMMCustomSpec defines custom configuration for PMM monitoring.
type PMMCustomSpec struct {
	// MonitoringConfigName specifies the name of the MonitoringConfig resource
	// to use for configuring PMM monitoring.
	// If not specified, monitoring will not be configured.
	MonitoringConfigName *string `json:"monitoringConfigName,omitempty"`
}

// BackupCustomSpec defines custom configuration for backup agents.
type BackupCustomSpec struct{}

// SplitHorizonCustomSpec defines custom configuration for split horizon DNS.
// When configured, the provider generates horizon DNS entries for each pod
// using the pattern: <dbName>-<rsName>-<i>-<namespace>.<domain>
type SplitHorizonCustomSpec struct {
	// UseDefaults when true instructs the controller to read domain and
	// tlsSecretName from the Provider CR topology defaults instead of the
	// inline values below. This enables bulk updates — changing the Provider
	// CR (via helm upgrade) automatically propagates to all instances that
	// have useDefaults set to true.
	UseDefaults bool `json:"useDefaults,omitempty"`

	// Domain is the base domain appended to generate horizon DNS entries.
	// Example: "mycompany.com"
	// Ignored when useDefaults is true.
	Domain string `json:"domain,omitempty"`

	// TLSSecretName is the name of an existing Kubernetes Secret containing TLS certificates
	// for split horizon DNS. The Secret must be of type kubernetes.io/tls and exist
	// in the same namespace as the Instance.
	// Ignored when useDefaults is true.
	TLSSecretName string `json:"tlsSecretName,omitempty"`
}
