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
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	"github.com/percona/percona-server-mongodb-operator/pkg/naming"
	psmdbmongo "github.com/percona/percona-server-mongodb-operator/pkg/psmdb/mongo"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
)

// Reasons of the problems the provider reports. A problem's reason becomes
// the status word users read, so each names one cause.
const (
	// reasonPausedOutsideOpenEverest: spec.pause is set on the
	// PerconaServerMongoDB object. An Instance cannot ask for that, so it was
	// set there by hand.
	reasonPausedOutsideOpenEverest = "PausedOutsideOpenEverest"
	// reasonAuthenticationFailed: mongod rejects the operator's credentials.
	reasonAuthenticationFailed = "AuthenticationFailed"
	// reasonNoPrimary: a replica set has no primary, so it takes no writes.
	reasonNoPrimary = "NoPrimary"
	// reasonConfigurationRejected: the operator refuses the cluster's
	// configuration.
	reasonConfigurationRejected = "ConfigurationRejected"
	// reasonTLSCertificatesNotReady: the operator cannot get the cluster's
	// certificates.
	reasonTLSCertificatesNotReady = "TLSCertificatesNotReady"
)

// Reasons explaining why clients cannot connect, or why that is unknown.
// reasonNoPrimary is one of them too.
const (
	reasonPaused                  = "Paused"
	reasonRestoring               = corev1alpha1.ConditionRestoring
	reasonNoShardJoined           = "NoShardJoined"
	reasonNoMongosReady           = "NoMongosReady"
	reasonMemberStatesNotReported = "MemberStatesNotReported"
	reasonMongosNotReported       = "MongosNotReported"
)

// reasonRollingOutImage is the reason of the Upgrading operation.
const reasonRollingOutImage = "RollingOutImage"

// Fragments of operator error messages the provider recognizes.
const (
	// The MongoDB driver reports a replica set it cannot find a primary in
	// with this topology name.
	errFragmentNoPrimary = "ReplicaSetNoPrimary"
	// The operator gives a restarted pod a fixed time to become ready, fails
	// its pass when the pod takes longer, and picks the rolling update up
	// again on the next pass.
	errFragmentPodRestartTimeout = "reach pod wait limit"
)

// upstreamErrorReasons names the operator errors a user can act on, by a
// fragment of their message. The first match wins.
var upstreamErrorReasons = []struct{ fragment, reason string }{
	{"AuthenticationFailed", reasonAuthenticationFailed},
	{errFragmentNoPrimary, reasonNoPrimary},
	{"CrashLoopBackOff", corev1alpha1.ReasonCrashLoopBackOff},
	{"wrong psmdb options", reasonConfigurationRejected},
	{"TLS secrets handler", reasonTLSCertificatesNotReady},
}

// restoreInProgress describes a restore that owns the cluster for now.
type restoreInProgress struct {
	// reason and message go on the Instance's Restoring condition.
	reason, message string
	// seeding is true for the restore that fills a new Instance from its
	// data source.
	seeding bool
	// loadingData is true once the operator is restoring. It is false while a
	// new Instance still waits for its cluster to come up before the restore
	// can start.
	loadingData bool
}

// StatusPSMDB reports what the provider observes about the cluster: whether
// it runs what the Instance asks for, whether clients can connect, what is
// wrong and which long operation is running. The runtime derives the
// Instance's phase and conditions from these facts.
func StatusPSMDB(c *controller.Context) (controller.Status, error) {
	psmdb, err := uncachedCluster(c)
	if apierrors.IsNotFound(err) {
		return controller.Progressing("Waiting for the PerconaServerMongoDB object to be created"), nil
	}
	if err != nil {
		return controller.Status{}, err
	}

	restore, err := runningRestore(c)
	if err != nil {
		return controller.Status{}, err
	}

	sets := &appsv1.StatefulSetList{}
	if err := c.List(sets, client.MatchingLabels(clusterStatefulSetLabels(psmdb.Name))); err != nil {
		return controller.Status{}, fmt.Errorf("list StatefulSets of cluster %q: %w", psmdb.Name, err)
	}

	status := observe(psmdb, sets.Items, restore)

	// The connection details are reported as soon as clients can connect,
	// not only once the cluster has converged: whoever is told the database
	// is available must find where to connect, and the details must follow a
	// change that is still rolling out.
	if !status.Converged && (status.Available != controller.Available || psmdb.Status.Host == "") {
		return status, nil
	}

	details, err := buildConnectionDetails(c, psmdb)
	if err != nil {
		// The users Secret is written by the operator, so its absence is
		// expected to pass. Returning the error has the runtime retry, which
		// nothing else would trigger: the provider does not watch Secrets.
		return controller.Status{}, fmt.Errorf("build connection details: %w", err)
	}
	status.ConnectionDetails = details
	return status, nil
}

