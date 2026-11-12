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
	"fmt"
	"testing"

	"github.com/AlekSi/pointer"
	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	"github.com/percona/percona-server-mongodb-operator/pkg/naming"
	psmdbmongo "github.com/percona/percona-server-mongodb-operator/pkg/psmdb/mongo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
)

const (
	statusTestName      = "my-mongo"
	statusTestNamespace = "db"
	statusTestImage     = "percona/percona-server-mongodb:8.0.12-4"
	statusTestHost      = "my-mongo-rs0.db.svc.cluster.local"
)

// statusContext builds a context for an Instance named my-mongo and seeds the
// cluster with objs, plus the users Secret the connection details are read
// from. seeding is what Sync staged about the data source, if anything.
func statusContext(t *testing.T, seeding *controller.DataSourceStatus, objs ...client.Object) *controller.Context {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1alpha1.AddToScheme(scheme))
	require.NoError(t, backupv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, psmdbv1.SchemeBuilder.AddToScheme(scheme))

	in := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: statusTestName, Namespace: statusTestNamespace},
	}
	users := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "everest-secrets-" + statusTestName, Namespace: statusTestNamespace},
		Data: map[string][]byte{
			psmdbv1.EnvMongoDBDatabaseAdminUser:     []byte("admin"),
			psmdbv1.EnvMongoDBDatabaseAdminPassword: []byte("secret"),
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(append(objs, in, users)...).
		WithIndex(&backupv1alpha1.Restore{}, controller.IndexRestoreInstanceName, func(obj client.Object) []string {
			return []string{obj.(*backupv1alpha1.Restore).Spec.InstanceRef.Name}
		}).
		Build()

	c := controller.NewContext(context.Background(), cl, in, "psmdb")
	if seeding != nil {
		c.SetDataSourceStatus(*seeding)
	}
	return c
}

// members returns the member states of a replica set as the operator records
// them, one per pod.
func members(rs string, states ...psmdbmongo.MemberState) map[string]psmdbv1.ReplsetMemberStatus {
	out := make(map[string]psmdbv1.ReplsetMemberStatus, len(states))
	for i, state := range states {
		pod := fmt.Sprintf("%s-%s-%d", statusTestName, rs, i)
		out[pod] = psmdbv1.ReplsetMemberStatus{
			Name:     pod + ":27017",
			State:    state,
			StateStr: psmdbmongo.MemberStateStrings[state],
		}
	}
	return out
}

// healthyReplset is the status of a three-member replica set with a primary.
func healthyReplset(rs string) psmdbv1.ReplsetStatus {
	return psmdbv1.ReplsetStatus{
		Initialized: true,
		Size:        3,
		Ready:       3,
		Status:      psmdbv1.AppStateReady,
		Members: members(rs,
			psmdbmongo.MemberStatePrimary, psmdbmongo.MemberStateSecondary, psmdbmongo.MemberStateSecondary),
	}
}

// replicaSetCluster returns a replica set cluster the operator reports ready
// for its latest generation. Tests change what they are about.
func replicaSetCluster(changes ...func(*psmdbv1.PerconaServerMongoDB)) *psmdbv1.PerconaServerMongoDB {
	psmdb := &psmdbv1.PerconaServerMongoDB{
		ObjectMeta: metav1.ObjectMeta{Name: statusTestName, Namespace: statusTestNamespace, Generation: 3},
		Spec: psmdbv1.PerconaServerMongoDBSpec{
			Image:    statusTestImage,
			Replsets: []*psmdbv1.ReplsetSpec{{Name: "rs0", Size: 3}},
		},
		Status: psmdbv1.PerconaServerMongoDBStatus{
			State:              psmdbv1.AppStateReady,
			ObservedGeneration: 3,
			Host:               statusTestHost,
			MongoImage:         statusTestImage,
			MongoVersion:       "8.0.12-4",
			Size:               3,
			Ready:              3,
			Replsets:           map[string]psmdbv1.ReplsetStatus{"rs0": healthyReplset("rs0")},
		},
	}
	for _, change := range changes {
		change(psmdb)
	}
	return psmdb
}

