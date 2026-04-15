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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	monitoringv1alpha2 "github.com/openeverest/openeverest/v2/api/monitoring/v1alpha2"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
)

const (
	psmdbDefaultConfigurationTemplate = `
      operationProfiling:
        mode: slowOp
`
	defaultBackupStartingTimeout = 120
)

var maxUnavailable = intstr.FromInt(1)

// defaultSpec returns the default PerconaServerMongoDBSpec for new instances.
func defaultSpec() psmdbv1.PerconaServerMongoDBSpec {
	return psmdbv1.PerconaServerMongoDBSpec{
		UpdateStrategy: psmdbv1.SmartUpdateStatefulSetStrategyType,
		UpgradeOptions: psmdbv1.UpgradeOptions{
			Apply:    "disabled",
			Schedule: "0 4 * * *",
			SetFCV:   true,
		},
		PMM: psmdbv1.PMMSpec{},
		Replsets: []*psmdbv1.ReplsetSpec{
			{
				Name:          "rs0",
				Configuration: psmdbv1.MongoConfiguration(psmdbDefaultConfigurationTemplate),
				MultiAZ: psmdbv1.MultiAZ{
					PodDisruptionBudget: &psmdbv1.PodDisruptionBudgetSpec{
						MaxUnavailable: &maxUnavailable,
					},
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{},
					},
				},
				Size: 3,
				VolumeSpec: &psmdbv1.VolumeSpec{
					PersistentVolumeClaim: psmdbv1.PVCSpec{
						PersistentVolumeClaimSpec: &corev1.PersistentVolumeClaimSpec{
							Resources: corev1.VolumeResourceRequirements{
								Requests: corev1.ResourceList{
									// TODO: set storage size
									corev1.ResourceStorage: resource.MustParse("10Gi"),
								},
							},
						},
					},
				},
			},
		},
		Sharding: psmdbv1.Sharding{
			Enabled: false,
		},
		VolumeExpansionEnabled: true,
		// FIXME
		CRVersion: "1.21.1",
	}
}

// ValidatePSMDB validates the Instance spec for PSMDB.
func ValidatePSMDB(c *controller.Context) error {
	l := log.FromContext(c.Context())
	l.Info("Validating PSMDB cluster", "cluster", c.Name())

	if err := validateMonitoring(c); err != nil {
		return fmt.Errorf("monitoring validation failed: %w", err)
	}

	// TODO: Add actual validation logic
	// Example: Check for required components, validate storage sizes, etc.
	return nil
}

// SyncPSMDB creates or updates the PerconaServerMongoDB resource based on the Instance spec.
func SyncPSMDB(c *controller.Context) error {
	l := log.FromContext(c.Context())
	l.Info("Syncing PSMDB cluster", "cluster", c.Name())

	defer l.Info("PSMDB cluster synced", "cluster", c.Name())

	psmdb := &psmdbv1.PerconaServerMongoDB{
		ObjectMeta: c.ObjectMeta(c.Name()),
		Spec:       defaultSpec(),
	}

	// Get the engine component spec
	engine := c.Instance().Spec.Components[common.ComponentEngine]
	// No need to check if engine is nil, it is guaranteed to be present by the validator

	// Set the image: use the user-specified image if provided, otherwise use the default from the provider spec
	if engine.Image != "" {
		// User explicitly specified an image
		psmdb.Spec.Image = engine.Image
	} else {
		spec, err := c.ProviderSpec()
		if err != nil {
			return err
		}
		psmdb.Spec.Image = controller.GetDefaultImageForComponent(spec, common.ComponentEngine)
	}
	psmdb.Spec.ImagePullPolicy = corev1.PullIfNotPresent

	psmdb.Spec.Replsets = configureReplsets(c)
	if c.Instance().Spec.Topology != nil && c.Instance().Spec.Topology.Type == "sharded" {
		psmdb.Spec.Sharding.Enabled = true
		psmdb.Spec.Sharding.ConfigsvrReplSet = configureConfigServerReplset(c)
		psmdb.Spec.Sharding.Mongos = configureMongos(c)
	}

	psmdb.Spec.Backup = configureBackup(c)

	usersSecretName := "everest-secrets-" + c.Name()

	// Fetch the current PSMDB from the cluster to inform monitoring resource
	// calculations. The current spec is nil when the resource doesn't exist yet
	// (first reconciliation for a new instance).
	var currentSpec *psmdbv1.PerconaServerMongoDBSpec
	currentPSMDB := &psmdbv1.PerconaServerMongoDB{}
	if err := c.Get(currentPSMDB, c.Name()); err == nil {
		currentSpec = &currentPSMDB.Spec
	}

	pmmSpec, err := configureMonitoring(c, currentSpec, usersSecretName)
	if err != nil {
		return err
	}
	if pmmSpec != nil {
		psmdb.Spec.PMM = *pmmSpec
	}

	psmdb.Spec.Secrets = &psmdbv1.SecretsSpec{
		Users:         usersSecretName,
		EncryptionKey: c.Name() + "-mongodb-encryption-key",
		SSLInternal:   c.Name() + "-ssl-internal",
	}

	if err := c.Apply(psmdb); err != nil {
		return err
	}

	return nil
}

