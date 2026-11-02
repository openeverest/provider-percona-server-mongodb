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
	psmdbconfig "github.com/percona/percona-server-mongodb-operator/pkg/psmdb/config"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
)

const (
	// metricsExporterComponentType is the component type in the provider spec
	// that pins the mongodb_exporter image.
	metricsExporterComponentType = "mongodbExporter"

	metricsExporterContainerName = "mongodb-exporter"
	metricsPortName              = "metrics"
	metricsPort                  = 9216
	metricsPath                  = "/metrics"

	// sslVolumeName is the operator's pod volume holding the certificate
	// clients connect to, mounted at psmdbconfig.SSLDir.
	sslVolumeName = "ssl"

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
)

// configureMetricsExporter adds a mongodb_exporter sidecar to every mongod and
// mongos pod while a monitoring class bound to the instance requires metrics.
func configureMetricsExporter(c *controller.Context, psmdb *psmdbv1.PerconaServerMongoDB, usersSecretName string) error {
	required, err := metricsExporterRequired(c)
	if err != nil || !required {
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

	useTLS := psmdb.Spec.TLS == nil || psmdb.Spec.TLS.Mode != psmdbv1.TLSModeDisabled
	sidecar := func(port int32, collectors []string) corev1.Container {
		return metricsExporterSidecar(image, usersSecretName, port, useTLS, collectors)
	}

	for _, rs := range psmdb.Spec.Replsets {
		rs.Sidecars = append(rs.Sidecars, sidecar(rs.GetPort(), mongodExporterCollectors))
	}

	if !psmdb.Spec.Sharding.Enabled {
		return nil
	}

	cfg := psmdb.Spec.Sharding.ConfigsvrReplSet
	cfg.Sidecars = append(cfg.Sidecars, sidecar(cfg.GetPort(), mongodExporterCollectors))

	mongos := psmdb.Spec.Sharding.Mongos
	mongos.Sidecars = append(mongos.Sidecars, sidecar(mongos.GetPort(), mongosExporterCollectors))

	return nil
}

// metricsExporterSidecar builds a mongodb_exporter container that scrapes the
// MongoDB process in its own pod.
func metricsExporterSidecar(image, usersSecretName string, mongoPort int32, useTLS bool, collectors []string) corev1.Container {
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

	uri := fmt.Sprintf("mongodb://127.0.0.1:%d", mongoPort)
	var mounts []corev1.VolumeMount
	if useTLS {
		// Connect as the operator's PMM client does: present the instance's
		// certificate, so this works whether or not mongod accepts clients
		// without one, and skip verifying the server's, which need not name
		// the loopback address. The connection never leaves the pod.
		uri += fmt.Sprintf("/?tls=true&tlsInsecure=true&tlsCertificateFile=%[1]s/tls.crt&tlsPrivateKeyFile=%[1]s/tls.key",
			psmdbconfig.SSLDir)
		mounts = []corev1.VolumeMount{{Name: sslVolumeName, MountPath: psmdbconfig.SSLDir, ReadOnly: true}}
	}

	args := []string{
		"--mongodb.uri=" + uri,
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
		VolumeMounts: mounts,
		Resources:    metricsExporterResources,
	}
}
