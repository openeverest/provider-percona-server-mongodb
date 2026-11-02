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
	"strings"
	"testing"

	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
)

const metricsTestUsersSecret = "everest-secrets-my-mongo"

// metricsRequiredContext builds a context for an Instance bound to a class
// that requires metrics.
func metricsRequiredContext(t *testing.T) *controller.Context {
	t.Helper()

	c, _ := bindingContext(t, []corev1alpha1.InstanceMonitoringDestination{entry("prom", "prom-dst")},
		acceptedBinding("prom", "prom-dst", "prometheus", corev1alpha1.MonitoringExecutionModeExtensionManaged),
		destination("prom-dst", "prometheus", "", ""),
		metricsClass("prometheus"),
	)
	return c
}

func shardedPSMDB() *psmdbv1.PerconaServerMongoDB {
	return &psmdbv1.PerconaServerMongoDB{
		Spec: psmdbv1.PerconaServerMongoDBSpec{
			Replsets: []*psmdbv1.ReplsetSpec{{Name: "rs0"}, {Name: "rs1"}},
			Sharding: psmdbv1.Sharding{
				Enabled:          true,
				ConfigsvrReplSet: &psmdbv1.ReplsetSpec{Name: "cfg"},
				Mongos:           &psmdbv1.MongosSpec{Port: 27018},
			},
		},
	}
}

func exporterSidecar(t *testing.T, sidecars []corev1.Container) corev1.Container {
	t.Helper()

	require.Len(t, sidecars, 1)
	assert.Equal(t, metricsExporterContainerName, sidecars[0].Name)
	return sidecars[0]
}

func TestConfigureMetricsExporter(t *testing.T) {
	t.Parallel()

	t.Run("adds an exporter to every mongod and mongos pod", func(t *testing.T) {
		t.Parallel()

		c := metricsRequiredContext(t)
		psmdb := shardedPSMDB()

		require.NoError(t, configureMetricsExporter(c, psmdb, metricsTestUsersSecret))

		for _, rs := range psmdb.Spec.Replsets {
			sidecar := exporterSidecar(t, rs.Sidecars)
			assert.Equal(t, "percona/mongodb_exporter:0.53.0", sidecar.Image)
			assert.Contains(t, sidecar.Args, "--mongodb.uri=mongodb://127.0.0.1:27017/?tls=true&tlsInsecure=true&tlsCertificateFile=/etc/mongodb-ssl/tls.crt&tlsPrivateKeyFile=/etc/mongodb-ssl/tls.key",
				"TLS is on by default, so the exporter connects as the operator's tools do")
			assert.Equal(t, []corev1.VolumeMount{{Name: "ssl", MountPath: "/etc/mongodb-ssl", ReadOnly: true}}, sidecar.VolumeMounts)
			assert.Contains(t, sidecar.Args, "--collector.replicasetstatus")
			assert.Equal(t, []corev1.ContainerPort{{Name: "metrics", ContainerPort: 9216, Protocol: corev1.ProtocolTCP}}, sidecar.Ports)

			require.Len(t, sidecar.Env, 2)
			for env, key := range map[string]string{"MONGODB_USER": clusterMonitorUserKey, "MONGODB_PASSWORD": clusterMonitorPasswordKey} {
				idx := -1
				for i := range sidecar.Env {
					if sidecar.Env[i].Name == env {
						idx = i
					}
				}
				require.NotEqual(t, -1, idx, "missing %s", env)
				ref := sidecar.Env[idx].ValueFrom.SecretKeyRef
				assert.Equal(t, metricsTestUsersSecret, ref.Name)
				assert.Equal(t, key, ref.Key)
			}
		}

		exporterSidecar(t, psmdb.Spec.Sharding.ConfigsvrReplSet.Sidecars)

		mongos := exporterSidecar(t, psmdb.Spec.Sharding.Mongos.Sidecars)
		assert.True(t, strings.HasPrefix(mongos.Args[0], "--mongodb.uri=mongodb://127.0.0.1:27018/"), "the exporter follows the mongos port")
		assert.Contains(t, mongos.Args, "--collector.shards")
		assert.NotContains(t, mongos.Args, "--collector.replicasetstatus", "mongos has no replica set status")
	})

	t.Run("connects without TLS when TLS is disabled", func(t *testing.T) {
		t.Parallel()

		c := metricsRequiredContext(t)
		psmdb := shardedPSMDB()
		psmdb.Spec.TLS = &psmdbv1.TLSSpec{Mode: psmdbv1.TLSModeDisabled}

		require.NoError(t, configureMetricsExporter(c, psmdb, metricsTestUsersSecret))

		sidecar := exporterSidecar(t, psmdb.Spec.Replsets[0].Sidecars)
		assert.Contains(t, sidecar.Args, "--mongodb.uri=mongodb://127.0.0.1:27017")
		assert.Empty(t, sidecar.VolumeMounts)
	})

	t.Run("leaves pods alone when no bound class requires metrics", func(t *testing.T) {
		t.Parallel()

		pmmOnly := []client.Object{
			acceptedBinding("pmm", "pmm-dst", "pmm", corev1alpha1.MonitoringExecutionModeProviderManaged),
			destination("pmm-dst", "pmm", "https://pmm.example.com", "pmm-token"),
			pmmClass("percona/pmm-client:3.8.0"),
		}
		for name, tc := range map[string]struct {
			entries []corev1alpha1.InstanceMonitoringDestination
			objs    []client.Object
		}{
			"no monitoring": {},
			"PMM only":      {entries: []corev1alpha1.InstanceMonitoringDestination{entry("pmm", "pmm-dst")}, objs: pmmOnly},
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				c, _ := bindingContext(t, tc.entries, tc.objs...)
				psmdb := shardedPSMDB()

				require.NoError(t, configureMetricsExporter(c, psmdb, metricsTestUsersSecret))

				assert.Empty(t, psmdb.Spec.Replsets[0].Sidecars)
				assert.Empty(t, psmdb.Spec.Sharding.ConfigsvrReplSet.Sidecars)
				assert.Empty(t, psmdb.Spec.Sharding.Mongos.Sidecars)
			})
		}
	})
}