// shardedCluster returns a sharded cluster with two shards that the operator
// reports ready for its latest generation.
func shardedCluster(changes ...func(*psmdbv1.PerconaServerMongoDB)) *psmdbv1.PerconaServerMongoDB {
	return replicaSetCluster(append([]func(*psmdbv1.PerconaServerMongoDB){
		func(psmdb *psmdbv1.PerconaServerMongoDB) {
			psmdb.Spec.Replsets = append(psmdb.Spec.Replsets, &psmdbv1.ReplsetSpec{Name: "rs1", Size: 3})
			psmdb.Spec.Sharding = psmdbv1.Sharding{
				Enabled: true,
				// The operator runs the config servers as "cfg" whatever
				// name they are given.
				ConfigsvrReplSet: &psmdbv1.ReplsetSpec{Name: "configsvr", Size: 3},
				Mongos:           &psmdbv1.MongosSpec{Size: 2},
			}
			psmdb.Status.Host = "my-mongo-mongos.db.svc.cluster.local"
			psmdb.Status.Replsets["rs1"] = healthyReplset("rs1")
			psmdb.Status.Replsets["cfg"] = healthyReplset("cfg")
			for _, shard := range []string{"rs0", "rs1"} {
				st := psmdb.Status.Replsets[shard]
				st.AddedAsShard = pointer.ToBool(true)
				psmdb.Status.Replsets[shard] = st
			}
			psmdb.Status.Mongos = &psmdbv1.MongosStatus{Size: 2, Ready: 2, Status: psmdbv1.AppStateReady}
		},
	}, changes...)...)
}

// replset changes the status the operator publishes for one replica set.
func replset(name string, change func(*psmdbv1.ReplsetStatus)) func(*psmdbv1.PerconaServerMongoDB) {
	return func(psmdb *psmdbv1.PerconaServerMongoDB) {
		st := psmdb.Status.Replsets[name]
		change(&st)
		psmdb.Status.Replsets[name] = st
	}
}

func withMembers(rs string, states ...psmdbmongo.MemberState) func(*psmdbv1.PerconaServerMongoDB) {
	return replset(rs, func(st *psmdbv1.ReplsetStatus) { st.Members = members(rs, states...) })
}

func state(state psmdbv1.AppState) func(*psmdbv1.PerconaServerMongoDB) {
	return func(psmdb *psmdbv1.PerconaServerMongoDB) { psmdb.Status.State = state }
}

// failing puts the cluster in the state the operator leaves it in when a pass
// over it fails.
func failing(message string) func(*psmdbv1.PerconaServerMongoDB) {
	return func(psmdb *psmdbv1.PerconaServerMongoDB) {
		psmdb.Status.State = psmdbv1.AppStateError
		psmdb.Status.Message = "Error: " + message
	}
}

// rolledOut returns one of the cluster's StatefulSets with every pod ready
// and running its latest template.
func rolledOut(name string, changes ...func(*appsv1.StatefulSet)) *appsv1.StatefulSet {
	set := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:       statusTestName + "-" + name,
			Namespace:  statusTestNamespace,
			Generation: 2,
			Labels:     clusterStatefulSetLabels(statusTestName),
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:       pointer.ToInt32(3),
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType},
		},
		Status: appsv1.StatefulSetStatus{
			ObservedGeneration: 2,
			Replicas:           3,
			ReadyReplicas:      3,
			UpdatedReplicas:    3,
			// With OnDelete the controller never moves currentRevision.
			CurrentRevision: statusTestName + "-" + name + "-1",
			UpdateRevision:  statusTestName + "-" + name + "-2",
		},
	}
	for _, change := range changes {
		change(set)
	}
	return set
}

func restoreInState(name string, state backupv1alpha1.RestoreState) *backupv1alpha1.Restore {
	return &backupv1alpha1.Restore{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: statusTestNamespace},
		Spec:       backupv1alpha1.RestoreSpec{InstanceRef: commonv1alpha1.ObjectRef{Name: statusTestName}},
		Status:     backupv1alpha1.RestoreStatus{State: state},
	}
}

