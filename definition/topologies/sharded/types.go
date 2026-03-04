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

// Package sharded contains custom spec types for the sharded cluster topology.
//
// The ShardedTopologyConfig struct is referenced by topology.yaml via configSchema
// and is converted to an OpenAPI schema during generation.
//
// +k8s:openapi-gen=true
package sharded

// ShardedTopologyConfig defines configuration for sharded cluster topology.
type ShardedTopologyConfig struct {
	// NumShards specifies the initial number of shards.
	// +k8s:validation:minimum=1
	// +default=2
	// +optional
	NumShards int32 `json:"numShards,omitempty"`
}
