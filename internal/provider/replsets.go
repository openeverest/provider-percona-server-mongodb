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
func configureReplset(name string, replicas *int32, resources *corev1.ResourceRequirements, storageSize *corev1alpha1.Storage, componentSpec *corev1alpha1.ComponentSpec, c *controller.Context) *psmdbv1.ReplsetSpec {
	var config string
	var affinity *corev1.Affinity

	// Extract config and affinity from componentSpec if provided
	if componentSpec != nil {
		cfg, err := c.ComponentConfig(*componentSpec)
		if err == nil {
			config = cfg
		}
		affinity = componentSpec.Affinity
	}

	configuration := psmdbDefaultConfigurationTemplate
	if config != "" {
		configuration = config
	}

	// Set default affinity if none is provided
	podAffinity := &psmdbv1.PodAffinity{
		Advanced: &corev1.Affinity{
			PodAntiAffinity: &corev1.PodAntiAffinity{
				PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
					{
						Weight: 1,
						PodAffinityTerm: corev1.PodAffinityTerm{
							TopologyKey: "kubernetes.io/hostname",
						},
					},
				},
			},
		},
	}

	// Override with user-provided affinity if specified
	if affinity != nil {
		podAffinity = &psmdbv1.PodAffinity{
			Advanced: affinity,
		}
	}

	rsSpec := &psmdbv1.ReplsetSpec{
		Name:          name,
		Configuration: psmdbv1.MongoConfiguration(configuration),
		MultiAZ: psmdbv1.MultiAZ{
			PodDisruptionBudget: &psmdbv1.PodDisruptionBudgetSpec{
				MaxUnavailable: &maxUnavailable,
			},
			Resources: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{},
			},
			Affinity: podAffinity,
		},
		Size: 3,
		VolumeSpec: &psmdbv1.VolumeSpec{
			PersistentVolumeClaim: psmdbv1.PVCSpec{
				PersistentVolumeClaimSpec: &corev1.PersistentVolumeClaimSpec{
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("10Gi"),
						},
					},
				},
			},
		},
		Expose: configureReplsetExpose(componentSpec, c),
	}

	if replicas != nil {
		rsSpec.Size = *replicas
	}

	if resources != nil {
		if resources.Limits != nil {
			if !resources.Limits.Cpu().IsZero() {
				rsSpec.Resources.Limits[corev1.ResourceCPU] = *resources.Limits.Cpu()
			}
			if !resources.Limits.Memory().IsZero() {
				rsSpec.Resources.Limits[corev1.ResourceMemory] = *resources.Limits.Memory()
			}
		}
		if resources.Requests != nil {
			rsSpec.Resources.Requests = corev1.ResourceList{}
			if !resources.Requests.Cpu().IsZero() {
				rsSpec.Resources.Requests[corev1.ResourceCPU] = *resources.Requests.Cpu()
			}
			if !resources.Requests.Memory().IsZero() {
				rsSpec.Resources.Requests[corev1.ResourceMemory] = *resources.Requests.Memory()
			}
		}
	}

	if storageSize != nil {
		if !storageSize.Size.IsZero() {
			rsSpec.VolumeSpec.PersistentVolumeClaim.Resources.Requests[corev1.ResourceStorage] = storageSize.Size
		}
		if storageSize.StorageClass != nil {
			rsSpec.VolumeSpec.PersistentVolumeClaim.StorageClassName = storageSize.StorageClass
		}
	}

	return rsSpec
}

// configureExpose configures the common Expose configuration from a Service spec.
// If service is nil, returns a default configuration with ClusterIP.
func configureExpose(service *corev1alpha1.Service) psmdbv1.Expose {
	ex := psmdbv1.Expose{
		ExposeType:         corev1.ServiceTypeClusterIP,
		ServiceAnnotations: map[string]string{},
	}

	if service == nil {
		return ex
	}

	switch service.ServiceType {
	case "LoadBalancer":
		ex.ExposeType = corev1.ServiceTypeLoadBalancer
		if service.LoadBalancerService != nil {
			if len(service.LoadBalancerService.SourceRanges) > 0 {
				ex.LoadBalancerSourceRanges = service.LoadBalancerService.SourceRanges.NormalizedSourceRanges()
			}
			ex.ServiceAnnotations = map[string]string{}
			if service.LoadBalancerService.Annotations != nil {
				ex.ServiceAnnotations = service.LoadBalancerService.Annotations
			}
		}
	case "NodePort":
		ex.ExposeType = corev1.ServiceTypeNodePort
		ex.ServiceAnnotations = map[string]string{}
	case "ClusterIP", "":
		ex.ExposeType = corev1.ServiceTypeClusterIP
		ex.ServiceAnnotations = map[string]string{}
	}

	return ex
}

// configureReplsetExpose configures the expose settings for a replset based on the Service spec.
func configureReplsetExpose(componentSpec *corev1alpha1.ComponentSpec, c *controller.Context) psmdbv1.ExposeTogglable {
	// If no component spec or service configuration is provided, keep it disabled
	if componentSpec == nil || componentSpec.Service == nil {
		return psmdbv1.ExposeTogglable{
			Enabled: false,
			Expose:  configureExpose(nil),
		}
	}

	return psmdbv1.ExposeTogglable{
		Enabled: true,
		Expose:  configureExpose(componentSpec.Service),
	}
}

// rsName generates a replset name based on the index (e.g., rs0, rs1, etc.)
func rsName(i int) string {
	return fmt.Sprintf("rs%v", i)
}

// configureReplsets configures the replsets based on the topology and component specs.
func configureReplsets(c *controller.Context) ([]*psmdbv1.ReplsetSpec, error) {
	in := c.Instance()
	spec := in.Spec
	engine := spec.Components[common.ComponentEngine]

	// TODO: implement disabling
	// For non-sharded (replicaSet) topology, configure a single replset that can be exposed
	if spec.Topology == nil || spec.Topology.Type != "sharded" {
		return []*psmdbv1.ReplsetSpec{
			configureReplset(rsName(0), engine.Replicas, engine.Resources, engine.Storage, &engine, c),
		}, nil
	}

	numShards := 2 // default
	var shardedConfig sharded.ShardedTopologyConfig
	if c.TryDecodeTopologyConfig(&shardedConfig) && shardedConfig.NumShards > 0 {
		numShards = int(shardedConfig.NumShards)
	}

	// For sharded topology, replsets should not be exposed directly (clients connect via mongos)
	// Pass nil for componentSpec to disable exposure
	replsets := make([]*psmdbv1.ReplsetSpec, 0, numShards)
	for i := 0; i < numShards; i++ {
		replsets = append(replsets, configureReplset(rsName(i), engine.Replicas, engine.Resources, engine.Storage, nil, c))
	}

	return replsets, nil
}

// configureConfigServerReplset configures the config server replset for sharded clusters.
func configureConfigServerReplset(c *controller.Context) (*psmdbv1.ReplsetSpec, error) {
	in := c.Instance()
	spec := in.Spec

	// TODO: implement disabling
	if spec.Topology == nil || spec.Topology.Type != "sharded" {
		return nil, nil
	}

	cfgSrv := spec.Components[common.ComponentConfigServer]

	// TODO: check if this is okay. It adds the configuration, expose.type,
	// name, podDisruptionBudget that we didn't have in the everest operator
	// Config servers should never be exposed directly - they are internal infrastructure
	// Pass nil for componentSpec to disable exposure
	return configureReplset("configsvr", cfgSrv.Replicas, cfgSrv.Resources, cfgSrv.Storage, nil, c), nil
}