func TestStatus(t *testing.T) {
	t.Parallel()

	const (
		primary   = psmdbmongo.MemberStatePrimary
		secondary = psmdbmongo.MemberStateSecondary
		down      = psmdbmongo.MemberStateDown
	)

	tests := []struct {
		name string
		// cluster is the PerconaServerMongoDB object; nil when it does not
		// exist.
		cluster *psmdbv1.PerconaServerMongoDB
		// sets are the cluster's StatefulSets; nil stands for the replica
		// set's StatefulSet, rolled out.
		sets     []*appsv1.StatefulSet
		restores []*backupv1alpha1.Restore
		seeding  *controller.DataSourceStatus

		converged bool
		// message is what the provider waits for, in part.
		message string
		// available, availableReason and availableMessage (in part) say
		// whether clients can connect.
		available        controller.Availability
		availableReason  string
		availableMessage string
		// notInAvailableMessage must not appear in that message.
		notInAvailableMessage []string
		// problems are the reported problems, messages in part.
		problems []controller.Problem
		// operations are the reported operations, as "Type/Reason".
		operations []string
	}{
		{
			name:    "the cluster object does not exist yet",
			cluster: nil,
			message: "Waiting for the PerconaServerMongoDB object to be created",
			// The provider cannot tell, so it leaves the answer to the runtime.
			available: controller.AvailabilityUnreported,
		},
		{
			name: "a new cluster the operator has not picked up",
			cluster: replicaSetCluster(func(psmdb *psmdbv1.PerconaServerMongoDB) {
				psmdb.Generation = 1
				psmdb.Status = psmdbv1.PerconaServerMongoDBStatus{}
			}),
			sets:             []*appsv1.StatefulSet{},
			message:          "Waiting for the operator to start creating the cluster",
			available:        controller.Unavailable,
			availableReason:  reasonNoPrimary,
			availableMessage: "Replica set rs0 has not been initialized",
		},
		{
			name: "a new cluster coming up",
			cluster: replicaSetCluster(state(psmdbv1.AppStateInit), replset("rs0", func(st *psmdbv1.ReplsetStatus) {
				*st = psmdbv1.ReplsetStatus{Size: 3, Ready: 1, Status: psmdbv1.AppStateInit}
			})),
			sets: []*appsv1.StatefulSet{rolledOut("rs0", func(set *appsv1.StatefulSet) {
				set.Status.ReadyReplicas = 1
			})},
			message:          "Waiting for replica set rs0 (1 of 3 members ready)",
			available:        controller.Unavailable,
			availableReason:  reasonNoPrimary,
			availableMessage: "Replica set rs0 has not been initialized",
		},
		{
			name:      "ready and caught up",
			cluster:   replicaSetCluster(),
			converged: true,
			available: controller.Available,
		},
		{
			name: "ready but the operator has not seen the latest generation",
			cluster: replicaSetCluster(func(psmdb *psmdbv1.PerconaServerMongoDB) {
				psmdb.Generation = 4
			}),
			message:   "Waiting for the operator to apply the latest configuration",
			available: controller.Available,
		},
		{
			name:    "ready but a StatefulSet has not picked up its new pod template",
			cluster: replicaSetCluster(),
			sets: []*appsv1.StatefulSet{rolledOut("rs0", func(set *appsv1.StatefulSet) {
				set.Generation = 3
			})},
			message:   "Waiting for StatefulSet my-mongo-rs0 to pick up its new pod template",
			available: controller.Available,
		},
		{
			name:    "ready but the rolling restart has not finished",
			cluster: replicaSetCluster(),
			sets: []*appsv1.StatefulSet{rolledOut("rs0", func(set *appsv1.StatefulSet) {
				set.Status.UpdatedReplicas = 1
			})},
			message:   "Waiting for StatefulSet my-mongo-rs0 to roll out (1 of 3 pods updated)",
			available: controller.Available,
		},
		{
			name: "a rolling restart with a primary still present",
			cluster: replicaSetCluster(
				state(psmdbv1.AppStateInit),
				replset("rs0", func(st *psmdbv1.ReplsetStatus) {
					st.Ready = 2
					st.Status = psmdbv1.AppStateInit
				}),
				withMembers("rs0", down, primary, secondary),
			),
			sets: []*appsv1.StatefulSet{rolledOut("rs0", func(set *appsv1.StatefulSet) {
				set.Status.UpdatedReplicas = 1
				set.Status.ReadyReplicas = 2
			})},
			message:   "Waiting for replica set rs0 (2 of 3 members ready)",
			available: controller.Available,
		},
		{
			name: "a rolling restart between two pods",
			cluster: replicaSetCluster(
				state(psmdbv1.AppStateInit),
				replset("rs0", func(st *psmdbv1.ReplsetStatus) { st.Status = psmdbv1.AppStateInit }),
			),
			sets: []*appsv1.StatefulSet{rolledOut("rs0", func(set *appsv1.StatefulSet) {
				set.Status.UpdatedReplicas = 2
			})},
			message:   "Waiting for StatefulSet my-mongo-rs0 to roll out (2 of 3 pods updated)",
			available: controller.Available,
		},
		{
			name: "a pod that restarts slowly fails the operator's pass",
			cluster: replicaSetCluster(failing(
				"reconcile statefulsets: reconcile replset rs0: reconcile StatefulSet for rs0: failed to run smartUpdate: " +
					"failed to apply changes: wait pod restart: reach pod wait limit")),
			message:   "Waiting for a pod to restart",
			available: controller.Available,
		},
		{
			name: "no primary",
			cluster: replicaSetCluster(
				state(psmdbv1.AppStateInit),
				withMembers("rs0", secondary, secondary, down),
			),
			message:          "Waiting for",
			available:        controller.Unavailable,
			availableReason:  reasonNoPrimary,
			availableMessage: "Replica set rs0 has no primary",
		},
		{
			name: "the operator reports no member states",
			cluster: replicaSetCluster(
				state(psmdbv1.AppStateInit),
				withMembers("rs0"),
			),
			message:          "Waiting for",
			available:        controller.AvailabilityUnknown,
			availableReason:  reasonMemberStatesNotReported,
			availableMessage: "replica set rs0",
		},
		{
			name: "the operator finds no primary",
			cluster: replicaSetCluster(
				failing("reconcile statefulsets: handle ReplicaSetNoPrimary: no mongod containers in running state"),
				withMembers("rs0"),
			),
			message:          "The operator reports an error: reconcile statefulsets: handle ReplicaSetNoPrimary",
			available:        controller.Unavailable,
			availableReason:  reasonNoPrimary,
			availableMessage: "The operator found no primary in replica set rs0",
			problems: []controller.Problem{{
				Reason:  reasonNoPrimary,
				Message: "handle ReplicaSetNoPrimary: no mongod containers in running state",
			}},
		},
		{
			name:    "sharded, ready and caught up",
			cluster: shardedCluster(),
			sets: []*appsv1.StatefulSet{
				rolledOut("cfg"), rolledOut("rs0"), rolledOut("rs1"),
				rolledOut("mongos", func(set *appsv1.StatefulSet) {
					set.Spec.Replicas = pointer.ToInt32(2)
					set.Status.Replicas, set.Status.ReadyReplicas, set.Status.UpdatedReplicas = 2, 2, 2
				}),
			},
			converged: true,
			available: controller.Available,
		},
		{
			name: "sharded with one shard without a primary",
			cluster: shardedCluster(
				state(psmdbv1.AppStateInit),
				withMembers("rs1", secondary, secondary, down),
			),
			message:               "Waiting for",
			available:             controller.Unavailable,
			availableReason:       reasonNoPrimary,
			availableMessage:      "Replica set rs1 has no primary",
			notInAvailableMessage: []string{"rs0", "cfg"},
		},
		{
			name: "sharded with the config servers without a primary",
			cluster: shardedCluster(
				state(psmdbv1.AppStateInit),
				withMembers("cfg", secondary, secondary, secondary),
			),
			message:               "Waiting for",
			available:             controller.Unavailable,
			availableReason:       reasonNoPrimary,
			availableMessage:      "Config server replica set cfg has no primary",
			notInAvailableMessage: []string{"rs0", "rs1"},
		},
		{
			name: "sharded with no mongos ready",
			cluster: shardedCluster(state(psmdbv1.AppStateInit), func(psmdb *psmdbv1.PerconaServerMongoDB) {
				psmdb.Status.Mongos = &psmdbv1.MongosStatus{Size: 2, Ready: 0, Status: psmdbv1.AppStateInit}
			}),
			message:          "Waiting for mongos (0 of 2 routers ready)",
			available:        controller.Unavailable,
			availableReason:  reasonNoMongosReady,
			availableMessage: "No mongos router is ready",
		},
		{
			name: "sharded with one mongos ready",
			cluster: shardedCluster(state(psmdbv1.AppStateInit), func(psmdb *psmdbv1.PerconaServerMongoDB) {
				psmdb.Status.Mongos = &psmdbv1.MongosStatus{Size: 2, Ready: 1, Status: psmdbv1.AppStateInit}
			}),
			message:   "Waiting for mongos (1 of 2 routers ready)",
			available: controller.Available,
		},
		{
			name: "sharded, the operator reports no member states for a shard",
			cluster: shardedCluster(
				state(psmdbv1.AppStateInit),
				withMembers("rs1"),
			),
			message:               "Waiting for",
			available:             controller.AvailabilityUnknown,
			availableReason:       reasonMemberStatesNotReported,
			availableMessage:      "replica set rs1",
			notInAvailableMessage: []string{"rs0", "cfg"},
		},
		{
			name: "sharded, a shard being added",
			cluster: shardedCluster(state(psmdbv1.AppStateInit), func(psmdb *psmdbv1.PerconaServerMongoDB) {
				psmdb.Spec.Replsets = append(psmdb.Spec.Replsets, &psmdbv1.ReplsetSpec{Name: "rs2", Size: 3})
				psmdb.Status.Replsets["rs2"] = psmdbv1.ReplsetStatus{Size: 3, Ready: 1, Status: psmdbv1.AppStateInit}
			}),
			message: "Waiting for replica set rs2 (1 of 3 members ready)",
			// The new shard holds no data yet, so nothing is out of reach.
			available: controller.Available,
		},
		{
			name: "sharded, a new cluster whose shards have not joined",
			cluster: shardedCluster(state(psmdbv1.AppStateInit), func(psmdb *psmdbv1.PerconaServerMongoDB) {
				for _, shard := range []string{"rs0", "rs1"} {
					st := psmdb.Status.Replsets[shard]
					st.AddedAsShard = nil
					psmdb.Status.Replsets[shard] = st
				}
			}),
			message:          "Waiting for",
			available:        controller.Unavailable,
			availableReason:  reasonNoShardJoined,
			availableMessage: "No shard has joined the cluster yet",
		},
		{
			name:      "upstream error",
			cluster:   replicaSetCluster(failing("reconcile backup tasks: storage unreachable")),
			message:   "The operator reports an error: reconcile backup tasks: storage unreachable",
			available: controller.Available,
			problems: []controller.Problem{{
				Reason:  corev1alpha1.ReasonEngineError,
				Message: "reconcile backup tasks: storage unreachable",
			}},
		},
		{
			name: "upstream error on a generation the operator has not caught up with",
			cluster: replicaSetCluster(failing("wrong psmdb options: replset size below safe minimum"),
				func(psmdb *psmdbv1.PerconaServerMongoDB) { psmdb.Generation = 4 }),
			message:   "The operator reports an error: wrong psmdb options",
			available: controller.Available,
			problems: []controller.Problem{{
				Reason:  reasonConfigurationRejected,
				Message: "wrong psmdb options: replset size below safe minimum",
			}},
		},
		{
			name: "upstream error from rejected credentials",
			cluster: replicaSetCluster(
				failing("reconcile statefulsets: dial: ping mongo: (AuthenticationFailed) Authentication failed."),
				withMembers("rs0"),
			),
			message:          "The operator reports an error",
			available:        controller.AvailabilityUnknown,
			availableReason:  reasonMemberStatesNotReported,
			availableMessage: "replica set rs0",
			problems: []controller.Problem{{
				Reason:  reasonAuthenticationFailed,
				Message: "(AuthenticationFailed) Authentication failed.",
			}},
		},
		{
			name: "paused upstream",
			cluster: replicaSetCluster(state(psmdbv1.AppStatePaused), func(psmdb *psmdbv1.PerconaServerMongoDB) {
				psmdb.Spec.Pause = true
				psmdb.Status.Replsets["rs0"] = psmdbv1.ReplsetStatus{Initialized: true, Size: 3, Status: psmdbv1.AppStatePaused}
			}),
			sets: []*appsv1.StatefulSet{rolledOut("rs0", func(set *appsv1.StatefulSet) {
				set.Spec.Replicas = pointer.ToInt32(0)
				set.Status.Replicas, set.Status.ReadyReplicas, set.Status.UpdatedReplicas = 0, 0, 0
			})},
			message:          "The cluster is paused on its PerconaServerMongoDB object",
			available:        controller.Unavailable,
			availableReason:  reasonPaused,
			availableMessage: "The cluster is paused",
			problems: []controller.Problem{{
				Reason:   reasonPausedOutsideOpenEverest,
				Message:  "spec.pause is set on its PerconaServerMongoDB object",
				Terminal: true,
			}},
		},
		{
			name: "pause requested upstream, pods still running",
			cluster: replicaSetCluster(func(psmdb *psmdbv1.PerconaServerMongoDB) {
				psmdb.Spec.Pause = true
				psmdb.Generation = 4
			}),
			message:         "The cluster is paused on its PerconaServerMongoDB object",
			available:       controller.Unavailable,
			availableReason: reasonPaused,
			problems: []controller.Problem{{
				Reason:   reasonPausedOutsideOpenEverest,
				Message:  "Unset it to start the database again",
				Terminal: true,
			}},
		},
		{
			name:             "a restore running",
			cluster:          replicaSetCluster(),
			restores:         []*backupv1alpha1.Restore{restoreInState("nightly", backupv1alpha1.RestoreStateRunning)},
			message:          "Restore nightly is running",
			available:        controller.Unavailable,
			availableReason:  reasonRestoring,
			availableMessage: "A restore is replacing the data",
			operations:       []string{"Restoring/Restoring"},
		},
		{
			name: "a restore running while the operator cannot reach mongod",
			cluster: replicaSetCluster(
				failing("reconcile statefulsets: dial: ping mongo: server selection error"),
				withMembers("rs0"),
			),
			restores:        []*backupv1alpha1.Restore{restoreInState("nightly", backupv1alpha1.RestoreStatePending)},
			message:         "Restore nightly is running",
			available:       controller.Unavailable,
			availableReason: reasonRestoring,
			operations:      []string{"Restoring/Restoring"},
		},
		{
			name:    "restores that have ended",
			cluster: replicaSetCluster(),
			restores: []*backupv1alpha1.Restore{
				restoreInState("nightly", backupv1alpha1.RestoreStateSucceeded),
				restoreInState("weekly", backupv1alpha1.RestoreStateFailed),
			},
			converged: true,
			available: controller.Available,
		},
		{
			name: "a new instance waiting for its cluster before it is seeded",
			cluster: replicaSetCluster(failing("TLS secrets handler: \"issuer not found\""),
				replset("rs0", func(st *psmdbv1.ReplsetStatus) { *st = psmdbv1.ReplsetStatus{Size: 3} })),
			seeding: &controller.DataSourceStatus{
				State:   controller.DataSourceStateWaiting,
				Reason:  corev1alpha1.ReasonDataSourceWaitingForCluster,
				Message: "waiting for PerconaServerMongoDB to be Ready and publish a BackupVersion",
			},
			message:          "waiting for PerconaServerMongoDB to be Ready and publish a BackupVersion",
			available:        controller.Unavailable,
			availableReason:  corev1alpha1.ReasonLoadingData,
			availableMessage: "seeded",
			// The restore has not started, so an error is not its doing.
			problems: []controller.Problem{{
				Reason:  reasonTLSCertificatesNotReady,
				Message: "issuer not found",
			}},
			operations: []string{"Restoring/Seeding"},
		},
		{
			name:    "a new instance being seeded",
			cluster: replicaSetCluster(),
			seeding: &controller.DataSourceStatus{
				State:   controller.DataSourceStateRestoring,
				Reason:  corev1alpha1.ReasonDataSourceRestoring,
				Message: "Restore \"my-mongo-datasource\" in progress (state=\"Running\")",
			},
			restores:        []*backupv1alpha1.Restore{restoreInState("my-mongo-datasource", backupv1alpha1.RestoreStateRunning)},
			message:         "Restore \"my-mongo-datasource\" in progress",
			available:       controller.Unavailable,
			availableReason: corev1alpha1.ReasonLoadingData,
			operations:      []string{"Restoring/Seeding"},
		},
		{
			name:    "a new instance that has been seeded",
			cluster: replicaSetCluster(),
			seeding: &controller.DataSourceStatus{
				Done:   true,
				State:  controller.DataSourceStateSucceeded,
				Reason: corev1alpha1.ReasonDataSourceSucceeded,
			},
			restores:  []*backupv1alpha1.Restore{restoreInState("my-mongo-datasource", backupv1alpha1.RestoreStateSucceeded)},
			converged: true,
			available: controller.Available,
		},
		{
			name: "a new engine image rolling out",
			cluster: replicaSetCluster(
				state(psmdbv1.AppStateInit),
				func(psmdb *psmdbv1.PerconaServerMongoDB) {
					psmdb.Spec.Image = "percona/percona-server-mongodb:8.0.19-7"
				},
			),
			sets: []*appsv1.StatefulSet{rolledOut("rs0", func(set *appsv1.StatefulSet) {
				set.Status.UpdatedReplicas = 1
			})},
			message:    "Waiting for StatefulSet my-mongo-rs0 to roll out (1 of 3 pods updated)",
			available:  controller.Available,
			operations: []string{"Upgrading/RollingOutImage"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var objs []client.Object
			if tt.cluster != nil {
				objs = append(objs, tt.cluster)
			}
			sets := tt.sets
			if sets == nil {
				sets = []*appsv1.StatefulSet{rolledOut("rs0")}
			}
			for _, set := range sets {
				objs = append(objs, set)
			}
			for _, restore := range tt.restores {
				objs = append(objs, restore)
			}

			got, err := StatusPSMDB(statusContext(t, tt.seeding, objs...))
			require.NoError(t, err)

			assert.Equal(t, tt.converged, got.Converged, "converged; message: %s", got.Message)
			if tt.message == "" {
				assert.Empty(t, got.Message)
			} else {
				assert.Contains(t, got.Message, tt.message)
			}

			assert.Equal(t, tt.available, got.Available, "available: %s", got.AvailableMessage)
			assert.Equal(t, tt.availableReason, got.AvailableReason)
			if tt.availableMessage == "" && tt.available != controller.Unavailable {
				assert.Empty(t, got.AvailableMessage)
			} else {
				assert.Contains(t, got.AvailableMessage, tt.availableMessage)
			}
			for _, absent := range tt.notInAvailableMessage {
				assert.NotContains(t, got.AvailableMessage, absent)
			}

			require.Len(t, got.Problems, len(tt.problems), "problems: %v", got.Problems)
			for i, want := range tt.problems {
				assert.Equal(t, want.Reason, got.Problems[i].Reason)
				assert.Equal(t, want.Terminal, got.Problems[i].Terminal, "terminal")
				assert.Contains(t, got.Problems[i].Message, want.Message)
			}

			var operations []string
			for _, op := range got.Operations {
				operations = append(operations, op.Type+"/"+op.Reason)
				assert.NotEmpty(t, op.Message)
			}
			assert.Equal(t, tt.operations, operations)

			// Connection details are reported while clients can connect,
			// converged or not, and never for a database that is down.
			if tt.converged || tt.available == controller.Available {
				assert.Equal(t, "mongodb", got.ConnectionDetails.Type)
				assert.Equal(t, tt.cluster.Status.Host, got.ConnectionDetails.Host)
				assert.Equal(t, "admin", got.ConnectionDetails.Username)
			} else {
				assert.True(t, got.ConnectionDetails.IsEmpty())
			}
		})
	}
}

