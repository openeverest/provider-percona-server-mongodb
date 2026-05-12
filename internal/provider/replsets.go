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

package provider

import (
	"fmt"

	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mongodb/definition/topologies/sharded"
	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
)

// configureReplset configures a single replset based on the provided parameters.
func configureReplset(name string, replicas *int32, resources *corev1.ResourceRequirements, storageSize *corev1alpha1.Storage, expose bool) *psmdbv1.ReplsetSpec {
	rsSpec := &psmdbv1.ReplsetSpec{
		Name:          name,
		Configuration: psmdbv1.MongoConfiguration(psmdbDefaultConfigurationTemplate),
		MultiAZ: psmdbv1.MultiAZ{
			PodDisruptionBudget: &psmdbv1.PodDisruptionBudgetSpec{
				MaxUnavailable: &maxUnavailable,
			},
			Resources: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{},
			},
		},
		Size: 3,
		VolumeSpec: &psmdbv1.VolumeSpec{
			PersistentVolumeClaim: psmdbv1.PVCSpec{
				PersistentVolumeClaimSpec: &corev1.PersistentVolumeClaimSpec{
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							// TODO: set storage size
							corev1.ResourceStorage: resource.MustParse("10Gi"),
						},
					},
				},
			},
		},
		Expose: psmdbv1.ExposeTogglable{
			Enabled: expose,
			// TODO: implement exposing replset
			Expose: psmdbv1.Expose{
				ExposeType:         corev1.ServiceTypeClusterIP,
				ServiceAnnotations: map[string]string{},
			},
		},
	}

	if replicas != nil {
		rsSpec.Size = *replicas
	}

	if resources != nil && resources.Limits != nil {
		if !resources.Limits.Cpu().IsZero() {
			rsSpec.Resources.Limits[corev1.ResourceCPU] = *resources.Limits.Cpu()
		}
		if !resources.Limits.Memory().IsZero() {
			rsSpec.Resources.Limits[corev1.ResourceMemory] = *resources.Limits.Memory()
		}
	}

	if storageSize != nil && !storageSize.Size.IsZero() {
		rsSpec.VolumeSpec.PersistentVolumeClaim.Resources.Requests[corev1.ResourceStorage] = storageSize.Size
	}

	return rsSpec
}

// rsName generates a replset name based on the index (e.g., rs0, rs1, etc.)
func rsName(i int) string {
	return fmt.Sprintf("rs%v", i)
}

// configureReplsets configures the replsets based on the topology and component specs.
func configureReplsets(c *controller.Context) []*psmdbv1.ReplsetSpec {
	var replsets []*psmdbv1.ReplsetSpec

	in := c.Instance()
	spec := in.Spec
	engine := spec.Components[common.ComponentEngine]

	// TODO: implement disabling
	if spec.Topology == nil || spec.Topology.Type != "sharded" {
		return []*psmdbv1.ReplsetSpec{
			configureReplset(rsName(0), engine.Replicas, engine.Resources, engine.Storage, true),
		}
	}

	numShards := 2 // default
	var shardedConfig sharded.ShardedTopologyConfig
	if c.TryDecodeTopologyConfig(&shardedConfig) && shardedConfig.NumShards > 0 {
		numShards = int(shardedConfig.NumShards)
	}

	// Create replsets for each shard
	for i := 0; i < numShards; i++ {
		replsets = append(replsets, configureReplset(rsName(i), engine.Replicas, engine.Resources, engine.Storage, false))
	}

	return replsets
}

// configureConfigServerReplset configures the config server replset for sharded clusters.
func configureConfigServerReplset(c *controller.Context) *psmdbv1.ReplsetSpec {
	var replset *psmdbv1.ReplsetSpec

	in := c.Instance()
	spec := in.Spec
	cfgSrv := spec.Components[common.ComponentConfigServer]

	// TODO: implement disabling
	if spec.Topology == nil || spec.Topology.Type != "sharded" {
		return replset
	}

	// TODO: check if this is okay. It adds the configuration, expose.type,
	// name, podDisruptionBudget that we didn't have in the everest operator
	return configureReplset("configsvr", cfgSrv.Replicas, cfgSrv.Resources, cfgSrv.Storage, false)
}
