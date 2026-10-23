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

// MonitoringParameters defines structured parameters for the monitoring
// component. Each monitoring integration is configured independently; an
// integration left unset is not deployed.
type MonitoringParameters struct {
	// MonitoringConfigName specifies the name of the MonitoringConfig resource
	// of the PMM server to send metrics to.
	// If not specified, PMM is not configured.
	MonitoringConfigName *string `json:"monitoringConfigName,omitempty"`

	// Metrics exposes MongoDB metrics in the Prometheus format through a
	// mongodb_exporter sidecar next to every mongod and mongos.
	// If not specified, no exporter runs.
	Metrics *MetricsParameters `json:"metrics,omitempty"`
}

// MetricsParameters configures which monitoring systems scrape the exposed
// metrics.
type MetricsParameters struct {
	// Prometheus configures scraping by the Prometheus Operator.
	Prometheus *ScrapeParameters `json:"prometheus,omitempty"`

	// VictoriaMetrics configures scraping by the VictoriaMetrics Operator.
	VictoriaMetrics *ScrapeParameters `json:"victoriaMetrics,omitempty"`
}

// ScrapeParameters configures scraping of the exposed metrics by one
// monitoring system.
type ScrapeParameters struct {
	// Enabled creates the monitoring system's scrape resource for the
	// instance: a PodMonitor for Prometheus, a VMPodScrape for VictoriaMetrics.
	Enabled bool `json:"enabled,omitempty"`
}

// BackupParameters defines structured parameters for backup agents.
type BackupParameters struct{}