// uncachedCluster reads the PerconaServerMongoDB object from the API server
// instead of the cache. Sync has just applied it, and the cache may still
// hold the object from before that: its status would then look caught up with
// a generation that is no longer the latest, and the Instance would read as
// ready for a change the operator has not seen. The client reads unstructured
// objects without the cache.
func uncachedCluster(c *controller.Context) (*psmdbv1.PerconaServerMongoDB, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(psmdbv1.SchemeGroupVersion.WithKind("PerconaServerMongoDB"))
	if err := c.Get(obj, c.Name()); err != nil {
		return nil, fmt.Errorf("get PerconaServerMongoDB %q: %w", c.Name(), err)
	}

	raw, err := obj.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("encode PerconaServerMongoDB %q: %w", c.Name(), err)
	}
	psmdb := &psmdbv1.PerconaServerMongoDB{}
	if err := json.Unmarshal(raw, psmdb); err != nil {
		return nil, fmt.Errorf("decode PerconaServerMongoDB %q: %w", c.Name(), err)
	}
	return psmdb, nil
}

// runningRestore returns the restore that is in progress for the Instance, or
// nil. A restore drives the engine through intermediate and momentarily ready
// states, so the Restore object reaching a terminal state is what ends it,
// not what the engine reports meanwhile.
func runningRestore(c *controller.Context) (*restoreInProgress, error) {
	if ds := c.GetDataSourceStatus(); ds != nil && !ds.Done {
		message := ds.Message
		if message == "" {
			message = "The instance is being seeded from its data source"
		}
		return &restoreInProgress{
			reason:      corev1alpha1.ReasonSeeding,
			message:     message,
			seeding:     true,
			loadingData: ds.State == controller.DataSourceStateRestoring,
		}, nil
	}

	name, err := activeRestoreName(c)
	if err != nil || name == "" {
		return nil, err
	}
	return &restoreInProgress{
		reason:      corev1alpha1.ConditionRestoring,
		message:     fmt.Sprintf("Restore %s is running", name),
		loadingData: true,
	}, nil
}

// activeRestoreName returns the name of a Restore targeting the Instance that
// has not reached a terminal state, or "" when there is none.
func activeRestoreName(c *controller.Context) (string, error) {
	restores := &backupv1alpha1.RestoreList{}
	if err := c.List(restores, client.MatchingFields{controller.IndexRestoreInstanceName: c.Name()}); err != nil {
		return "", fmt.Errorf("list Restore resources for instance %q: %w", c.Name(), err)
	}

	names := make([]string, 0, len(restores.Items))
	for i := range restores.Items {
		r := &restores.Items[i]
		if !r.DeletionTimestamp.IsZero() {
			continue
		}
		switch r.Status.State {
		case backupv1alpha1.RestoreStateSucceeded, backupv1alpha1.RestoreStateFailed:
		default:
			names = append(names, r.Name)
		}
	}
	if len(names) == 0 {
		return "", nil
	}
	return slices.Min(names), nil
}

