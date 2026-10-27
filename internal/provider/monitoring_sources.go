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
	"slices"

	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/intstr"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
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
// labels labelPods sets, once that component has a ready pod.
func (p *PSMDBProvider) MonitoringSources(c *controller.Context) (*corev1alpha1.MonitoringSources, error) {
	psmdb, err := liveEngine(c)
	if err != nil {
		return nil, err
	}
	s := &corev1alpha1.MonitoringSources{}
	if !hasMetricsExporter(psmdb) {
		return s, nil
	}
	for _, component := range servingComponents(psmdb) {
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

// servingComponents returns the components the operator reports a ready pod
// for, so a rolling restart or scale keeps their endpoints published.
func servingComponents(psmdb *psmdbv1.PerconaServerMongoDB) []string {
	var components []string
	if slices.ContainsFunc(psmdb.Spec.Replsets, func(rs *psmdbv1.ReplsetSpec) bool {
		return psmdb.Status.Replsets[rs.Name].Ready > 0
	}) {
		components = append(components, common.ComponentEngine)
	}
	if !psmdb.Spec.Sharding.Enabled {
		return components
	}
	if psmdb.Status.Replsets[psmdbv1.ConfigReplSetName].Ready > 0 {
		components = append(components, common.ComponentConfigServer)
	}
	if psmdb.Status.Mongos != nil && psmdb.Status.Mongos.Ready > 0 {
		components = append(components, common.ComponentProxy)
	}
	return components
}

// hasMetricsExporter reports whether the engine CR carries the exporter
// sidecar on any replica set; the sidecar is always added to every component
// together.
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

var (
	_ controller.MonitoringSourcesProvider      = (*PSMDBProvider)(nil)
	_ controller.MonitoringIntegrationsProvider = (*PSMDBProvider)(nil)
)
