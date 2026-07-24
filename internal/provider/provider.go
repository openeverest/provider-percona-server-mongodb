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

	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	monitoringv1alpha1 "github.com/openeverest/openeverest/v2/api/monitoring/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	pbm "github.com/openeverest/provider-percona-server-mongodb/definition/backupclasses/percona-backup-mongodb"
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
		CRVersion: "1.22.0",
	}
}

// ValidatePSMDB validates the Instance spec for PSMDB.
func ValidatePSMDB(c *controller.Context) error {
	l := log.FromContext(c.Context())
	l.Info("Validating PSMDB cluster", "cluster", c.Name())

	if err := validateMetadata(c); err != nil {
		l.Error(err, "Metadata validation failed", "cluster", c.Name())
		return fmt.Errorf("metadata validation failed: %w", err)
	}

	if err := validateComponents(c); err != nil {
		l.Error(err, "Components validation failed", "cluster", c.Name())
		return fmt.Errorf("components validation failed: %w", err)
	}

	return nil
}

// SyncPSMDB creates or updates the PerconaServerMongoDB resource based on the Instance spec.
func SyncPSMDB(c *controller.Context) error {
	l := log.FromContext(c.Context())
	l.Info("Syncing PSMDB cluster", "cluster", c.Name())

	defer l.Info("PSMDB cluster synced", "cluster", c.Name())

	meta := c.ObjectMeta(c.Name())
	meta.Finalizers = []string{
		"percona.com/delete-psmdb-pods-in-order",
		"percona.com/delete-psmdb-pvc",
	}
	psmdb := &psmdbv1.PerconaServerMongoDB{
		ObjectMeta: meta,
		Spec:       defaultSpec(),
	}

	// Get the engine component spec
	engine := c.Instance().Spec.Components[common.ComponentEngine]
	// No need to check if engine is nil, it is guaranteed to be present by the validator
	psmdb.Spec.Unsafe = unsafeFlags(c)

	// Set the image: use the user-specified image if provided, otherwise resolve
	// from the version bundle (engine.Version is populated by the provider-runtime)
	// or fall back to the provider's default image.
	if engine.Image != "" {
		// User explicitly specified an image override.
		psmdb.Spec.Image = engine.Image
	} else {
		spec, err := c.ProviderSpec()
		if err != nil {
			return err
		}
		if engine.Version != "" {
			psmdb.Spec.Image = controller.GetImageForVersion(spec, common.ComponentEngine, engine.Version)
		}
		if psmdb.Spec.Image == "" {
			psmdb.Spec.Image = controller.GetDefaultImageForComponent(spec, common.ComponentEngine)
		}
	}
	psmdb.Spec.ImagePullPolicy = corev1.PullIfNotPresent

	replsets, err := configureReplsets(c)
	if err != nil {
		return err
	}
	psmdb.Spec.Replsets = replsets
	if c.Instance().Spec.Topology != nil && c.Instance().Spec.Topology.Type == "sharded" {
		psmdb.Spec.Sharding.Enabled = true
		configsvr, err := configureConfigServerReplset(c)
		if err != nil {
			return err
		}
		psmdb.Spec.Sharding.ConfigsvrReplSet = configsvr
		mongos, err := configureMongos(c)
		if err != nil {
			return err
		}
		psmdb.Spec.Sharding.Mongos = mongos
	}

	backupSpec, err := buildBackupSpec(c)
	if err != nil {
		return err
	}
	psmdb.Spec.Backup = backupSpec

	usersSecretName := "everest-secrets-" + c.Name()
	ds := c.Instance().Spec.DataSource

	// When seeding from a DataSource (Backup or Import), the target cluster's
	// users secret must contain the same credentials as the source cluster.
	// PBM backups embed credential hashes; mismatched secrets render the
	// restored data inaccessible. Copy BEFORE applying the PSMDB CR so the
	// operator never initializes the secret with random passwords.
	if ds != nil {
		switch ds.Type {
		case backupv1alpha1.DataSourceTypeBackup:
			if err := ensureDataSourceCredentials(c, usersSecretName); err != nil {
				return err
			}
		case backupv1alpha1.DataSourceTypeImport:
			if err := ensureImportCredentials(c, usersSecretName); err != nil {
				return err
			}
		}
	}

	// Configure monitoring after ensuring data source user credentials,
	// as it adds PMM credentials to the user secret.
	pmmSpec, err := configureMonitoring(c, usersSecretName)
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

	// For Backup and Provider Managed Import DataSources, initial seeding from
	// .spec.dataSource is gated on the engine being Ready
	// AND PSMDB having published a BackupVersion. Issuing the Restore before
	// the operator has selected a backup-agent image causes the restore to
	// hang in a Waiting state. While the gate is not satisfied the helper is
	// not invoked and StatusPSMDB will report Restoring so callers know the
	// Instance is still being seeded.
	if ds != nil {
		current := &psmdbv1.PerconaServerMongoDB{}
		if err := c.Get(current, c.Name()); err != nil {
			// Cluster has not been created yet (first Sync). The next
			// reconcile after the PSMDB CR appears will re-enter this branch.
			return nil
		}

		switch ds.Type {
		case backupv1alpha1.DataSourceTypeBackup:
			// For Backup type, wait for Ready + BackupVersion before restore
			if current.Status.State != psmdbv1.AppStateReady || current.Status.BackupVersion == "" {
				c.SetDataSourceStatus(controller.DataSourceStatus{
					Done:    false,
					State:   controller.DataSourceStateWaiting,
					Reason:  corev1alpha1.ReasonDataSourceWaitingForCluster,
					Message: "waiting for PerconaServerMongoDB to be Ready and publish a BackupVersion",
				})
				return nil
			}

		case backupv1alpha1.DataSourceTypeImport:
			// For Import, check if using Job mode (classRef set) or ProviderManaged mode.
			if ds.Import != nil && ds.Import.ClassRef != nil && ds.Import.ClassRef.Name != "" {
				// Job mode import - not yet implemented
				c.SetDataSourceStatus(controller.DataSourceStatus{
					Done:    true,
					State:   controller.DataSourceStateFailed,
					Reason:  corev1alpha1.ReasonDataSourceFailed,
					Message: "Job mode import is not yet implemented",
				})
				return nil
			}
			// ProviderManaged mode: wait for Ready + BackupVersion before restore.
			if current.Status.State != psmdbv1.AppStateReady || current.Status.BackupVersion == "" {
				c.SetDataSourceStatus(controller.DataSourceStatus{
					Done:    false,
					State:   controller.DataSourceStateWaiting,
					Reason:  corev1alpha1.ReasonDataSourceWaitingForCluster,
					Message: "waiting for PerconaServerMongoDB to be Ready and publish a BackupVersion",
				})
				return nil
			}
		}

		if _, err := c.ReconcileDataSource(); err != nil {
			return fmt.Errorf("reconcile data source: %w", err)
		}
	}

	return nil
}

