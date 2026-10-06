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
	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	"github.com/percona/percona-server-mongodb-operator/pkg/naming"
	corev1 "k8s.io/api/core/v1"

	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
)

// applyScheduling places the pods of every component of the cluster.
func applyScheduling(c *controller.Context, psmdb *psmdbv1.PerconaServerMongoDB) {
	components := c.Instance().Spec.Components
	for _, rs := range psmdb.Spec.Replsets {
		scheduleMultiAZ(&rs.MultiAZ, components[common.ComponentEngine].SchedulingPolicy, naming.MongodLabels(psmdb, rs), corev1.LabelHostname)
	}
	if !psmdb.Spec.Sharding.Enabled {
		return
	}
	// The operator uses "cfg" as both the replset name and the component label of config server pods.
	cfgLabels := naming.RSLabels(psmdb, &psmdbv1.ReplsetSpec{Name: psmdbv1.ConfigReplSetName})
	cfgLabels[naming.LabelKubernetesComponent] = psmdbv1.ConfigReplSetName
	scheduleMultiAZ(&psmdb.Spec.Sharding.ConfigsvrReplSet.MultiAZ, components[common.ComponentConfigServer].SchedulingPolicy, cfgLabels, corev1.LabelHostname)
	// mongos holds no data, so its replicas may share a node.
	scheduleMultiAZ(&psmdb.Spec.Sharding.Mongos.MultiAZ, components[common.ComponentProxy].SchedulingPolicy, naming.MongosLabels(psmdb), psmdbv1.AffinityOff)
}

// scheduleMultiAZ places one component's pods. Unless the user brings their
// own affinity, the operator's required anti-affinity keeps the pods in
// separate antiAffinityKey domains; psmdbv1.AffinityOff disables it.
func scheduleMultiAZ(az *psmdbv1.MultiAZ, policy *commonv1alpha1.SchedulingPolicy, podLabels map[string]string, antiAffinityKey string) {
	az.Affinity = &psmdbv1.PodAffinity{TopologyKey: new(antiAffinityKey)}
	if policy != nil && policy.Affinity != nil {
		az.Affinity = &psmdbv1.PodAffinity{Advanced: policy.Affinity}
	}
	az.TopologySpreadConstraints = controller.TopologySpreadConstraints(policy, podLabels)
}

// labelPods labels every component's pods so the runtime counts them into
// the Instance's status.components. The operator adds them to the pod
// templates only, never to the StatefulSet selectors.
func labelPods(c *controller.Context, psmdb *psmdbv1.PerconaServerMongoDB) {
	for _, rs := range psmdb.Spec.Replsets {
		rs.Labels = c.PodLabels(common.ComponentEngine)
	}
	if !psmdb.Spec.Sharding.Enabled {
		return
	}
	psmdb.Spec.Sharding.ConfigsvrReplSet.Labels = c.PodLabels(common.ComponentConfigServer)
	psmdb.Spec.Sharding.Mongos.Labels = c.PodLabels(common.ComponentProxy)
}
