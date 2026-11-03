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
	"strconv"
	"strings"

	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	"github.com/percona/percona-server-mongodb-operator/pkg/naming"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
)

// hiddenReplicas is the number of hidden members the instance asks for. Zero
// turns them off: the component may be absent, or left without replicas by a
// form that switched it off.
func hiddenReplicas(c *controller.Context) int32 {
	hidden, ok := c.Instance().Spec.Components[common.ComponentHidden]
	if !ok || hidden.Replicas == nil {
		return 0
	}
	return *hidden.Replicas
}

// configureHidden adds the hidden members to a replica set. They hold a full
// copy of the data and vote, but clients never see them and they can't become
// primary. Resources and storage left unset match the engine's.
//
// Hidden members being turned on start only once the volumes of earlier ones
// are gone: a new StatefulSet would otherwise bind them, keeping their stale
// copy and their storage class. Until then they stay off and the next pass
// retries (see hiddenStartPending).
func configureHidden(c *controller.Context, rs *psmdbv1.ReplsetSpec) error {
	size := hiddenReplicas(c)
	if size <= 0 {
		return nil
	}

	live, err := liveEngine(c)
	if err != nil {
		return err
	}
	liveHidden := psmdbv1.HiddenSpec{}
	for _, liveRS := range live.Spec.Replsets {
		if liveRS != nil && liveRS.Name == rs.Name {
			liveHidden = liveRS.Hidden
		}
	}

	if !liveHidden.Enabled {
		remaining, err := deleteHiddenVolumes(c, 0)
		if err != nil {
			return err
		}
		if remaining > 0 {
			return nil
		}
	}

	components := c.Instance().Spec.Components
	engine := components[common.ComponentEngine]
	hidden := components[common.ComponentHidden]

	rs.Hidden = psmdbv1.HiddenSpec{
		Enabled:    true,
		Size:       size,
		VolumeSpec: mongodVolume(hiddenStorage(engine.Storage, hidden.Storage)),
		MultiAZ: psmdbv1.MultiAZ{
			PodDisruptionBudget: &psmdbv1.PodDisruptionBudgetSpec{
				MaxUnavailable: &maxUnavailable,
			},
			Resources: mongodResources(hiddenResources(engine.Resources, hidden.Resources)),
		},
	}

	// The class of running hidden members can't change: StatefulSet
	// volumeClaimTemplates are immutable, so the operator would get stuck
	// applying it. Turning them off and on again is how it changes.
	if liveHidden.Enabled && liveHidden.VolumeSpec != nil &&
		liveHidden.VolumeSpec.PersistentVolumeClaim.PersistentVolumeClaimSpec != nil {
		rs.Hidden.VolumeSpec.PersistentVolumeClaim.StorageClassName = liveHidden.VolumeSpec.PersistentVolumeClaim.StorageClassName
	}
	return nil
}

// hiddenStartPending reports whether hidden members asked for are held back
// while the volumes of earlier ones are deleted.
func hiddenStartPending(c *controller.Context, psmdb *psmdbv1.PerconaServerMongoDB) bool {
	if hiddenReplicas(c) <= 0 || psmdb.Spec.Sharding.Enabled {
		return false
	}
	return !slices.ContainsFunc(psmdb.Spec.Replsets, func(rs *psmdbv1.ReplsetSpec) bool {
		return rs.Hidden.Enabled
	})
}

// hiddenStorage fills what the hidden storage leaves unset from the engine's.
func hiddenStorage(engine, hidden *corev1alpha1.Storage) *corev1alpha1.Storage {
	if hidden == nil {
		return engine
	}
	merged := hidden.DeepCopy()
	if engine == nil {
		return merged
	}
	if merged.Size.IsZero() {
		merged.Size = engine.Size
	}
	if merged.StorageClass == nil {
		merged.StorageClass = engine.StorageClass
	}
	return merged
}