// allow unsafe for replset and mongos when the configured replicas fall below the
// production-safe minimum; the rest are kept at their safe defaults
// (Presets are expected to relax them when needed).
func unsafeFlags(c *controller.Context) psmdbv1.UnsafeFlags {
	const (
		productionSafeReplsetSize = 3
		productionSafeMongosSize  = 3
		allowUnsafeTLS            = false
		allowUnsafeTermination    = false
		allowUnsafeBackup         = false
	)

	components := c.Instance().Spec.Components
	engineReplicas := components[common.ComponentEngine].Replicas
	proxyReplicas := components[common.ComponentProxy].Replicas

	return psmdbv1.UnsafeFlags{
		TLS:                    allowUnsafeTLS,
		ReplsetSize:            engineReplicas != nil && *engineReplicas < productionSafeReplsetSize,
		MongosSize:             proxyReplicas != nil && *proxyReplicas < productionSafeMongosSize,
		TerminationGracePeriod: allowUnsafeTermination,
		BackupIfUnhealthy:      allowUnsafeBackup,
	}
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
		if ds := c.GetDataSourceStatus(); ds != nil {
			if !ds.Done {
				return controller.Restoring(ds.Message), nil
			}
			if ds.State == controller.DataSourceStateFailed {
				return controller.Failed(ds.Message), nil
			}
		}

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
		URI:      fmt.Sprintf("mongodb://%s:%s@%s/admin?ssl=false", username, password, host),
	}, nil
}