// TestStatusWithoutUsersSecret covers a cluster clients can connect to before
// its users Secret can be read: the provider fails the pass so that the
// runtime retries it, since nothing else would.
func TestStatusWithoutUsersSecret(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cluster *psmdbv1.PerconaServerMongoDB
		wantErr bool
	}{
		{name: "ready", cluster: replicaSetCluster(), wantErr: true},
		{name: "available while a change rolls out", cluster: replicaSetCluster(state(psmdbv1.AppStateInit)), wantErr: true},
		{
			name: "not available",
			cluster: replicaSetCluster(state(psmdbv1.AppStateInit),
				withMembers("rs0", psmdbmongo.MemberStateSecondary)),
		},
		{
			name: "available but the operator has not published a host",
			cluster: replicaSetCluster(state(psmdbv1.AppStateInit), func(psmdb *psmdbv1.PerconaServerMongoDB) {
				psmdb.Status.Host = ""
			}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := statusContext(t, nil, tt.cluster, rolledOut("rs0"))
			users := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "everest-secrets-" + statusTestName, Namespace: statusTestNamespace}}
			require.NoError(t, c.Client().Delete(context.Background(), users))

			got, err := StatusPSMDB(c)
			if tt.wantErr {
				require.ErrorContains(t, err, "build connection details")
				return
			}
			require.NoError(t, err)
			assert.True(t, got.ConnectionDetails.IsEmpty())
		})
	}
}

