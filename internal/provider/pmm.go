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
	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
)

const (
	// Kibibyte represents 1 KiB.
	Kibibyte = 1024
	// Mebibyte represents 1 MiB.
	Mebibyte = 1024 * Kibibyte
	// pmmCPUSmall is the default CPU requests for PMM client in small clusters.
	pmmCPUSmall = 95
	// pmmCPUMedium is the default CPU requests for PMM client in medium clusters.
	pmmCPUMedium = 228
	// pmmCPULarge is the default CPU requests for PMM client in large clusters.
	pmmCPULarge = 228
)

// Prefefined database engine sizes based on memory.
var (
	memoryMediumSize = resource.MustParse("8G")
	memoryLargeSize  = resource.MustParse("32G")
)

var (
	// NOTE: provided below values were taken from the tool
	// https://github.com/Tusamarco/mysqloperatorcalculator.

	// pmmResourcesSmall is the resource requirements
	// for PMM for small clusters.
	pmmResourcesSmall = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			// 97.27Mi = 97 MiB + 276 KiB = 99604 KiB
			corev1.ResourceMemory: *resource.NewQuantity(
				97*Mebibyte+276*Kibibyte,
				resource.BinarySI,
			),
			corev1.ResourceCPU: *resource.NewScaledQuantity(
				pmmCPUSmall,
				resource.Milli,
			),
		},
	}

	// pmmResourcesMedium is the resource requirements
	// for PMM for medium clusters.
	pmmResourcesMedium = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			// 194.5Mi = 194 MiB + 512 KiB = 199168 KiB
			corev1.ResourceMemory: *resource.NewQuantity(
				194*Mebibyte+512*Kibibyte,
				resource.BinarySI,
			),
			corev1.ResourceCPU: *resource.NewScaledQuantity(
				pmmCPUMedium,
				resource.Milli,
			),
		},
	}

	// pmmResourcesLarge is the resource requirements
	// for PMM for large clusters.
	pmmResourcesLarge = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			// 778.23Mi = 778 MiB + 235 KiB = 796907 KiB
			corev1.ResourceMemory: *resource.NewQuantity(
				778*Mebibyte+235*Kibibyte,
				resource.BinarySI,
			),
			corev1.ResourceCPU: *resource.NewScaledQuantity(
				pmmCPULarge,
				resource.Milli,
			),
		},
	}
)

// getPMMResources returns the PMM resources to be used for instance.
// The logic is as follows:
//  1. If this is a new instance, use the resources specified in
//     the monitoring component, if any.
//     Otherwise, use the default resources based on the engine component size.
//  2. If this is an existing instance and the engine component size
//     has changed, use the resources specified in the monitoring component, if any.
//     Otherwise, use the default resources based on the new engine component size.
//  3. If this is an existing instance and PMM was enabled before,
//     use the resources specified in the monitoring component, if any.
//     Otherwise, use the current PMM resources.
//  4. If this is an existing instance and PMM was not enabled before,
//     use the resources specified in the monitoring component, if any.
//     Otherwise, use the default resources based on the engine component size.
func getPMMResources(c *controller.Context, curPsmdbSpec *psmdbv1.PerconaServerMongoDBSpec,
) corev1.ResourceRequirements {
	monitoring := c.Instance().Spec.Components[common.ComponentMonitoring]

	requested := corev1.ResourceRequirements{}
	if monitoring.Resources != nil {
		requested = *monitoring.Resources
	}

	engine := c.Instance().Spec.Components[common.ComponentEngine]
	engineMemoryLimits := resource.Quantity{}
	if engine.Resources != nil {
		engineMemoryLimits = engine.Resources.Limits[corev1.ResourceMemory]
	}

	if c.Instance().Status.Phase == corev1alpha1.InstancePhaseCreating {
		// This is new instance.
		// Monitoring component may contain custom PMM resources -> merge them with defaults.
		// If none are specified, default resources will be used.
		return mergeResources(requested, calculatePMMResources(engineMemoryLimits))
	}

	// Fetch current instance size
	var currentReplSet psmdbv1.ReplsetSpec
	for _, replset := range curPsmdbSpec.Replsets {
		if replset.Name == rsName(0) {
			currentReplSet = *replset
			break
		}
	}

	if !equalSize(engineMemoryLimits, *currentReplSet.Resources.Requests.Memory()) {
		// Engine component size has changed -> need to update PMM resources.
		// Monitoring component may contain custom PMM resources -> merge them with defaults.
		return mergeResources(requested, calculatePMMResources(engineMemoryLimits))
	}

	if curPsmdbSpec.PMM.Enabled {
		// Instance is not new and PMM was enabled before.
		// Monitoring component may contain new custom PMM resources -> merge them with previously used PMM resources.
		return mergeResources(requested, curPsmdbSpec.PMM.Resources)
	}

	// Instance is not new and PMM was not enabled before. Now it is being enabled.
	// Monitoring component may contain custom PMM resources -> merge them with defaults.
	return mergeResources(requested, calculatePMMResources(engineMemoryLimits))
}