// observe turns the PerconaServerMongoDB object, the cluster's StatefulSets
// and the restore in progress, if any, into the facts the provider reports.
func observe(psmdb *psmdbv1.PerconaServerMongoDB, sets []appsv1.StatefulSet, restore *restoreInProgress) controller.Status {
	var status controller.Status
	status.Available, status.AvailableReason, status.AvailableMessage = availability(psmdb, restore)

	if psmdb.Spec.Pause {
		// The provider never sets spec.pause and does not own the field, so
		// its Sync leaves it alone: only whoever set it can start the
		// database again.
		status = status.WithTerminalProblem(reasonPausedOutsideOpenEverest,
			"The cluster is paused: spec.pause is set on its PerconaServerMongoDB object, outside OpenEverest. Unset it to start the database again.")
	}

	// While data is loaded the operator stops and restarts mongod, and fails
	// passes for as long as it cannot reach it. Those errors come with the
	// restore; one that outlives it is reported once the restore has ended.
	if message, failing := upstreamError(psmdb); failing && (restore == nil || !restore.loadingData) {
		// Never terminal, even for an error only a person can fix: the
		// operator does not say which generation its error is about, so
		// right after a fix is applied the error still showing may be the
		// one being fixed, and the Instance must not read as stalled then.
		status = status.WithProblem(upstreamErrorReason(message), message)
	}

	if restore != nil {
		status = status.WithOperation(corev1alpha1.ConditionRestoring, restore.reason, restore.message)
	}

	status.Message = waitingFor(psmdb, sets, restore)
	status.Converged = status.Message == ""

	// The operator records the image the members run only after a rollout, so
	// a different spec.image means an engine version change is rolling or
	// about to.
	if running := psmdb.Status.MongoImage; !status.Converged && running != "" && running != psmdb.Spec.Image {
		message := "Rolling out image " + psmdb.Spec.Image
		if version := psmdb.Status.MongoVersion; version != "" {
			message += fmt.Sprintf(" (the cluster was running version %s)", version)
		}
		status = status.WithOperation(corev1alpha1.ConditionUpgrading, reasonRollingOutImage, message)
	}

	return status
}

// upstreamError returns the error the operator reports for its last pass
// over the cluster, and whether it counts as a failure.
func upstreamError(psmdb *psmdbv1.PerconaServerMongoDB) (string, bool) {
	if psmdb.Status.State != psmdbv1.AppStateError {
		return "", false
	}
	message := strings.TrimSpace(strings.TrimPrefix(psmdb.Status.Message, "Error: "))
	if message == "" {
		message = "The operator reports an error without a message"
	}
	// A pod that restarts slowly is not a failure: the operator carries on
	// with the next pass. A pod that cannot start at all is reported by the
	// runtime from the pod itself.
	return message, !strings.Contains(message, errFragmentPodRestartTimeout)
}

// upstreamErrorReason picks the reason for an operator error from its message.
func upstreamErrorReason(message string) string {
	for _, known := range upstreamErrorReasons {
		if strings.Contains(message, known.fragment) {
			return known.reason
		}
	}
	return corev1alpha1.ReasonEngineError
}

// waitingFor says what keeps the cluster from running what the Instance asks
// for, in the operator's own words where it has any. It returns "" when
// nothing does: the operator reports ready for the latest generation and
// every StatefulSet has rolled out.
func waitingFor(psmdb *psmdbv1.PerconaServerMongoDB, sets []appsv1.StatefulSet, restore *restoreInProgress) string {
	state := psmdb.Status.State
	switch {
	case psmdb.Spec.Pause:
		return "The cluster is paused on its PerconaServerMongoDB object"
	case restore != nil:
		return restore.message
	case state == psmdbv1.AppStateError:
		message, failing := upstreamError(psmdb)
		if !failing {
			return "Waiting for a pod to restart"
		}
		return "The operator reports an error: " + message
	case psmdb.Status.ObservedGeneration == 0:
		return "Waiting for the operator to start creating the cluster"
	case psmdb.Status.ObservedGeneration < psmdb.Generation:
		return "Waiting for the operator to apply the latest configuration"
	case state != psmdbv1.AppStateReady:
		return notReadyWait(psmdb, sets)
	}

	// The operator decides that nothing is rolling from the StatefulSets'
	// status. Right after it changes a pod template that status still
	// describes the previous one, so it can report ready for the latest
	// generation before the rolling restart has started.
	if wait := rolloutWait(sets); wait != "" {
		return "Waiting for " + wait
	}
	return ""
}

