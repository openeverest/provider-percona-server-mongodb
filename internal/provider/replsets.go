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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mongodb/definition"
	"github.com/openeverest/provider-percona-server-mongodb/definition/components"
	"github.com/openeverest/provider-percona-server-mongodb/definition/topologies/sharded"
	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
)

const (
	// defaultReplsetSize is the number of members of a replica set whose
	// component asks for no replicas.
	defaultReplsetSize int32 = 3
	// defaultNumShards is the number of shards of a sharded cluster whose
	// topology parameters ask for none.
	defaultNumShards int32 = 2
)

// isSharded reports whether the instance asks for the sharded topology.
func isSharded(c *controller.Context) bool {
	return c.Instance().GetTopologyType() == string(definition.TopologyTypeSharded)
}

// numShards is the number of shards a sharded instance asks for.
func numShards(c *controller.Context) int32 {
	var params sharded.ShardedTopologyParameters
	if c.TryDecodeTopologyParameters(&params) && params.NumShards > 0 {
		return params.NumShards
	}
	return defaultNumShards
}

// engineReplicas is the number of members of each replica set holding the
// data: the only one, or each shard's.
func engineReplicas(c *controller.Context) int32 {
	return replsetSize(c.Instance().Spec.Components[common.ComponentEngine].Replicas)
}

// configServerReplicas is the number of members of the config server replica
// set of a sharded instance.
func configServerReplicas(c *controller.Context) int32 {
	return replsetSize(c.Instance().Spec.Components[common.ComponentConfigServer].Replicas)
}

// replsetSize is the number of members of a replica set whose component asks
// for replicas.
func replsetSize(replicas *int32) int32 {
	if replicas == nil {
		return defaultReplsetSize
	}
	return *replicas
}

// configureReplset configures a single replset based on the provided parameters.
func configureReplset(name string, size int32, resources *corev1.ResourceRequirements, storageSize *corev1alpha1.Storage, expose bool, config string, service *corev1alpha1.Service) *psmdbv1.ReplsetSpec {
	configuration := psmdbDefaultConfigurationTemplate
	if config != "" {
		configuration = config
	}

	rsSpec := &psmdbv1.ReplsetSpec{
		Name:          name,
		Configuration: psmdbv1.MongoConfiguration(configuration),
		MultiAZ: psmdbv1.MultiAZ{
			PodDisruptionBudget: &psmdbv1.PodDisruptionBudgetSpec{
				MaxUnavailable: &maxUnavailable,
			},
			Resources: mongodResources(resources),
		},
		Size:       size,
		VolumeSpec: mongodVolume(storageSize),
		Expose: psmdbv1.ExposeTogglable{
			Enabled: expose,
			Expose:  configureExpose(service),
		},
	}

	return rsSpec
}

// mongodResources keeps the CPU and memory of the requested resources.
func mongodResources(resources *corev1.ResourceRequirements) corev1.ResourceRequirements {
	out := corev1.ResourceRequirements{Limits: corev1.ResourceList{}}
	if resources == nil {
		return out
	}
	if resources.Limits != nil {
		if !resources.Limits.Cpu().IsZero() {
			out.Limits[corev1.ResourceCPU] = *resources.Limits.Cpu()
		}
		if !resources.Limits.Memory().IsZero() {
			out.Limits[corev1.ResourceMemory] = *resources.Limits.Memory()
		}
	}
	if resources.Requests != nil {
		out.Requests = corev1.ResourceList{}
		if !resources.Requests.Cpu().IsZero() {
			out.Requests[corev1.ResourceCPU] = *resources.Requests.Cpu()
		}
		if !resources.Requests.Memory().IsZero() {
			out.Requests[corev1.ResourceMemory] = *resources.Requests.Memory()
		}
	}
	return out
}

// mongodVolume builds the data volume of mongod pods, 10Gi on the default
// storage class unless the storage says otherwise.
func mongodVolume(storage *corev1alpha1.Storage) *psmdbv1.VolumeSpec {
	volume := &psmdbv1.VolumeSpec{
		PersistentVolumeClaim: psmdbv1.PVCSpec{
			PersistentVolumeClaimSpec: &corev1.PersistentVolumeClaimSpec{
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("10Gi"),
					},
				},
			},
		},
	}
	if storage == nil {
		return volume
	}
	if !storage.Size.IsZero() {
		volume.PersistentVolumeClaim.Resources.Requests[corev1.ResourceStorage] = storage.Size
	}
	if storage.StorageClass != nil {
		volume.PersistentVolumeClaim.StorageClassName = storage.StorageClass
	}
	return volume
}