// hiddenResources fills the CPU and memory the hidden resources leave unset,
// limit by limit and request by request, from the engine's: a hidden member
// without a memory limit would size its cache from the whole node.
func hiddenResources(engine, hidden *corev1.ResourceRequirements) *corev1.ResourceRequirements {
	if hidden == nil {
		return engine
	}
	merged := hidden.DeepCopy()
	if engine == nil {
		return merged
	}
	fill := func(into *corev1.ResourceList, from corev1.ResourceList) {
		for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
			value, ok := from[name]
			if current := (*into)[name]; !ok || !current.IsZero() {
				continue
			}
			if *into == nil {
				*into = corev1.ResourceList{}
			}
			(*into)[name] = value
		}
	}
	fill(&merged.Limits, engine.Limits)
	fill(&merged.Requests, engine.Requests)
	return merged
}

// mirrorReplsetOntoHidden gives the hidden members the final mongod
// configuration and sidecars of their replica set, once everything that
// adjusts them (engine configuration, TLS options, metrics exporter) has run.
// The operator keeps both separately for hidden members and would otherwise
// run them on its defaults.
func mirrorReplsetOntoHidden(psmdb *psmdbv1.PerconaServerMongoDB) {
	for _, rs := range psmdb.Spec.Replsets {
		if !rs.Hidden.Enabled {
			continue
		}
		rs.Hidden.Configuration = rs.Configuration
		rs.Hidden.Sidecars = append([]corev1.Container(nil), rs.Sidecars...)
	}
}

// hiddenBackupPriority is the backup priority of hidden members, PBM's own
// default for them.
const hiddenBackupPriority = 2.0

// preferHiddenForBackups lets backups run on hidden members, keeping their
// load off the members that serve the application. PBM prefers hidden
// members by itself, but only while no priority is configured: the operator
// configures one (the primary, deprioritised) whenever backup options are
// set, and every member left out of it then gets PBM's default priority, the
// hidden ones included. Listing the hidden members restores their preference;
// the primary is then no longer deprioritised, which matters only while no
// hidden member can take the backup.
func preferHiddenForBackups(psmdb *psmdbv1.PerconaServerMongoDB) {
	opts := psmdb.Spec.Backup.Configuration.BackupOptions
	if opts == nil {
		return
	}
	dnsSuffix := psmdb.Spec.ClusterServiceDNSSuffix
	if dnsSuffix == "" {
		dnsSuffix = psmdbv1.DefaultDNSSuffix
	}

	priority := map[string]float64{}
	for _, rs := range psmdb.Spec.Replsets {
		if !rs.Hidden.Enabled {
			continue
		}
		for i := range rs.Hidden.Size {
			// The member's host as the operator registers it in the replica set
			// configuration: the pod's DNS name under the replica set Service.
			host := fmt.Sprintf("%s-%d.%s-%s.%s.%s:%d",
				naming.HiddenStatefulSetName(psmdb, rs), i,
				psmdb.Name, rs.Name, psmdb.Namespace, dnsSuffix, rs.GetPort())
			priority[host] = hiddenBackupPriority
		}
	}
	if len(priority) > 0 {
		opts.Priority = priority
	}
}

// deleteUnusedHiddenVolumes deletes the volumes of hidden members that were
// removed, by scaling down or by turning hidden members off. The operator
// keeps them, and a hidden member added again later would start from that
// stale copy, which may be too far behind to catch up.
func deleteUnusedHiddenVolumes(c *controller.Context) error {
	_, err := deleteHiddenVolumes(c, int(hiddenReplicas(c)))
	return err
}