// ensureDataSourceCredentials copies the users secret from the source Instance
// to the target Instance when seeding from .spec.dataSource. The source
// Instance is identified via the referenced Backup CR's .spec.instanceRef.name.
// This is idempotent: if the target secret already exists it is not
// overwritten, ensuring reconcile loops don't corrupt credentials.
func ensureDataSourceCredentials(c *controller.Context, targetSecretName string) error {
	// If the target secret already exists, we're done. Either a previous
	// reconcile created it or the user pre-provisioned it manually.
	targetSecret := &corev1.Secret{}
	if err := c.Get(targetSecret, targetSecretName); err == nil {
		return nil
	}

	// Resolve the source Instance name from the referenced Backup CR.
	ds := c.Instance().Spec.DataSource
	srcBackup := &backupv1alpha1.Backup{}
	if err := c.Get(srcBackup, ds.Backup.BackupRef.Name); err != nil {
		if apierrors.IsNotFound(err) {
			// Source Backup not found; ReconcileDataSource will surface this
			// as a condition later. Let Sync continue — the gate on
			// PSMDB Ready + BackupVersion will hold the restore.
			return nil
		}
		return fmt.Errorf("get source Backup %q for credential copy: %w", ds.Backup.BackupRef.Name, err)
	}

	// The source Instance's users secret follows the same naming convention.
	sourceSecretName := "everest-secrets-" + srcBackup.Spec.InstanceRef.Name
	sourceSecret := &corev1.Secret{}
	if err := c.Get(sourceSecret, sourceSecretName); err != nil {
		if apierrors.IsNotFound(err) {
			c.SetDataSourceStatus(controller.DataSourceStatus{
				Done:    true,
				State:   controller.DataSourceStateFailed,
				Reason:  corev1alpha1.ReasonDataSourceFailed,
				Message: fmt.Sprintf("source Instance credentials secret %q not found; the source Instance may have been deleted", sourceSecretName),
			})
			return &controller.DataSourceConfigError{
				Reason:  corev1alpha1.ReasonDataSourceFailed,
				Message: fmt.Sprintf("source Instance credentials secret %q not found; the source Instance may have been deleted", sourceSecretName),
			}
		}
		return fmt.Errorf("get source credentials secret %q: %w", sourceSecretName, err)
	}

	// Create the target secret with the same data.
	return createSecretCopy(c, targetSecretName, sourceSecret)
}

func createSecretCopy(c *controller.Context, targetSecretName string, sourceSecret *corev1.Secret) error {
	// Create the target secret with the same data.
	newSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      targetSecretName,
			Namespace: c.Namespace(),
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "everest",
				"app.kubernetes.io/instance":   c.Name(),
			},
		},
		Data: sourceSecret.Data,
	}
	if err := c.Client().Create(c.Context(), newSecret); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Race: another reconcile beat us to it.
			return nil
		}
		return fmt.Errorf("create target credentials secret %q: %w", targetSecretName, err)
	}
	return nil
}

// ensureImportCredentials copies the user-provided import credentials
// secret to the target Instance's users secret. This is called before
// applying the PSMDB CR to ensure the operator initializes its internal
// users secret with the correct password hashes (matching the backup).
// Unlike ensureDataSourceCredentials, this ALWAYS overwrites the target secret
// because the import credentials MUST match the backup source.
func ensureImportCredentials(c *controller.Context, targetSecretName string) error {
	// Get the source credentials secret name from the import config.
	sourceSecretName := getImportCredentialsSecretName(c)
	if sourceSecretName == "" {
		return fmt.Errorf("failed to get import credentails secret")
	}

	sourceSecret := &corev1.Secret{}
	if err := c.Get(sourceSecret, sourceSecretName); err != nil {
		if apierrors.IsNotFound(err) {
			c.SetDataSourceStatus(controller.DataSourceStatus{
				Done:    true,
				State:   controller.DataSourceStateFailed,
				Reason:  corev1alpha1.ReasonDataSourceFailed,
				Message: fmt.Sprintf("import credentials secret %q not found", sourceSecretName),
			})
			return &controller.DataSourceConfigError{
				Reason:  corev1alpha1.ReasonDataSourceFailed,
				Message: fmt.Sprintf("import credentials secret %q not found", sourceSecretName),
			}
		}
		return fmt.Errorf("get import credentials secret %q: %w", sourceSecretName, err)
	}

	// Set owner reference on the source secret so it's cleaned up with the Instance.
	if err := c.Apply(sourceSecret); err != nil {
		return fmt.Errorf("set owner reference on import credentials secret %q: %w", sourceSecretName, err)
	}

	// Create the target secret with the same data.
	return createSecretCopy(c, targetSecretName, sourceSecret)
}

