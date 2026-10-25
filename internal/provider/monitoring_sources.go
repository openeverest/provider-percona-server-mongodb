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
	"fmt"
	"net/url"

	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	monitoringv1alpha1 "github.com/openeverest/openeverest/v2/api/monitoring/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
)

// pmmIntegration is the integration name declared in the chart's monitoring
// contract and matched by the pmm MonitoringClass.
const pmmIntegration = "pmm"

// MonitoringIntegrations implements controller.MonitoringIntegrationsProvider.
func (p *PSMDBProvider) MonitoringIntegrations() []string {
	return []string{pmmIntegration}
}

// MonitoringSources implements controller.MonitoringSourcesProvider. Every
// mongod and mongos pod carries a mongodb_exporter sidecar while metrics are
// demanded, so one endpoint per component is published: engine, and for
// sharded clusters configServer and proxy, each selected by the component
// labels labelPods sets.
func (p *PSMDBProvider) MonitoringSources(c *controller.Context) (*corev1alpha1.MonitoringSources, error) {
	psmdb := &psmdbv1.PerconaServerMongoDB{}
	if err := c.Get(psmdb, c.Name()); err != nil {
		// Nothing is serving before the engine CR exists.
		return &corev1alpha1.MonitoringSources{}, nil //nolint:nilerr
	}
	s := &corev1alpha1.MonitoringSources{}
	if !hasMetricsExporter(psmdb) || psmdb.Status.State != psmdbv1.AppStateReady {
		return s, nil
	}
	components := []string{common.ComponentEngine}
	if psmdb.Spec.Sharding.Enabled {
		components = append(components, common.ComponentConfigServer, common.ComponentProxy)
	}
	for _, component := range components {
		s.Metrics = append(s.Metrics, corev1alpha1.MetricsEndpoint{
			Component: component,
			Kind:      "mongodb",
			PodSelector: map[string]string{
				controller.InstanceLabel:  c.Name(),
				controller.ComponentLabel: component,
			},
			Port:   corev1alpha1.MetricsEndpointPort{Name: metricsPortName},
			Path:   metricsPath,
			Scheme: "http",
		})
	}
	return s, nil
}

// hasMetricsExporter reports whether the engine CR carries the exporter
// sidecar on its first replica set; the sidecar is always added to every
// component together.
func hasMetricsExporter(psmdb *psmdbv1.PerconaServerMongoDB) bool {
	for _, rs := range psmdb.Spec.Replsets {
		for _, sc := range rs.Sidecars {
			if sc.Name == metricsExporterContainerName {
				return true
			}
		}
	}
	return false
}

// metricsExporterDemanded says whether the exporter sidecars go on the engine
// pods this pass: when the legacy monitoring component exposes metrics, when an
// ExtensionManaged binding's class requires metrics, or, while bindings are
// still being materialised, when the live CR already carries them (hold rule:
// removing and re-adding a sidecar would roll the pods twice for nothing).
func metricsExporterDemanded(c *controller.Context) (bool, error) {
	legacy, err := metricsParameters(c)
	if err != nil {
		return false, err
	}
	if legacy != nil {
		return true, nil
	}
	demand, err := c.MonitoringDemand()
	if err != nil {
		return false, err
	}
	if demand.Metrics {
		return true, nil
	}
	if demand.Settled {
		return false, nil
	}
	live := &psmdbv1.PerconaServerMongoDB{}
	if err := c.Get(live, c.Name()); err != nil {
		return false, nil //nolint:nilerr // nothing live to hold
	}
	return hasMetricsExporter(live), nil
}

// pmmDestinationParameters is the shape of MonitoringDestination.spec.parameters
// for the pmm class.
type pmmDestinationParameters struct {
	URL       string `json:"url"`
	VerifyTLS *bool  `json:"verifyTLS,omitempty"`
}

// pmmInstanceParameters is the shape of
// Instance.spec.monitoring.destinations[].parameters for the pmm class.
type pmmInstanceParameters struct {
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
}

