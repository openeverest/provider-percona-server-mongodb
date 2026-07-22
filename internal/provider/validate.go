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
	"regexp"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mongodb/definition/topologies/sharded"
	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
)

// maxInstanceNameLength limits cluster names to 22 characters to ensure generated
// resource names (with suffixes like -credentials, -backup-<timestamp>, etc.)
// stay under Kubernetes' Pod name 63-character limit.
const maxInstanceNameLength = 22

var (
	// Minimum resource requirements
	minStorage = resource.MustParse("1Gi")
	minCPU     = resource.MustParse("600m") // 600 millicores
	minMemory  = resource.MustParse("512Mi")
)

// rfc1035Regexp validates RFC1035 compatible names.
// https://kubernetes.io/docs/concepts/overview/working-with-objects/names/#rfc-1035-label-names
var rfc1035Regexp = regexp.MustCompile("^[a-z]([-a-z0-9]{0,61}[a-z0-9])?$")

// validateMetadata validates the instance metadata.
func validateMetadata(c *controller.Context) error {
	name := c.Instance().Name
	if name == "" {
		return fmt.Errorf("name cannot be empty")
	}

	if !rfc1035Regexp.MatchString(name) {
		return fmt.Errorf("name %q must be RFC-1035 compliant (lowercase alphanumeric or '-', start/end with letter)", name)
	}

	if len(name) > maxInstanceNameLength {
		return fmt.Errorf("name %q exceeds max length of %d", name, maxInstanceNameLength)
	}

	return nil
}

// validateComponents validates the instance spec components and their configuration.
func validateComponents(c *controller.Context) error {
	spec := c.Instance().Spec

	if err := validateEngine(c); err != nil {
		return fmt.Errorf("engine validation failed: %w", err)
	}

	if spec.Topology != nil && spec.Topology.Type == "sharded" {
		if err := validateShardedTopology(c); err != nil {
			return fmt.Errorf("sharded topology validation failed: %w", err)
		}
	}

	if err := validateMonitoring(c); err != nil {
		return fmt.Errorf("monitoring validation failed: %w", err)
	}

	return nil
}

// validateEngine validates the engine component spec.
func validateEngine(c *controller.Context) error {
	spec := c.Instance().Spec

	engine, ok := spec.Components[common.ComponentEngine]
	if !ok {
		return fmt.Errorf("missing spec.components.engine")
	}

	if engine.Replicas == nil {
		return fmt.Errorf("missing spec.components.engine.replicas")
	}

	if *engine.Replicas < 1 || *engine.Replicas%2 == 0 {
		return fmt.Errorf("engine replicas must be a positive odd number, got %d", *engine.Replicas)
	}

	// Validate storage size.
	if engine.Storage == nil {
		return fmt.Errorf("missing spec.components.engine.storage")
	}

	if engine.Storage.Size.Cmp(minStorage) < 0 {
		return fmt.Errorf("spec.components.engine.storage.size must be >= %s", minStorage.String())
	}

	// Validate resources (CPU and memory).
	if engine.Resources == nil || engine.Resources.Limits == nil {
		return fmt.Errorf("missing spec.components.engine.resources.limits")
	}

	if cpu := engine.Resources.Limits.Cpu(); cpu == nil || cpu.Cmp(minCPU) < 0 {
		return fmt.Errorf("spec.components.engine.resources.limits.cpu must be >= %s", minCPU.String())
	}

	if mem := engine.Resources.Limits.Memory(); mem == nil || mem.Cmp(minMemory) < 0 {
		return fmt.Errorf("spec.components.engine.resources.limits.memory must be >= %s", minMemory.String())
	}

	// Validate engine version is available in the provider spec.
	if engine.Version != "" {
		providerSpec, err := c.ProviderSpec()
		if err != nil {
			return fmt.Errorf("failed to get provider spec: %w", err)
		}

		if img := controller.GetImageForVersion(providerSpec, common.ComponentEngine, engine.Version); img == "" {
			return fmt.Errorf("version %q is not in available engine versions", engine.Version)
		}
	}

	if err := validateService(c, common.ComponentEngine); err != nil {
		return fmt.Errorf("engine service validation failed: %w", err)
	}

	return nil
}

// validateDataSource validates the dataSource spec.
func validateDataSource(c *controller.Context) error {
	ds := c.Instance().Spec.DataSource

	if ds == nil {
		return nil
	}

	switch ds.Type {
	case backupv1alpha1.DataSourceTypeBackup:
		return validateDataSourceBackup(c, ds)
	case backupv1alpha1.DataSourceTypeExternal:
		return validateDataSourceExternal(c, ds)
	default:
		return fmt.Errorf("unsupported dataSource.type: %q", ds.Type)
	}
}