// configureExpose configures the Expose configuration from a Service spec.
// If service is nil, returns a default configuration with ClusterIP.
func configureExpose(service *corev1alpha1.Service) psmdbv1.Expose {
	ex := psmdbv1.Expose{
		ExposeType:         corev1.ServiceTypeClusterIP,
		ServiceAnnotations: map[string]string{},
	}

	if service == nil {
		return ex
	}

	ex.ExposeType = service.ServiceType

	if service.Annotations != nil {
		ex.ServiceAnnotations = service.Annotations
	}

	if service.ServiceType == corev1.ServiceTypeLoadBalancer &&
		service.LoadBalancerService != nil &&
		service.LoadBalancerService.SourceRanges != nil {
		ex.LoadBalancerSourceRanges = service.LoadBalancerService.SourceRanges.NormalizedSourceRanges()
	}

	return ex
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

	// The engine configuration file is the conventional `configuration`
	// property inside the component's parameters.
	var engineParams components.MongodParameters
	_ = c.TryDecodeComponentParameters(engine, &engineParams)
	config := engineParams.Configuration

	// TODO: implement disabling
	if !isSharded(c) {
		rs := configureReplset(rsName(0), engineReplicas(c), engine.Resources, engine.Storage, true, config, engine.Service)
		if err := configureHidden(c, rs); err != nil {
			return nil, err
		}
		return []*psmdbv1.ReplsetSpec{rs}, nil
	}

	// For sharded topology, replsets should not be exposed directly (clients connect via mongos)
	shards := int(numShards(c))
	replsets := make([]*psmdbv1.ReplsetSpec, 0, shards)
	for i := range shards {
		replsets = append(replsets, configureReplset(rsName(i), engineReplicas(c), engine.Resources, engine.Storage, false, config, nil))
	}

	return replsets, nil
}

// configureConfigServerReplset configures the config server replset for sharded clusters.
func configureConfigServerReplset(c *controller.Context) (*psmdbv1.ReplsetSpec, error) {
	// TODO: implement disabling
	if !isSharded(c) {
		return nil, nil
	}

	cfgSrv := c.Instance().Spec.Components[common.ComponentConfigServer]

	// The engine configuration file is the conventional `configuration`
	// property inside the component's parameters.
	var cfgSrvParams components.MongodParameters
	_ = c.TryDecodeComponentParameters(cfgSrv, &cfgSrvParams)
	config := cfgSrvParams.Configuration

	// TODO: check if this is okay. It adds the configuration, expose.type,
	// name, podDisruptionBudget that we didn't have in the everest operator
	// Config servers should never be exposed directly - they are internal infrastructure
	rs := configureReplset("configsvr", configServerReplicas(c), cfgSrv.Resources, cfgSrv.Storage, false, config, nil)
	if err := keepCurrentConfigServerStorageClass(c, rs); err != nil {
		return nil, err
	}

	return rs, nil
}

// keepCurrentConfigServerStorageClass pins the config server storage class to
// the one the running cluster was created with. The operator cannot apply a
// change to it (StatefulSet volumeClaimTemplates are immutable), and instances
// created before the UI started writing the field have none set, so a later
// edit that fills it in must not wedge the operator.
func keepCurrentConfigServerStorageClass(c *controller.Context, rs *psmdbv1.ReplsetSpec) error {
	current := &psmdbv1.PerconaServerMongoDB{}
	if err := c.Get(current, c.Name()); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("fetching current PSMDB CR: %w", err)
	}

	currentCfg := current.Spec.Sharding.ConfigsvrReplSet
	if !current.Spec.Sharding.Enabled || currentCfg == nil || currentCfg.VolumeSpec == nil ||
		currentCfg.VolumeSpec.PersistentVolumeClaim.PersistentVolumeClaimSpec == nil {
		return nil
	}

	rs.VolumeSpec.PersistentVolumeClaim.StorageClassName = currentCfg.VolumeSpec.PersistentVolumeClaim.StorageClassName
	return nil
}
