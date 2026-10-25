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
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mongodb/definition/components"
	vmv1beta1 "github.com/openeverest/provider-percona-server-mongodb/internal/apis/victoriametrics/v1beta1"
)

const (
	// metricsExporterComponentType is the component type in the provider spec
	// that pins the mongodb_exporter image.
	metricsExporterComponentType = "mongodbExporter"

	metricsExporterContainerName = "mongodb-exporter"
	metricsPortName              = "metrics"
	metricsPort                  = 9216
	metricsPath                  = "/metrics"

	// The exporter authenticates as the cluster monitor user the operator
	// keeps in the users secret.
	clusterMonitorUserKey     = "MONGODB_CLUSTER_MONITOR_USER"
	clusterMonitorPasswordKey = "MONGODB_CLUSTER_MONITOR_PASSWORD"
)

var (
	// mongodExporterCollectors are the collectors run against mongod. PBM is
	// left out: its collector rewrites the connection to reach the whole
	// replica set, and backups are reported by the instance status already.
	mongodExporterCollectors = []string{
		"--collector.diagnosticdata",
		"--collector.replicasetstatus",
		"--collector.dbstats",
		"--collector.topmetrics",
		"--collector.currentopmetrics",
		"--collector.indexstats",
		"--collector.collstats",
		// Per-collection collectors get expensive on large catalogs.
		"--collector.collstats-limit=200",
	}

	// mongosExporterCollectors are the collectors that apply to mongos.
	mongosExporterCollectors = []string{
		"--collector.diagnosticdata",
		"--collector.shards",
	}

	metricsExporterResources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("50m"),
			corev1.ResourceMemory: resource.MustParse("64Mi"),
		},
	}

	podMonitorGroupKind  = schema.GroupKind{Group: monitoringv1.SchemeGroupVersion.Group, Kind: monitoringv1.PodMonitorsKind}
	vmPodScrapeGroupKind = schema.GroupKind{Group: vmv1beta1.GroupVersion.Group, Kind: "VMPodScrape"}
)

// metricsParameters returns the metrics settings of the monitoring component,
// or nil when metrics are not exposed.
func metricsParameters(c *controller.Context) (*components.MetricsParameters, error) {
	params, err := monitoringParameters(c)
	if err != nil || params == nil {
		return nil, err
	}
	return params.Metrics, nil
}

func scrapeEnabled(scrape *components.ScrapeParameters) bool {
	return scrape != nil && scrape.Enabled
}

// configureMetricsExporter adds a mongodb_exporter sidecar to every mongod and
// mongos pod when the monitoring component exposes metrics or an
// ExtensionManaged monitoring binding demands them.
func configureMetricsExporter(c *controller.Context, psmdb *psmdbv1.PerconaServerMongoDB, usersSecretName string) error {
	demanded, err := metricsExporterDemanded(c)
	if err != nil || !demanded {
		return err
	}

	spec, err := c.ProviderSpec()
	if err != nil {
		return fmt.Errorf("get provider spec: %w", err)
	}
	image := controller.GetDefaultImage(spec, metricsExporterComponentType)
	if image == "" {
		return fmt.Errorf("provider spec has no image for component type %q", metricsExporterComponentType)
	}

	for _, rs := range psmdb.Spec.Replsets {
		rs.Sidecars = append(rs.Sidecars, metricsExporterSidecar(image, usersSecretName, rs.GetPort(), mongodExporterCollectors))
	}

	if !psmdb.Spec.Sharding.Enabled {
		return nil
	}

	cfg := psmdb.Spec.Sharding.ConfigsvrReplSet
	cfg.Sidecars = append(cfg.Sidecars, metricsExporterSidecar(image, usersSecretName, cfg.GetPort(), mongodExporterCollectors))

	mongos := psmdb.Spec.Sharding.Mongos
	mongos.Sidecars = append(mongos.Sidecars, metricsExporterSidecar(image, usersSecretName, mongos.GetPort(), mongosExporterCollectors))

	return nil
}

// metricsExporterSidecar builds a mongodb_exporter container that scrapes the
// MongoDB process in its own pod.
func metricsExporterSidecar(image, usersSecretName string, mongoPort int32, collectors []string) corev1.Container {
	secretEnv := func(name, key string) corev1.EnvVar {
		return corev1.EnvVar{
			Name: name,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: usersSecretName},
					Key:                  key,
				},
			},
		}
	}

	args := []string{
		fmt.Sprintf("--mongodb.uri=mongodb://127.0.0.1:%d", mongoPort),
		"--mongodb.direct-connect",
		// Keeps the metric names the community Grafana dashboards query.
		"--compatible-mode",
		fmt.Sprintf("--web.listen-address=:%d", metricsPort),
	}

	return corev1.Container{
		Name:  metricsExporterContainerName,
		Image: image,
		Args:  append(args, collectors...),
		// Credentials go through the environment so they do not show in the
		// process list.
		Env: []corev1.EnvVar{
			secretEnv("MONGODB_USER", clusterMonitorUserKey),
			secretEnv("MONGODB_PASSWORD", clusterMonitorPasswordKey),
		},
		Ports: []corev1.ContainerPort{{
			Name:          metricsPortName,
			ContainerPort: metricsPort,
			Protocol:      corev1.ProtocolTCP,
		}},
		Resources: metricsExporterResources,
	}
}

