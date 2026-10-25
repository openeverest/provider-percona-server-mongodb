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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/log"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	monitoringv1alpha1 "github.com/openeverest/openeverest/v2/api/monitoring/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
)

// pmmIntegration is the integration name declared in the provider's monitoring
// contract and matched by the pmm MonitoringClass.
const pmmIntegration = "pmm"

// MonitoringIntegrations implements controller.MonitoringIntegrationsProvider.
func (p *PSMDBProvider) MonitoringIntegrations() []string {
	return []string{pmmIntegration}
}

// MonitoringSources implements controller.MonitoringSourcesProvider. Every
// mongod and mongos pod carries a mongodb_exporter sidecar while a bound class
// requires metrics, so one endpoint per component is published: engine, and
// for sharded clusters configServer and proxy, each selected by the component
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
			Port:   intstr.FromString(metricsPortName),
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

// liveEngine returns the engine CR as it is now, empty before it exists.
func liveEngine(c *controller.Context) (*psmdbv1.PerconaServerMongoDB, error) {
	live := &psmdbv1.PerconaServerMongoDB{}
	if err := c.Get(live, c.Name()); err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("get live PerconaServerMongoDB: %w", err)
	}
	return live, nil
}

// metricsExporterRequired says whether the exporter sidecars go on the engine
// pods this pass: when a bound class requires metrics or, while that is
// unresolved, when the live CR already carries them (removing and re-adding a
// sidecar would roll the pods twice for nothing).
func metricsExporterRequired(c *controller.Context) (bool, error) {
	req, err := c.RequiredSources()
	if err != nil {
		return false, err
	}
	live, err := liveEngine(c)
	if err != nil {
		return false, err
	}
	return req.Metrics.Else(hasMetricsExporter(live)), nil
}

// pmmDestinationParameters is the shape of MonitoringDestination.spec.parameters
// for the pmm class.
type pmmDestinationParameters struct {
	URL string `json:"url"`
}

// pmmInstanceParameters is the shape of
// Instance.spec.monitoring.destinations[].parameters for the pmm class.
type pmmInstanceParameters struct {
	Resources corev1.ResourceRequirements `json:"resources"`
}

// applyPMMBindings renders spec.pmm from the Instance's ProviderManaged pmm
// binding while the pmm integration is required, keeps the live spec.pmm
// while that is unresolved, and leaves PMM disabled otherwise.
//
// A binding that fails to render is reported through
// SetMonitoringBindingResult and keeps the live rendering; it never fails
// Sync.
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
			if err := renderPMMBinding(c, psmdb, usersSecretName, rb); err != nil {
				log.FromContext(c.Context()).Error(err, "PMM binding could not be rendered", "binding", rb.Binding.Name)
				c.SetMonitoringBindingResult(rb.Binding.Name, monitoringv1alpha1.ReasonRenderFailed, err.Error())
				psmdb.Spec.PMM = *live.Spec.PMM.DeepCopy()
			}
		}
	case pmm.Else(live.Spec.PMM.Enabled):
		psmdb.Spec.PMM = *live.Spec.PMM.DeepCopy()
	}
	return nil
}

// renderPMMBinding sets spec.pmm from one Apply-able pmm binding: the server
// from the destination URL, the token from the destination credentials, the
// image from the class and the resources from the size tier and the entry
// parameters.
func renderPMMBinding(c *controller.Context, psmdb *psmdbv1.PerconaServerMongoDB, usersSecretName string, rb controller.ResolvedBinding) error {
	image := rb.Class.Spec.ProviderManaged.AgentImage
	if image == "" {
		return fmt.Errorf("MonitoringClass %q sets no providerManaged.agentImage", rb.Class.Name)
	}

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

	entryParams := pmmInstanceParameters{}
	if rb.Binding.Spec.Parameters != nil {
		if err := json.Unmarshal(rb.Binding.Spec.Parameters.Raw, &entryParams); err != nil {
			return fmt.Errorf("decode binding parameters: %w", err)
		}
	}

	// The operator reads the server token from the users secret.
	if err := copySecretData(c, rb.Destination.Spec.CredentialsSecretRef.Name, usersSecretName, "apiKey", "PMM_SERVER_TOKEN"); err != nil {
		return fmt.Errorf("copy PMM token to users secret: %w", err)
	}

	psmdb.Spec.PMM = psmdbv1.PMMSpec{
		Enabled:    true,
		ServerHost: u.Host,
		Image:      image,
		Resources:  getPMMResources(c, entryParams.Resources),
		// mongodParams covers both shard and config server replica sets, so
		// only mongos can be labelled with its component.
		MongodParams: pmmAdminParams(c, ""),
		MongosParams: pmmAdminParams(c, common.ComponentProxy),
	}
	return nil
}

var (
	_ controller.MonitoringSourcesProvider      = (*PSMDBProvider)(nil)
	_ controller.MonitoringIntegrationsProvider = (*PSMDBProvider)(nil)
)
