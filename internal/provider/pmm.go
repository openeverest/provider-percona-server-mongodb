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
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/log"

	monitoringv1alpha1 "github.com/openeverest/openeverest/v2/api/monitoring/v1alpha1"
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
// medium, or large) and then merged with requested, the overrides set in the
// parameters of the instance's pmm monitoring destination. Requested values
// always take precedence over the calculated defaults. Resources are never
// preserved from a previous monitoring configuration; they are always freshly
// calculated on each reconciliation.
func getPMMResources(
	c *controller.Context,
	requested corev1.ResourceRequirements,
) (corev1.ResourceRequirements, error) {
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
// A default request above the merged limit is lowered to the limit; a
// requested request above it is an error.
func mergeResources(requested, defaultResources corev1.ResourceRequirements) (corev1.ResourceRequirements, error) {
	resources := corev1.ResourceRequirements{}

	resources.Requests = mergeResourceList(requested.Requests, defaultResources.Requests)
	resources.Limits = mergeResourceList(requested.Limits, defaultResources.Limits)

	for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		request, hasRequest := resources.Requests[name]
		limit, hasLimit := resources.Limits[name]
		if !hasRequest || !hasLimit || request.Cmp(limit) <= 0 {
			continue
		}
		if v, ok := requested.Requests[name]; ok && !v.IsZero() {
			return corev1.ResourceRequirements{}, fmt.Errorf("resources.requests.%s %s exceeds resources.limits.%s %s",
				name, request.String(), name, limit.String())
		}
		resources.Requests[name] = limit
	}

	return resources, nil
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

// pmmInstanceParameters is the shape of
// Instance.spec.monitoring.destinations[].parameters for the pmm class.
type pmmInstanceParameters struct {
	Resources corev1.ResourceRequirements `json:"resources"`
}

// renderFailure is a binding that cannot be rendered as configured. It is
// reported as RenderFailed; any other error fails Sync, which is retried.
type renderFailure struct{ error }

func renderFailedf(format string, args ...any) error {
	return renderFailure{fmt.Errorf(format, args...)}
}

// applyPMMBindings renders spec.pmm from the Instance's ProviderManaged pmm
// binding while the pmm integration is required, keeps the live spec.pmm
// while that is unresolved, and leaves PMM disabled otherwise, removing the
// server token from the users secret once the live cluster has PMM off.
//
// A binding that cannot be rendered as configured is reported through
// SetMonitoringBindingResult and keeps the live rendering; a rendered one is
// reported as AgentTLSNotVerified.
func applyPMMBindings(c *controller.Context, psmdb *psmdbv1.PerconaServerMongoDB, usersSecretName string) error {
	req, err := c.RequiredSources()
	if err != nil {
		return err
	}
	live, err := liveEngine(c)
	if err != nil {
		return err
	}
	switch pmm := req.Integrations[pmmIntegration]; {
	case pmm == controller.Required:
		bindings, err := c.MonitoringBindings()
		if err != nil {
			return err
		}
		for _, rb := range bindings {
			if !rb.Apply || rb.Integration() != pmmIntegration {
				continue
			}
			err := renderPMMBinding(c, psmdb, usersSecretName, rb)
			if _, invalid := errors.AsType[renderFailure](err); invalid {
				log.FromContext(c.Context()).Error(err, "PMM binding could not be rendered", "binding", rb.Binding.Name)
				c.SetMonitoringBindingResult(rb.Binding.Name, monitoringv1alpha1.ReasonRenderFailed, err.Error())
				psmdb.Spec.PMM = *live.Spec.PMM.DeepCopy()
				continue
			}
			if err != nil {
				return fmt.Errorf("render PMM binding %s: %w", rb.Binding.Name, err)
			}
			c.SetMonitoringBindingResult(rb.Binding.Name, monitoringv1alpha1.ReasonAgentTLSNotVerified,
				"pmm-client does not verify the PMM server certificate, whatever spec.tls says: the Percona operator runs it with PMM_AGENT_SERVER_INSECURE_TLS=1")
		}
	case pmm.Else(live.Spec.PMM.Enabled):
		psmdb.Spec.PMM = *live.Spec.PMM.DeepCopy()
	case pmm == controller.NotRequired && !live.Spec.PMM.Enabled:
		return removeSecretData(c, usersSecretName, psmdbv1.PMMServerToken)
	}
	return nil
}

// renderPMMBinding sets spec.pmm from one Apply-able pmm binding: the server
// from the destination's spec.url, the token from the destination credentials,
// the image from the class and the resources from the size tier and the entry
// parameters.
func renderPMMBinding(c *controller.Context, psmdb *psmdbv1.PerconaServerMongoDB, usersSecretName string, rb controller.ResolvedBinding) error {
	image := rb.Class.Spec.ProviderManaged.AgentImage
	if image == "" {
		return renderFailedf("MonitoringClass %q sets no providerManaged.agentImage", rb.Class.Name)
	}

	u, err := url.Parse(rb.Destination.Spec.URL)
	if err != nil || u.Host == "" {
		return renderFailedf("MonitoringDestination %q spec.url %q: not an absolute URL", rb.Destination.Name, rb.Destination.Spec.URL)
	}
	// A missing Secret or key is a renderFailure; other API errors are retried.
	_, credentials, err := c.MonitoringCredentials(rb)
	if _, isAPIErr := errors.AsType[*apierrors.StatusError](err); isAPIErr && !apierrors.IsNotFound(err) {
		return fmt.Errorf("read PMM credentials: %w", err)
	}
	if err != nil {
		return renderFailure{err}
	}
	token := credentials["token"]
	if len(token) == 0 {
		return renderFailedf("MonitoringDestination %q credentials carry no token", rb.Destination.Name)
	}

	entryParams := pmmInstanceParameters{}
	if rb.Binding.Spec.Parameters != nil {
		if err := json.Unmarshal(rb.Binding.Spec.Parameters.Raw, &entryParams); err != nil {
			return renderFailedf("decode binding parameters: %w", err)
		}
	}
	resources, err := getPMMResources(c, entryParams.Resources)
	if err != nil {
		return renderFailedf("binding parameters: %w", err)
	}

	// The operator reads the server token from the users secret; server-side
	// apply owns only that key and leaves the operator's user keys alone.
	if err := c.Apply(&corev1.Secret{
		ObjectMeta: c.ObjectMeta(usersSecretName),
		Data:       map[string][]byte{psmdbv1.PMMServerToken: token},
	}); err != nil {
		return fmt.Errorf("copy PMM token to users secret: %w", err)
	}

	psmdb.Spec.PMM = psmdbv1.PMMSpec{
		Enabled:    true,
		ServerHost: u.Host,
		Image:      image,
		Resources:  resources,
		// mongodParams covers both shard and config server replica sets, so
		// only mongos can be labelled with its component.
		MongodParams: pmmAdminParams(c, ""),
		MongosParams: pmmAdminParams(c, common.ComponentProxy),
	}
	return nil
}