// reconcileMetricsScrapes creates the scrape resource of every monitoring
// system the monitoring component enables, and removes the others.
func reconcileMetricsScrapes(c *controller.Context) error {
	metrics, err := metricsParameters(c)
	if err != nil {
		return err
	}

	var prometheus, victoriaMetrics bool
	if metrics != nil {
		prometheus = scrapeEnabled(metrics.Prometheus)
		victoriaMetrics = scrapeEnabled(metrics.VictoriaMetrics)
	}

	if err := reconcileScrape(c, prometheus, podMonitor(c), &monitoringv1.PodMonitor{ObjectMeta: c.ObjectMeta(podMonitorName(c))}); err != nil {
		return fmt.Errorf("reconcile PodMonitor: %w", err)
	}
	if !prometheus {
		if err := deleteConvertedPodMonitor(c); err != nil {
			return err
		}
	}
	if err := reconcileScrape(c, victoriaMetrics, vmPodScrape(c), &vmv1beta1.VMPodScrape{ObjectMeta: c.ObjectMeta(vmPodScrapeName(c))}); err != nil {
		return fmt.Errorf("reconcile VMPodScrape: %w", err)
	}

	return nil
}

func reconcileScrape(c *controller.Context, enabled bool, desired, existing client.Object) error {
	if enabled {
		return c.Apply(desired)
	}
	return deleteIfServed(c, existing)
}

// deleteIfServed deletes obj; there is nothing to remove when the cluster
// does not serve its kind at all.
func deleteIfServed(c *controller.Context, obj client.Object) error {
	if err := c.Delete(obj); err != nil && !meta.IsNoMatchError(err) {
		return err
	}
	return nil
}

// deleteConvertedPodMonitor removes the VMPodScrape the VictoriaMetrics
// Operator converted from the instance's PodMonitor. The converter does not
// always remove it with the PodMonitor (it sets no owner reference by
// default), and it would keep the instance scraped.
func deleteConvertedPodMonitor(c *controller.Context) error {
	converted := &vmv1beta1.VMPodScrape{ObjectMeta: c.ObjectMeta(podMonitorName(c))}
	if err := deleteIfServed(c, converted); err != nil {
		return fmt.Errorf("delete VMPodScrape converted from the PodMonitor: %w", err)
	}
	return nil
}

// scrapeSelector selects the instance's pods; only those running the
// exporter expose the metrics port the scrape endpoints name.
func scrapeSelector(c *controller.Context) metav1.LabelSelector {
	return metav1.LabelSelector{MatchLabels: map[string]string{controller.InstanceLabel: c.Name()}}
}

// scrapeTargetLabels label every series with the instance and component, the
// way PMM metrics are labelled.
func scrapeTargetLabels() []string {
	return []string{controller.InstanceLabel, controller.ComponentLabel}
}

// The scrape resources are named apart: the VictoriaMetrics Operator converts
// a PodMonitor into a VMPodScrape of the same name by default, and the
// provider must not mistake that one for its own.
func podMonitorName(c *controller.Context) string {
	return c.Name() + "-prometheus"
}

func vmPodScrapeName(c *controller.Context) string {
	return c.Name() + "-victoriametrics"
}

func podMonitor(c *controller.Context) *monitoringv1.PodMonitor {
	return &monitoringv1.PodMonitor{
		ObjectMeta: c.ObjectMeta(podMonitorName(c)),
		Spec: monitoringv1.PodMonitorSpec{
			Selector:        scrapeSelector(c),
			PodTargetLabels: scrapeTargetLabels(),
			PodMetricsEndpoints: []monitoringv1.PodMetricsEndpoint{{
				Port: ptr.To(metricsPortName),
				Path: metricsPath,
			}},
		},
	}
}

func vmPodScrape(c *controller.Context) *vmv1beta1.VMPodScrape {
	return &vmv1beta1.VMPodScrape{
		ObjectMeta: c.ObjectMeta(vmPodScrapeName(c)),
		Spec: vmv1beta1.VMPodScrapeSpec{
			Selector:        scrapeSelector(c),
			PodTargetLabels: scrapeTargetLabels(),
			PodMetricsEndpoints: []vmv1beta1.PodMetricsEndpoint{{
				Port: ptr.To(metricsPortName),
				Path: metricsPath,
			}},
		},
	}
}

// validateMetrics rejects scraping by a monitoring system whose operator is
// not installed, since its scrape resource could not be created.
func validateMetrics(c *controller.Context) error {
	metrics, err := metricsParameters(c)
	if err != nil || metrics == nil {
		return err
	}

	checks := []struct {
		enabled  bool
		kind     schema.GroupKind
		version  string
		operator string
	}{
		{scrapeEnabled(metrics.Prometheus), podMonitorGroupKind, monitoringv1.Version, "Prometheus Operator"},
		{scrapeEnabled(metrics.VictoriaMetrics), vmPodScrapeGroupKind, vmv1beta1.GroupVersion.Version, "VictoriaMetrics Operator"},
	}
	for _, check := range checks {
		if !check.enabled {
			continue
		}
		if _, err := c.Client().RESTMapper().RESTMapping(check.kind, check.version); err != nil {
			if meta.IsNoMatchError(err) {
				return fmt.Errorf("%s is not installed: the cluster does not serve %s", check.operator, check.kind)
			}
			return fmt.Errorf("look up %s: %w", check.kind, err)
		}
	}

	return nil
}
