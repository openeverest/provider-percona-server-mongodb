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

// Package replicaset contains custom spec types for the replica set topology.
//
// Add fields to ReplicaSetTopologyConfig and reference it via configSchema in
// topology.yaml when this topology needs custom configuration.
//
// +k8s:openapi-gen=true
package replicaset

// ReplicaSetTopologyConfig defines configuration for replica set topology.
// Currently empty — add fields here when the replica set topology needs
// custom configuration beyond what the base Instance spec provides.
type ReplicaSetTopologyConfig struct{}