// applyPMMBindings renders spec.pmm from the Instance's ProviderManaged pmm
// binding (a spec.monitoring.destinations[] entry), if any. It reports whether
// a binding claimed spec.pmm so the caller can fall back to the legacy
// monitoring component otherwise.
//
// Hold rule: a binding that exists but is not Accepted keeps whatever the
// live engine CR carries. A binding that fails to render is reported through
// SetMonitoringBindingResult and likewise keeps the live rendering; it never
// fails Sync.
func applyPMMBindings(c *controller.Context, psmdb *psmdbv1.PerconaServerMongoDB, usersSecretName string) (bool, error) {
	bindings, err := c.MonitoringBindings()
	if err != nil {
		return false, err
	}
	for _, rb := range bindings {
		if rb.Mode() != corev1alpha1.MonitoringExecutionModeProviderManaged || rb.Entry == nil {
			continue
		}
		if rb.Class != nil && rb.Integration() != pmmIntegration {
			continue
		}
		if !rb.Apply {
			keepLivePMM(c, psmdb)
			return true, nil
		}
		if err := renderPMMBinding(c, psmdb, usersSecretName, rb); err != nil {
			log.FromContext(c.Context()).Error(err, "PMM binding could not be rendered", "binding", rb.Binding.Name)
			c.SetMonitoringBindingResult(rb.Binding.Name, monitoringv1alpha1.ReasonRenderFailed, err.Error())
			keepLivePMM(c, psmdb)
		}
		return true, nil
	}
	return false, nil
}

func keepLivePMM(c *controller.Context, psmdb *psmdbv1.PerconaServerMongoDB) {
	live := &psmdbv1.PerconaServerMongoDB{}
	if err := c.Get(live, c.Name()); err == nil {
		psmdb.Spec.PMM = *live.Spec.PMM.DeepCopy()
	}
}

func renderPMMBinding(c *controller.Context, psmdb *psmdbv1.PerconaServerMongoDB, usersSecretName string, rb controller.ResolvedBinding) error {
	dstParams := pmmDestinationParameters{}
	if rb.Destination.Spec.Parameters != nil {
		if err := json.Unmarshal(rb.Destination.Spec.Parameters.Raw, &dstParams); err != nil {
			return fmt.Errorf("decode MonitoringDestination %q parameters: %w", rb.Destination.Name, err)
		}
	}
	u, err := url.Parse(dstParams.URL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("MonitoringDestination %q parameters.url %q: not an absolute URL", rb.Destination.Name, dstParams.URL)
	}
	if rb.Destination.Spec.CredentialsSecretRef == nil {
		return fmt.Errorf("MonitoringDestination %q has no credentialsSecretRef", rb.Destination.Name)
	}

	image := rb.Class.Spec.ProviderManaged.AgentImage
	if image == "" {
		spec, err := c.ProviderSpec()
		if err != nil {
			return err
		}
		image = controller.GetDefaultImageForComponent(spec, common.ComponentMonitoring)
	}
	if image == "" {
		return fmt.Errorf("MonitoringClass %q sets no agentImage and the provider has no default pmm image", rb.Class.Name)
	}

	// The operator reads the server token from the users secret.
	if err := copySecretData(c, rb.Destination.Spec.CredentialsSecretRef.Name, usersSecretName, "apiKey", "PMM_SERVER_TOKEN"); err != nil {
		return fmt.Errorf("copy PMM token to users secret: %w", err)
	}

	resources := getPMMResources(c)
	if rb.Binding.Spec.Parameters != nil {
		bp := pmmInstanceParameters{}
		if err := json.Unmarshal(rb.Binding.Spec.Parameters.Raw, &bp); err != nil {
			return fmt.Errorf("decode binding parameters: %w", err)
		}
		if bp.Resources != nil {
			resources = mergeResources(*bp.Resources, resources)
		}
	}

	psmdb.Spec.PMM = psmdbv1.PMMSpec{
		Enabled:      true,
		ServerHost:   u.Host,
		Image:        image,
		Resources:    resources,
		MongodParams: pmmAdminParams(c, ""),
		MongosParams: pmmAdminParams(c, common.ComponentProxy),
	}
	return nil
}

var (
	_ controller.MonitoringSourcesProvider      = (*PSMDBProvider)(nil)
	_ controller.MonitoringIntegrationsProvider = (*PSMDBProvider)(nil)
)