// notReadyWait says what a cluster the operator does not report ready is
// waiting for, from what the operator publishes about each replica set and
// about mongos.
func notReadyWait(psmdb *psmdbv1.PerconaServerMongoDB, sets []appsv1.StatefulSet) string {
	var waits []string
	for _, rs := range replicaSets(psmdb) {
		st, reported := psmdb.Status.Replsets[rs]
		label := replicaSetLabel(psmdb, rs)
		switch {
		case !reported:
			waits = append(waits, label+" to be created")
		case st.Message != "":
			waits = append(waits, fmt.Sprintf("%s (%s)", label, strings.TrimRight(st.Message, "; ")))
		case st.Ready < st.Size:
			waits = append(waits, fmt.Sprintf("%s (%d of %d members ready)", label, st.Ready, st.Size))
		case !st.Initialized:
			waits = append(waits, label+" to be initialized")
		}
	}
	if psmdb.Spec.Sharding.Enabled {
		want := 0
		if spec := psmdb.Spec.Sharding.Mongos; spec != nil {
			want = int(spec.Size)
		}
		switch mongos := psmdb.Status.Mongos; {
		case mongos == nil:
			waits = append(waits, "mongos to be created")
		case mongos.Message != "":
			waits = append(waits, fmt.Sprintf("mongos (%s)", strings.TrimRight(mongos.Message, "; ")))
		case mongos.Ready < want:
			waits = append(waits, fmt.Sprintf("mongos (%d of %d routers ready)", mongos.Ready, want))
		}
	}
	if len(waits) > 0 {
		return "Waiting for " + strings.Join(waits, "; ")
	}

	if wait := rolloutWait(sets); wait != "" {
		return "Waiting for " + wait
	}
	if cond := psmdb.Status.FindCondition(psmdbv1.ConditionTypePendingSmartUpdate); cond != nil && cond.Status == psmdbv1.ConditionTrue {
		return "Waiting for the operator to start a rolling update of the pods"
	}
	return fmt.Sprintf("Waiting for the operator to report the cluster ready (it reports %s)", psmdb.Status.State)
}

// rolloutWait returns what the first StatefulSet that has not finished
// rolling out is waiting for, or "" when all have.
//
// The operator replaces pods itself, so its StatefulSets use the OnDelete
// strategy. Their currentRevision then never moves, and a rollout is complete
// when the controller has seen the latest pod template and every pod runs it.
func rolloutWait(sets []appsv1.StatefulSet) string {
	sets = slices.Clone(sets)
	slices.SortFunc(sets, func(a, b appsv1.StatefulSet) int { return strings.Compare(a.Name, b.Name) })

	for i := range sets {
		set := &sets[i]
		if !set.DeletionTimestamp.IsZero() {
			continue
		}
		want := int32(1)
		if set.Spec.Replicas != nil {
			want = *set.Spec.Replicas
		}
		st := set.Status
		switch {
		case st.ObservedGeneration < set.Generation:
			return fmt.Sprintf("StatefulSet %s to pick up its new pod template", set.Name)
		case st.Replicas != want:
			return fmt.Sprintf("StatefulSet %s to scale to %d pods (it has %d)", set.Name, want, st.Replicas)
		case st.UpdatedReplicas < st.Replicas:
			return fmt.Sprintf("StatefulSet %s to roll out (%d of %d pods updated)", set.Name, st.UpdatedReplicas, st.Replicas)
		case st.ReadyReplicas < st.Replicas:
			return fmt.Sprintf("StatefulSet %s to become ready (%d of %d pods ready)", set.Name, st.ReadyReplicas, st.Replicas)
		}
	}
	return ""
}