// StatusPSMDB computes the current status of the PSMDB cluster.
func StatusPSMDB(c *controller.Context) (controller.Status, error) {
	// TODO: We probably shouldn't be querying the PSMDB object directly here;
	// It can lead to a race condition where we are setting the status based on
	// new data whereas the sync used older data.
	// Should the SDK be responsible for fetching and caching the PSMDB object
	// to ensure we only get it once during the reconcile?
	psmdb := &psmdbv1.PerconaServerMongoDB{}
	if err := c.Get(psmdb, c.Name()); err != nil {
		return controller.Provisioning("Waiting for PerconaServerMongoDB"), nil
	}
	switch psmdb.Status.State {
	case psmdbv1.AppStateReady:
		details, err := buildConnectionDetails(c, psmdb)
		if err != nil {
			return controller.Failed("Failed to build connection details: " + err.Error()), nil
		}
		return controller.ReadyWithConnectionDetails(details), nil
	case psmdbv1.AppStateError:
		return controller.Failed(psmdb.Status.Message), nil
	default:
		return controller.Provisioning("Cluster is being created"), nil
	}
}

// buildConnectionDetails reads the PSMDB Users secret and combines it with host info
// to produce a set of well-known connection details.
func buildConnectionDetails(c *controller.Context, psmdb *psmdbv1.PerconaServerMongoDB) (controller.ConnectionDetails, error) {
	secretName := "everest-secrets-" + c.Name()
	secret := &corev1.Secret{}
	if err := c.Get(secret, secretName); err != nil {
		return controller.ConnectionDetails{}, fmt.Errorf("failed to get credentials secret %s: %w", secretName, err)
	}

	host := psmdb.Status.Host
	port := "27017"

	username := string(secret.Data[psmdbv1.EnvMongoDBDatabaseAdminUser])
	password := string(secret.Data[psmdbv1.EnvMongoDBDatabaseAdminPassword])

	return controller.ConnectionDetails{
		Type:     "mongodb",
		Provider: "percona-server-mongodb",
		Host:     host,
		Port:     port,
		Username: username,
		Password: password,
		URI:      fmt.Sprintf("mongodb://%s:%s@%s:%s/admin?ssl=false", username, password, host, port),
	}, nil
}