// getImportCredentialsSecretName returns the credentials secret name
// from the import parameters. This is used to set
// psmdb.Spec.Secrets.Users directly.
func getImportCredentialsSecretName(c *controller.Context) string {
	ds := c.Instance().Spec.DataSource
	if ds == nil {
		return ""
	}

	var params []byte
	switch ds.Type {
	case backupv1alpha1.DataSourceTypeImport:
		if ds.Import == nil || ds.Import.Parameters == nil {
			return ""
		}
		params = ds.Import.Parameters.Raw
	default:
		return ""
	}

	var importCfg pbm.PerconaImportParameters
	if err := json.Unmarshal(params, &importCfg); err != nil {
		return ""
	}
	return importCfg.CredentialsSecretRef.Name
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
			monitoringv1alpha1.SchemeBuilder.AddToScheme,
		},
		WatchConfigs: []controller.WatchConfig{
			// Watch owned PSMDB resources - only trigger on spec changes
			// TODO: do we need some predicate? The
			// GenerationChangedPredicate definitely isn't correct because
			// we need to be notified when the status changes so we can
			// update the Instance status.
			controller.WatchOwned(&psmdbv1.PerconaServerMongoDB{}),
			// Watch owned PerconaServerMongoDBRestore resources - import
			// restores are owned by the Instance. When the restore completes
			// or fails, the Instance reconciler updates the DataSourceStatus.
			controller.WatchOwned(&psmdbv1.PerconaServerMongoDBRestore{}),
			// Watch the datasource Restore CR (owned by the Instance via
			// ReconcileDataSource). When the Restore reconciler marks it
			// Succeeded the Instance reconciler re-evaluates and exits
			// the Restoring phase.
			controller.WatchOwned(&backupv1alpha1.Restore{}),
			// Watch operator backups so latestRestorableTime refreshes
			// (stamped by PBM on ready backups) propagate to
			// instance.status.backup.storages via BackupStorageStatuses.
			// Operator backups are not owned by the Instance, so map them
			// to the parent via spec.clusterName.
			controller.WatchExternal(&psmdbv1.PerconaServerMongoDBBackup{},
				handler.EnqueueRequestsFromMapFunc(enqueueOperatorBackupInstance()),
			),
			controller.WatchExternal(&monitoringv1alpha1.MonitoringConfig{},
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
			Object:    &monitoringv1alpha1.MonitoringConfig{},
			FieldPath: credentialsSecretPath,
			Extractor: extractMonitoringConfigSecretName,
		},
	}
}

// BackupWatches implements controller.BackupWatcher. The runtime's Backup
// reconciler watches PerconaServerMongoDBBackup CRs as owned resources so
// operator status changes are routed directly to the parent Backup CR via
// owner-reference based enqueue (1:1, no Instance fan-out). SyncBackup sets
// the controller reference from Backup -> PerconaServerMongoDBBackup, so
// owner-based enqueue applies to every adopted backup. Operator-emitted
// scheduled backups are still routed through the Instance reconciler (where
// they get mirrored into Backup CRs) until the next SyncBackup adopts them.
func (p *PSMDBProvider) BackupWatches() []controller.WatchConfig {
	return []controller.WatchConfig{
		controller.WatchOwned(&psmdbv1.PerconaServerMongoDBBackup{}),
	}
}

// RestoreWatches mirrors BackupWatches for PerconaServerMongoDBRestore.
func (p *PSMDBProvider) RestoreWatches() []controller.WatchConfig {
	return []controller.WatchConfig{
		controller.WatchOwned(&psmdbv1.PerconaServerMongoDBRestore{}),
	}
}

// Compile-time interface checks
var _ controller.ProviderInterface = (*PSMDBProvider)(nil)
var _ controller.WatchProvider = (*PSMDBProvider)(nil)
var _ controller.FieldIndexProvider = (*PSMDBProvider)(nil)
var _ controller.BackupProvider = (*PSMDBProvider)(nil)
var _ controller.BackupWatcher = (*PSMDBProvider)(nil)
var _ controller.RestoreWatcher = (*PSMDBProvider)(nil)
var _ controller.BackupMirror = (*PSMDBProvider)(nil)
var _ controller.InstanceBackupStatusReporter = (*PSMDBProvider)(nil)
