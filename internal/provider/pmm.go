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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
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

// getPMMResources returns the PMM resource requirements for the instance.
//
// Default resources are calculated from the engine memory size tier (small,
// medium, or large) and then merged with any user-specified overrides from the
// monitoring component. User-provided values always take precedence over the
// calculated defaults. Resources are never preserved from a previous monitoring
// configuration; they are always freshly calculated on each reconciliation.
func getPMMResources(
	c *controller.Context,
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

	var defaultResources corev1.ResourceRequirements

	switch {
	case engineMemoryLimits.Cmp(memoryLargeSize) >= 0:
		defaultResources = pmmResourcesLarge
	case engineMemoryLimits.Cmp(memoryMediumSize) >= 0:
		defaultResources = pmmResourcesMedium
	default:
		defaultResources = pmmResourcesSmall
	}

	return mergeResources(requested, defaultResources)
}

// mergeResources merges requested and default resources.
// If a resource is specified in both, the value from requested is used.
// If a resource is only specified in one, that value is used.
func mergeResources(requested, defaultResources corev1.ResourceRequirements) corev1.ResourceRequirements {
	resources := corev1.ResourceRequirements{}

	resources.Requests = mergeResourceList(requested.Requests, defaultResources.Requests)
	resources.Limits = mergeResourceList(requested.Limits, defaultResources.Limits)

	return resources
}

// mergeResourceList merges two resource lists, preferring values from requested.
// Returns nil if the merged result is empty.
func mergeResourceList(requested, defaultResources corev1.ResourceList) corev1.ResourceList {
	merged := corev1.ResourceList{}

	for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		if v, ok := requested[name]; ok && !v.IsZero() {
			merged[name] = v
		} else if v, ok := defaultResources[name]; ok && !v.IsZero() {
			merged[name] = v
		}
	}

	if len(merged) == 0 {
		return nil
	}

	return merged
}