// CleanupPSMDB handles deletion of the PSMDB cluster.
func CleanupPSMDB(c *controller.Context) error {
	l := log.FromContext(c.Context())
	l.Info("Cleaning up PSMDB cluster", "cluster", c.Name())

	// TODO: Implemenent handling of finalizers
	psmdb := &psmdbv1.PerconaServerMongoDB{
		ObjectMeta: c.ObjectMeta(c.Name()),
	}
	if err := c.Delete(psmdb); err != nil {
		return err
	}

	l.Info("PSMDB cluster cleaned up", "cluster", c.Name())

	return nil
}

// PSMDBProvider implements the ProviderInterface.
type PSMDBProvider struct {
	controller.BaseProvider
	client client.Client
}

// SetClient injects the Kubernetes client into the provider.
// Must be called after reconciler.New() and before r.Start().
// TODO: this is not great, change the way manager is configured
// so injection is not necessary.
func (p *PSMDBProvider) SetClient(c client.Client) {
	p.client = c
}

// NewPSMDBProviderInterface creates a new PSMDB provider.
// The provider name must match the Provider CR name so the runtime
// can automatically fetch schemas and version metadata from it.
// Call SetClient on the returned provider before starting the reconciler
// so the MonitoringConfig watch handler can list referencing Instances.
func NewPSMDBProviderInterface() *PSMDBProvider {
	p := &PSMDBProvider{}

	p.BaseProvider = controller.BaseProvider{
		ProviderName: "percona-server-mongodb",
		SchemeFuncs: []func(*runtime.Scheme) error{
			psmdbv1.SchemeBuilder.AddToScheme,
			monitoringv1alpha2.SchemeBuilder.AddToScheme,
		},
		WatchConfigs: []controller.WatchConfig{
			// Watch owned PSMDB resources - only trigger on spec changes
			// TODO: do we need some predicate? The
			// GenerationChangedPredicate definitely isn't correct because
			// we need to be notified when the status changes so we can
			// update the Instance status.
			controller.WatchOwned(&psmdbv1.PerconaServerMongoDB{}),
			controller.WatchExternal(&monitoringv1alpha2.MonitoringConfig{},
				handler.EnqueueRequestsFromMapFunc(enqueueMonitoringConfig(p)),
				monitoringConfigPredicate(),
			),
			// Watch secrets referenced by MonitoringConfig resources
			// TODO: Can this watch removed? After all, MontoringConfig owns this secret.
			controller.WatchExternal(&corev1.Secret{},
				handler.EnqueueRequestsFromMapFunc(enqueueMonitoringConfigSecret(p)),
				monitoringConfigPredicate(),
			),
		},
	}

	return p
}

// Validate validates the Instance spec.
func (p *PSMDBProvider) Validate(c *controller.Context) error {
	return ValidatePSMDB(c)
}

// Sync ensures all resources exist and are configured correctly.
func (p *PSMDBProvider) Sync(c *controller.Context) error {
	return SyncPSMDB(c)
}

// Status computes the current status of the cluster.
func (p *PSMDBProvider) Status(c *controller.Context) (controller.Status, error) {
	return StatusPSMDB(c)
}

// Cleanup handles deletion of the cluster and any necessary cleanup.
func (p *PSMDBProvider) Cleanup(c *controller.Context) error {
	return CleanupPSMDB(c)
}

// FieldIndexes implements controller.FieldIndexProvider.
// It registers indexes used by watchers.
func (p *PSMDBProvider) FieldIndexes() []controller.FieldIndex {
	return []controller.FieldIndex{
		{
			Object:    &corev1alpha1.Instance{},
			FieldPath: monitoringConfigPath,
			Extractor: extractMonitoringConfigName,
		},
		{
			Object:    &monitoringv1alpha2.MonitoringConfig{},
			FieldPath: credentialsSecretPath,
			Extractor: extractMonitoringConfigSecretName,
		},
	}
}

// Compile-time interface checks
var _ controller.ProviderInterface = (*PSMDBProvider)(nil)
var _ controller.WatchProvider = (*PSMDBProvider)(nil)
var _ controller.FieldIndexProvider = (*PSMDBProvider)(nil)