func TestUpstreamErrorReason(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"reconcile statefulsets: dial: ping mongo: (AuthenticationFailed) Authentication failed.":         reasonAuthenticationFailed,
		"reconcile statefulsets: dial: ping mongo: server selection error: { Type: ReplicaSetNoPrimary }": reasonNoPrimary,
		"reconcile statefulsets: handle ReplicaSetNoPrimary: no mongod containers in running state":       reasonNoPrimary,
		"reconcile statefulsets: pod my-mongo-rs0-1 is in CrashLoopBackOff state":                         corev1alpha1.ReasonCrashLoopBackOff,
		"wrong psmdb options: replset rs0 size below safe minimum":                                        reasonConfigurationRejected,
		`TLS secrets handler: "issuer not found". Please create your TLS secret my-mongo-ssl manually`:    reasonTLSCertificatesNotReady,
		"reconcile backup tasks: there is no storage s3 in cluster my-mongo for nightly task":             corev1alpha1.ReasonEngineError,
		"": corev1alpha1.ReasonEngineError,
	}
	for message, want := range tests {
		t.Run(want+"/"+message, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, want, upstreamErrorReason(message))
		})
	}
}

func TestRolloutWait(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		sets []*appsv1.StatefulSet
		want string
	}{
		{name: "no StatefulSets"},
		{
			name: "every pod runs the latest template",
			sets: []*appsv1.StatefulSet{rolledOut("rs0"), rolledOut("rs0-hidden")},
		},
		{
			name: "the controller has not seen the latest template",
			sets: []*appsv1.StatefulSet{rolledOut("rs0", func(set *appsv1.StatefulSet) {
				set.Generation = 3
			})},
			want: "StatefulSet my-mongo-rs0 to pick up its new pod template",
		},
		{
			name: "pods on the previous template",
			sets: []*appsv1.StatefulSet{rolledOut("rs0", func(set *appsv1.StatefulSet) {
				set.Status.UpdatedReplicas = 0
			})},
			want: "StatefulSet my-mongo-rs0 to roll out (0 of 3 pods updated)",
		},
		{
			name: "scaling up",
			sets: []*appsv1.StatefulSet{rolledOut("rs0", func(set *appsv1.StatefulSet) {
				set.Spec.Replicas = pointer.ToInt32(5)
			})},
			want: "StatefulSet my-mongo-rs0 to scale to 5 pods (it has 3)",
		},
		{
			name: "a pod not ready",
			sets: []*appsv1.StatefulSet{rolledOut("rs0", func(set *appsv1.StatefulSet) {
				set.Status.ReadyReplicas = 2
			})},
			want: "StatefulSet my-mongo-rs0 to become ready (2 of 3 pods ready)",
		},
		{
			name: "the first one by name is reported",
			sets: []*appsv1.StatefulSet{
				rolledOut("rs1", func(set *appsv1.StatefulSet) { set.Status.UpdatedReplicas = 1 }),
				rolledOut("cfg", func(set *appsv1.StatefulSet) { set.Status.UpdatedReplicas = 2 }),
				rolledOut("rs0"),
			},
			want: "StatefulSet my-mongo-cfg to roll out (2 of 3 pods updated)",
		},
		{
			name: "one being removed",
			sets: []*appsv1.StatefulSet{rolledOut("rs0-hidden", func(set *appsv1.StatefulSet) {
				now := metav1.Now()
				set.DeletionTimestamp = &now
				set.Status.ReadyReplicas = 0
			})},
		},
		{
			name: "replicas unset",
			sets: []*appsv1.StatefulSet{rolledOut("rs0", func(set *appsv1.StatefulSet) {
				set.Spec.Replicas = nil
				set.Status.Replicas, set.Status.ReadyReplicas, set.Status.UpdatedReplicas = 1, 1, 1
			})},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sets := make([]appsv1.StatefulSet, 0, len(tt.sets))
			for _, set := range tt.sets {
				sets = append(sets, *set)
			}
			assert.Equal(t, tt.want, rolloutWait(sets))
		})
	}
}

func TestEnqueueStatefulSetInstance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		labels map[string]string
		want   []reconcile.Request
	}{
		{
			name:   "a StatefulSet of a cluster",
			labels: clusterStatefulSetLabels(statusTestName),
			want: []reconcile.Request{{
				NamespacedName: types.NamespacedName{Namespace: statusTestNamespace, Name: statusTestName},
			}},
		},
		{
			name:   "a StatefulSet something else runs",
			labels: map[string]string{naming.LabelKubernetesInstance: statusTestName},
		},
		{
			name:   "a StatefulSet of the operator without a cluster",
			labels: map[string]string{naming.LabelKubernetesManagedBy: "percona-server-mongodb-operator"},
		},
		{name: "a StatefulSet without labels"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			set := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{Name: "some-set", Namespace: statusTestNamespace, Labels: tt.labels},
			}
			assert.Equal(t, tt.want, enqueueStatefulSetInstance()(context.Background(), set))
		})
	}
}