// validateDataSourceBackup validates dataSource when type=Backup.
func validateDataSourceBackup(c *controller.Context, ds *backupv1alpha1.DataSource) error {
	if ds.Backup == nil {
		return fmt.Errorf("missing spec.dataSource.backup")
	}

	if ds.Backup.BackupRef.Name == "" {
		return fmt.Errorf("missing spec.dataSource.backup.backupRef.name")
	}

	return nil
}

// validateDataSourceExternal validates dataSource when type=External.
func validateDataSourceExternal(c *controller.Context, ds *backupv1alpha1.DataSource) error {
	if ds.External == nil {
		return fmt.Errorf("missing spec.dataSource.external")
	}

	ext := ds.External

	if ext.Parameters == nil {
		return fmt.Errorf("missing spec.dataSource.external.parameters")
	}

	// Get resolved BackupClass and BackupStorage from spec.backup
	bc, _, err := getImportBackupRefs(c)
	if err != nil {
		return err
	}

	providerName := c.Instance().Spec.ProviderRef.Name
	if !bc.Spec.SupportedProviders.Has(providerName) {
		return fmt.Errorf("BackupClass %q does not support provider %q", bc.Name, providerName)
	}

	// Validate based on execution mode
	switch bc.Spec.ExecutionMode {
	case backupv1alpha1.BackupExecutionModeProviderManaged:
		if bc.Spec.ProviderManaged == nil || !bc.Spec.ProviderManaged.SupportsImport {
			return fmt.Errorf("BackupClass %q does not support import", bc.Name)
		}
	case backupv1alpha1.BackupExecutionModeJob:
		if bc.Spec.ImportJob == nil {
			return fmt.Errorf("BackupClass %q does not have importJob defined", bc.Name)
		}
	default:
		return fmt.Errorf("BackupClass %q has unsupported executionMode %q for import", bc.Name, bc.Spec.ExecutionMode)
	}

	// Validate config against BackupClass.spec.importConfig schema
	if err := bc.Spec.ImportParametersSchema.Validate(ext.Parameters); err != nil {
		return fmt.Errorf("spec.dataSource.external.parameters validation failed: %w", err)
	}

	return nil
}

// validateShardedTopology validates sharded cluster topology and its components.
func validateShardedTopology(c *controller.Context) error {
	spec := c.Instance().Spec

	// Validate sharded topology parameters.
	var shardedParams sharded.ShardedTopologyParameters
	if c.TryDecodeTopologyParameters(&shardedParams) {
		if shardedParams.NumShards < 1 {
			return fmt.Errorf("spec.topology.parameters.numShards must be >= 1, got %d", shardedParams.NumShards)
		}
	}

	if err := validateConfigServer(c); err != nil {
		return fmt.Errorf("spec.components.configServer validation failed: %w", err)
	}

	// Proxy (mongos) is required for sharded topology.
	_, ok := spec.Components[common.ComponentProxy]
	if !ok {
		return fmt.Errorf("component %q required for sharded topology", common.ComponentProxy)
	}

	if err := validateService(c, common.ComponentProxy); err != nil {
		return fmt.Errorf("service validation failed: %w", err)
	}

	return nil
}

// validateConfigServer validates the configServer component spec.
func validateConfigServer(c *controller.Context) error {
	cfgSrv, ok := c.Instance().Spec.Components[common.ComponentConfigServer]
	if !ok {
		return fmt.Errorf("missing spec.components.configServer")
	}

	if cfgSrv.Replicas == nil {
		return fmt.Errorf("missing spec.components.configServer.replicas")
	}

	replicas := *cfgSrv.Replicas
	if replicas < 1 || replicas%2 == 0 {
		return fmt.Errorf("spec.components.configServer.replicas must be a positive odd number, got %d", replicas)
	}

	// Validate replica count based on engine replicas
	engine := c.Instance().Spec.Components[common.ComponentEngine]
	if engine.Replicas != nil {
		engineReplicas := *engine.Replicas
		// Multi-node setup requires at least 3 replicas for configServer
		if engineReplicas > 1 && replicas < 3 {
			return fmt.Errorf("spec.components.configServer.replicas must be >= 3 for multi-node, got %d", replicas)
		}
	}

	return nil
}

func validateService(c *controller.Context, componentName string) error {
	spec := c.Instance().Spec.Components[componentName]

	if spec.Service == nil {
		return nil
	}

	switch spec.Service.ServiceType {
	case corev1.ServiceTypeClusterIP,
		corev1.ServiceTypeLoadBalancer,
		corev1.ServiceTypeNodePort:
		return nil
	default:
		return fmt.Errorf("spec.components.%s.service.serviceType must be one of ClusterIP, LoadBalancer or NodePort", componentName)
	}
}