// deleteHiddenVolumes deletes the hidden members' volumes from the ordinal
// keepBelow up whose pod is gone, and returns how many of those volumes are
// still there: in use, or being deleted.
func deleteHiddenVolumes(c *controller.Context, keepBelow int) (int, error) {
	selector := client.MatchingLabels{
		naming.LabelKubernetesInstance:  c.Name(),
		naming.LabelKubernetesComponent: naming.ComponentHidden,
	}

	pvcs := &corev1.PersistentVolumeClaimList{}
	if err := c.List(pvcs, selector); err != nil {
		return 0, fmt.Errorf("list hidden volumes: %w", err)
	}
	if len(pvcs.Items) == 0 {
		return 0, nil
	}

	pods := &corev1.PodList{}
	if err := c.List(pods, selector); err != nil {
		return 0, fmt.Errorf("list hidden pods: %w", err)
	}

	remaining := 0
	for i := range pvcs.Items {
		pvc := &pvcs.Items[i]
		// StatefulSet volumes are named <template>-<pod>, and pods end with
		// their ordinal.
		ordinal, err := strconv.Atoi(pvc.Name[strings.LastIndex(pvc.Name, "-")+1:])
		if err != nil || ordinal < keepBelow {
			continue
		}
		remaining++
		inUse := slices.ContainsFunc(pods.Items, func(pod corev1.Pod) bool {
			return strings.HasSuffix(pvc.Name, "-"+pod.Name)
		})
		if inUse || pvc.DeletionTimestamp != nil {
			continue
		}
		if err := c.Delete(pvc); err != nil {
			return 0, fmt.Errorf("delete hidden volume %s: %w", pvc.Name, err)
		}
	}
	return remaining, nil
}

// withoutHiddenHosts drops the hidden members from the replica set's host
// list. The operator lists every member it runs, but clients must not be
// pointed at hidden members: applications never reaching them is their whole
// point. A hidden member appears either by its pod DNS name or, when the
// replica set is exposed through a load balancer, by the ingress address of
// its per-pod Service, which is named after the pod.
func withoutHiddenHosts(c *controller.Context, cluster *psmdbv1.PerconaServerMongoDB, hosts string) (string, error) {
	if cluster.Spec.Sharding.Enabled || !strings.Contains(hosts, ",") {
		return hosts, nil
	}

	pods := &corev1.PodList{}
	if err := c.List(pods, client.MatchingLabels{
		naming.LabelKubernetesInstance:  cluster.Name,
		naming.LabelKubernetesComponent: naming.ComponentHidden,
	}); err != nil {
		return "", fmt.Errorf("list hidden pods: %w", err)
	}
	if len(pods.Items) == 0 {
		return hosts, nil
	}

	var hiddenPrefixes []string
	for _, pod := range pods.Items {
		hiddenPrefixes = append(hiddenPrefixes, pod.Name+".")

		svc := &corev1.Service{}
		if err := c.Get(svc, pod.Name); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return "", fmt.Errorf("get service of hidden pod %s: %w", pod.Name, err)
		}
		for _, ingress := range svc.Status.LoadBalancer.Ingress {
			if ingress.IP != "" {
				hiddenPrefixes = append(hiddenPrefixes, ingress.IP+":")
			}
			if ingress.Hostname != "" {
				hiddenPrefixes = append(hiddenPrefixes, ingress.Hostname+":")
			}
		}
	}

	kept := make([]string, 0)
	for _, host := range strings.Split(hosts, ",") {
		isHidden := slices.ContainsFunc(hiddenPrefixes, func(prefix string) bool {
			return strings.HasPrefix(host, prefix)
		})
		if !isHidden {
			kept = append(kept, host)
		}
	}
	return strings.Join(kept, ","), nil
}

// hiddenMembersServing reports whether the engine CR runs hidden members and
// one of them is ready. The operator's status does not count hidden members
// apart from the others, so their pods are checked directly.
func hiddenMembersServing(c *controller.Context, cluster *psmdbv1.PerconaServerMongoDB) (bool, error) {
	if !slices.ContainsFunc(cluster.Spec.Replsets, func(rs *psmdbv1.ReplsetSpec) bool {
		return rs.Hidden.Enabled && rs.Hidden.Size > 0
	}) {
		return false, nil
	}
	pods := &corev1.PodList{}
	if err := c.List(pods, client.MatchingLabels{
		controller.InstanceLabel:  c.Name(),
		controller.ComponentLabel: common.ComponentHidden,
	}); err != nil {
		return false, fmt.Errorf("list hidden pods: %w", err)
	}
	return slices.ContainsFunc(pods.Items, func(pod corev1.Pod) bool {
		return slices.ContainsFunc(pod.Status.Conditions, func(cond corev1.PodCondition) bool {
			return cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue
		})
	}), nil
}