// equalSize checks if two memory sizes fall into the same predefined size category.
func equalSize(a, b resource.Quantity) bool {
	switch {
	case a.Cmp(memoryLargeSize) >= 0:
		// a is large size -> b must be large size
		return b.Cmp(memoryLargeSize) >= 0
	case a.Cmp(memoryMediumSize) >= 0:
		// a is medium size -> b must be medium size
		return b.Cmp(memoryMediumSize) >= 0 && b.Cmp(memoryLargeSize) == -1
	default:
		// a is small size -> b must be small size (less than medium)
		return b.Cmp(memoryMediumSize) == -1
	}
}

// calculatePMMResources returns the resources for PMM based on memory size.
func calculatePMMResources(m resource.Quantity) corev1.ResourceRequirements {
	if m.Cmp(memoryLargeSize) >= 0 {
		return pmmResourcesLarge
	}

	if m.Cmp(memoryMediumSize) >= 0 {
		return pmmResourcesMedium
	}

	return pmmResourcesSmall
}

// mergeResources merges requested and calculated resources.
// If a resource is specified in both, the value from requested is used.
// If a resource is only specified in one, that value is used.
func mergeResources(requested, calculated corev1.ResourceRequirements) corev1.ResourceRequirements {
	resources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{},
		Limits:   corev1.ResourceList{},
	}

	// CPU Requests
	if hasRequestsCPU(requested) {
		resources.Requests[corev1.ResourceCPU] = *requested.Requests.Cpu()
	} else if hasRequestsCPU(calculated) {
		resources.Requests[corev1.ResourceCPU] = *calculated.Requests.Cpu()
	}

	// Memory Requests
	if hasRequestsMemory(requested) {
		resources.Requests[corev1.ResourceMemory] = *requested.Requests.Memory()
	} else if hasRequestsMemory(calculated) {
		resources.Requests[corev1.ResourceMemory] = *calculated.Requests.Memory()
	}

	// CPU Limits
	if hasLimitsCPU(requested) {
		resources.Limits[corev1.ResourceCPU] = *requested.Limits.Cpu()
	} else if hasLimitsCPU(calculated) {
		resources.Limits[corev1.ResourceCPU] = *calculated.Limits.Cpu()
	}

	// Memory Limits
	if hasLimitsMemory(requested) {
		resources.Limits[corev1.ResourceMemory] = *requested.Limits.Memory()
	} else if hasLimitsMemory(calculated) {
		resources.Limits[corev1.ResourceMemory] = *calculated.Limits.Memory()
	}

	return resources
}

// hasRequestsCPU checks if the given resources has non-zero CPU requests.
func hasRequestsCPU(resources corev1.ResourceRequirements) bool {
	return resources.Requests != nil &&
		resources.Requests.Cpu() != nil &&
		!resources.Requests.Cpu().IsZero()
}

// hasRequestsMemory checks if the given resources has non-zero memory requests.
func hasRequestsMemory(resources corev1.ResourceRequirements) bool {
	return resources.Requests != nil &&
		resources.Requests.Memory() != nil &&
		!resources.Requests.Memory().IsZero()
}

// hasLimitsCPU checks if the given resources has non-zero CPU limits.
func hasLimitsCPU(resources corev1.ResourceRequirements) bool {
	return resources.Limits != nil &&
		resources.Limits.Cpu() != nil &&
		!resources.Limits.Cpu().IsZero()
}

// hasLimitsMemory checks if the given resources has non-zero memory limits.
func hasLimitsMemory(resources corev1.ResourceRequirements) bool {
	return resources.Limits != nil &&
		resources.Limits.Memory() != nil &&
		!resources.Limits.Memory().IsZero()
}