// availability says whether clients can connect: every replica set, and the
// config servers of a sharded cluster, need a primary to take reads and
// writes of all the data, and a sharded cluster needs a shard that has joined
// it and a mongos to route to it.
//
// It reads the member states the operator recorded on its last complete pass
// over each replica set. That makes the answer as old as that pass: the
// operator keeps the last report while it replaces pods or fails early in a
// pass, so a cluster that lost a primary since then still reads as available
// until the operator looks again. A replica set whose member states the
// operator left out is reported as unknown, never guessed from pod counts:
// ready pods do not make a primary.
func availability(psmdb *psmdbv1.PerconaServerMongoDB, restore *restoreInProgress) (controller.Availability, string, string) {
	switch {
	case psmdb.Spec.Pause, psmdb.Status.State == psmdbv1.AppStatePaused:
		return controller.Unavailable, reasonPaused, "The cluster is paused"
	case restore != nil && restore.seeding:
		return controller.Unavailable, corev1alpha1.ReasonLoadingData, "The instance is being seeded from its data source"
	case restore != nil:
		return controller.Unavailable, reasonRestoring, "A restore is replacing the data"
	}

	sharded := psmdb.Spec.Sharding.Enabled
	shards := 0
	var down, unreported []string
	for _, rs := range replicaSets(psmdb) {
		st := psmdb.Status.Replsets[rs]
		if sharded && rs != psmdbv1.ConfigReplSetName {
			// A shard that has not joined the cluster holds no data and gets
			// no requests. One being added does not make the cluster any
			// less available.
			if st.AddedAsShard == nil || !*st.AddedAsShard {
				continue
			}
			shards++
		}
		label := replicaSetLabel(psmdb, rs)
		switch {
		case hasPrimary(st):
		case !st.Initialized:
			down = append(down, label+" has not been initialized")
		case len(st.Members) == 0:
			unreported = append(unreported, label)
		default:
			down = append(down, label+" has no primary")
		}
	}
	if len(down) > 0 {
		return controller.Unavailable, reasonNoPrimary, sentence(strings.Join(down, "; "))
	}
	if sharded && shards == 0 {
		return controller.Unavailable, reasonNoShardJoined, "No shard has joined the cluster yet"
	}

	// The operator drops the member states of the replica sets it could not
	// find a primary in, so those are the ones its error is about.
	if message, failing := upstreamError(psmdb); failing && strings.Contains(message, errFragmentNoPrimary) {
		switch len(unreported) {
		case 0:
			return controller.Unavailable, reasonNoPrimary, "The operator found no primary in a replica set"
		case 1:
			return controller.Unavailable, reasonNoPrimary, "The operator found no primary in " + unreported[0]
		default:
			return controller.Unavailable, reasonNoPrimary,
				"The operator found no primary in at least one of: " + strings.Join(unreported, ", ")
		}
	}

	mongos := psmdb.Status.Mongos
	if sharded && mongos != nil && mongos.Ready < 1 {
		return controller.Unavailable, reasonNoMongosReady, "No mongos router is ready"
	}

	if len(unreported) > 0 {
		return controller.AvailabilityUnknown, reasonMemberStatesNotReported,
			"The operator has not reported the member states of " + strings.Join(unreported, ", ")
	}
	if sharded && mongos == nil {
		return controller.AvailabilityUnknown, reasonMongosNotReported, "The operator has not reported on the mongos routers"
	}

	return controller.Available, "", ""
}

// hasPrimary reports whether the operator recorded a member in state PRIMARY.
func hasPrimary(st psmdbv1.ReplsetStatus) bool {
	for _, member := range st.Members {
		if member.State == psmdbmongo.MemberStatePrimary {
			return true
		}
	}
	return false
}

// replicaSets returns the names of the cluster's replica sets as the operator
// keys them in its status: the shards, then the config servers when sharded.
func replicaSets(psmdb *psmdbv1.PerconaServerMongoDB) []string {
	names := make([]string, 0, len(psmdb.Spec.Replsets)+1)
	for _, rs := range psmdb.Spec.Replsets {
		if rs != nil {
			names = append(names, rs.Name)
		}
	}
	if psmdb.Spec.Sharding.Enabled && psmdb.Spec.Sharding.ConfigsvrReplSet != nil {
		// The operator names the config server replica set itself, whatever
		// name the object gives it.
		names = append(names, psmdbv1.ConfigReplSetName)
	}
	return names
}

// replicaSetLabel names a replica set in a message.
func replicaSetLabel(psmdb *psmdbv1.PerconaServerMongoDB, name string) string {
	if psmdb.Spec.Sharding.Enabled && name == psmdbv1.ConfigReplSetName {
		return "config server replica set " + name
	}
	return "replica set " + name
}

// sentence capitalizes the first letter of s.
func sentence(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// clusterStatefulSetLabels selects the StatefulSets the operator runs for a
// cluster: mongod, config servers, mongos and hidden members.
func clusterStatefulSetLabels(cluster string) map[string]string {
	return map[string]string{
		naming.LabelKubernetesInstance:  cluster,
		naming.LabelKubernetesManagedBy: naming.Labels()[naming.LabelKubernetesManagedBy],
	}
}

// enqueueStatefulSetInstance maps a StatefulSet the operator runs for a
// cluster to the Instance of that cluster. Whether the Instance has converged
// depends on its StatefulSets having rolled out, and the operator's own
// status may not change again once they have.
func enqueueStatefulSetInstance() func(ctx context.Context, obj client.Object) []reconcile.Request {
	return func(_ context.Context, obj client.Object) []reconcile.Request {
		labels := obj.GetLabels()
		cluster := labels[naming.LabelKubernetesInstance]
		if cluster == "" || !naming.IsManagedByOperator(labels) {
			return nil
		}
		return []reconcile.Request{{
			NamespacedName: types.NamespacedName{Namespace: obj.GetNamespace(), Name: cluster},
		}}
	}
}
