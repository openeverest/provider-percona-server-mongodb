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
	"strings"

	pbmdefs "github.com/percona/percona-backup-mongodb/pbm/defs"
	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	pbm "github.com/openeverest/provider-percona-server-mongodb/definition/backupclasses/percona-backup-mongodb"
)

const (
	// ImportRestoreSuffix is appended to the Instance name to form the import restore CR name.
	ImportRestoreSuffix = "-import"
	// ImportCredentialsSecretSuffix is appended to the Instance name to form the S3 credentials secret name.
	ImportCredentialsSecretSuffix = "-import-s3-creds"

	// ManagedByDataImportAnnotation marks a PerconaServerMongoDBRestore as created by the import workflow.
	// This prevents the Everest operator from creating a DatabaseBackupRestore (DBR) for this restore.
	ManagedByDataImportAnnotation      = "openeverest.io/managed-by-data-import"
	ManagedByDataImportAnnotationValue = "true"
)

// ReconcileExternalDataSource handles the import workflow for type=External data sources.
//
// This implementation uses ProviderManaged mode: it creates a PerconaServerMongoDBRestore
// CR directly and lets the PSMDB operator's PBM agent perform the actual restore.
func ReconcileExternalDataSource(c *controller.Context) error {
	l := log.FromContext(c.Context())
	ds := c.Instance().Spec.DataSource
	if ds == nil || ds.Type != backupv1alpha1.DataSourceTypeExternal {
		return nil
	}

	ext := ds.External
	l.Info("Reconciling external data source import", "backupClass", ext.BackupClassRef.Name, "storage", ext.StorageRef.Name)

	// Parse import config
	var importCfg pbm.PerconaImportConfig
	if err := json.Unmarshal(ext.Config.Raw, &importCfg); err != nil {
		return fmt.Errorf("failed to parse import config: %w", err)
	}

	// Get BackupClass to determine execution mode
	bc, err := c.BackupClass(ext.BackupClassRef.Name)
	if err != nil {
		return fmt.Errorf("failed to get BackupClass %q: %w", ext.BackupClassRef.Name, err)
	}

	// Route to the appropriate handler based on execution mode
	switch bc.Spec.ExecutionMode {
	case backupv1alpha1.BackupExecutionModeProviderManaged:
		if bc.Spec.ProviderManaged == nil || !bc.Spec.ProviderManaged.SupportsImport {
			return fmt.Errorf("BackupClass %q is ProviderManaged but does not support import", bc.Name)
		}

		return reconcileProviderManagedImport(c, ext, importCfg)

	case backupv1alpha1.BackupExecutionModeJob:
		if bc.Spec.ImportJob == nil {
			return fmt.Errorf("BackupClass %q is Job mode but does not have importJob defined", bc.Name)
		}

		return reconcileJobModeImport(c, ext, importCfg, bc)

	default:
		return fmt.Errorf("unsupported execution mode %q for import", bc.Spec.ExecutionMode)
	}
}

// reconcileProviderManagedImport handles import using the PSMDB operator's restore mechanism.
// This creates a PerconaServerMongoDBRestore CR directly, and PBM performs the actual restore.
func reconcileProviderManagedImport(c *controller.Context, ext *backupv1alpha1.DataSourceExternal, importCfg pbm.PerconaImportConfig) error {
	l := log.FromContext(c.Context())

	// Validate that credentials secret is provided
	if importCfg.CredentialsSecretRef.Name == "" {
		return fmt.Errorf("credentialsSecretRef.name is required for ProviderManaged import: " +
			"PBM backups embed credential hashes, the target Instance must use the same " +
			"credentials as the source database")
	}

	// Get BackupStorage for S3 credentials
	storage, err := c.BackupStorage(ext.StorageRef.Name)
	if err != nil {
		return fmt.Errorf("failed to get BackupStorage %q: %w", ext.StorageRef.Name, err)
	}

	// Set owner reference on the user-provided credentials secret so it's
	// cleaned up when the Instance is deleted. The user created this secret
	// specifically for this import operation.
	if err := ensureImportCredentialsOwnership(c, importCfg.CredentialsSecretRef.Name); err != nil {
		return fmt.Errorf("failed to ensure import credentials ownership: %w", err)
	}

	// Ensure S3 credentials secret for the restore
	s3CredSecretName := c.Name() + ImportCredentialsSecretSuffix
	if err := ensureS3CredentialsSecret(c, s3CredSecretName, storage); err != nil {
		return fmt.Errorf("failed to ensure S3 credentials secret: %w", err)
	}

	// Create or get the PerconaServerMongoDBRestore CR
	restoreName := c.Name() + ImportRestoreSuffix
	if err := ensureImportRestore(c, restoreName, s3CredSecretName, storage, importCfg); err != nil {
		return fmt.Errorf("failed to ensure import restore CR: %w", err)
	}

	// Observe the restore status and update data source status
	return observeImportRestoreStatus(c, restoreName, l)
}

// getExternalImportCredentialsSecretName returns the credentials secret name
// from the external import config. This is called from provider.go to set
// psmdb.Spec.Secrets.Users to use the user-provided secret directly.
func getExternalImportCredentialsSecretName(c *controller.Context) string {
	ds := c.Instance().Spec.DataSource
	if ds == nil || ds.External == nil || ds.External.Config == nil {
		return ""
	}

	var importCfg pbm.PerconaImportConfig
	if err := json.Unmarshal(ds.External.Config.Raw, &importCfg); err != nil {
		return ""
	}
	return importCfg.CredentialsSecretRef.Name
}

// ensureImportCredentialsOwnership sets owner reference on the user-provided
// credentials secret so it's cleaned up when the Instance is deleted.
//
// The PSMDB CR's spec.secrets.users is set to this secret name directly
// (see provider.go), so no copying is needed.
func ensureImportCredentialsOwnership(c *controller.Context, secretName string) error {
	secret := &corev1.Secret{}
	if err := c.Get(secret, secretName); err != nil {
		return fmt.Errorf("failed to get credentials secret %q: %w", secretName, err)
	}

	// Set owner reference so the secret is cleaned up with the Instance
	if !hasOwnerReference(secret, c.Instance()) {
		if err := controllerutil.SetOwnerReference(c.Instance(), secret, c.Client().Scheme()); err != nil {
			return fmt.Errorf("failed to set owner reference: %w", err)
		}
		if err := c.Client().Update(c.Context(), secret); err != nil {
			return fmt.Errorf("failed to update secret with owner reference: %w", err)
		}
	}

	return nil
}

// hasOwnerReference checks if the object has an owner reference to the given owner.
func hasOwnerReference(obj, owner metav1.Object) bool {
	for _, ref := range obj.GetOwnerReferences() {
		if ref.UID == owner.GetUID() {
			return true
		}
	}
	return false
}

// ensureS3CredentialsSecret creates a secret with S3 credentials for the import restore.
func ensureS3CredentialsSecret(c *controller.Context, name string, storage *backupv1alpha1.BackupStorage) error {
	if storage.Spec.S3 == nil {
		return fmt.Errorf("BackupStorage does not have S3 config")
	}

	// Get credentials from the BackupStorage's referenced secret
	if storage.Spec.S3.CredentialsSecretName == "" {
		return fmt.Errorf("BackupStorage S3 config does not specify credentialsSecret")
	}

	srcSecret := &corev1.Secret{}
	if err := c.Get(srcSecret, storage.Spec.S3.CredentialsSecretName); err != nil {
		return fmt.Errorf("failed to get S3 credentials secret %q: %w", storage.Spec.S3.CredentialsSecretName, err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: c.Namespace(),
		},
	}

	_, err := controllerutil.CreateOrUpdate(c.Context(), c.Client(), secret, func() error {
		secret.Type = corev1.SecretTypeOpaque
		secret.StringData = map[string]string{
			"AWS_ACCESS_KEY_ID":     string(srcSecret.Data["AWS_ACCESS_KEY_ID"]),
			"AWS_SECRET_ACCESS_KEY": string(srcSecret.Data["AWS_SECRET_ACCESS_KEY"]),
		}
		return controllerutil.SetControllerReference(c.Instance(), secret, c.Client().Scheme())
	})

	return err
}

// ensureImportRestore creates or updates the PerconaServerMongoDBRestore CR for the import.
func ensureImportRestore(
	c *controller.Context,
	restoreName, s3CredSecretName string,
	storage *backupv1alpha1.BackupStorage,
	importCfg pbm.PerconaImportConfig,
) error {
	s3Cfg := storage.Spec.S3

	// Parse the backup path to extract prefix and destination
	// Path format: "path/to/backup" -> prefix: "path/to", destination: "s3://bucket/path/to/backup"
	backupPath := strings.Trim(importCfg.Path, "/")
	split := strings.Split(backupPath, "/")
	prefix := ""
	if len(split) > 1 {
		prefix = strings.Join(split[:len(split)-1], "/")
	}
	destination := fmt.Sprintf("s3://%s/%s", s3Cfg.Bucket, backupPath)

	forcePathStyle := s3Cfg.ForcePathStyle != nil && *s3Cfg.ForcePathStyle
	verifyTLS := s3Cfg.VerifyTLS == nil || *s3Cfg.VerifyTLS

	psmdbRestore := &psmdbv1.PerconaServerMongoDBRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      restoreName,
			Namespace: c.Namespace(),
		},
	}

	_, err := controllerutil.CreateOrUpdate(c.Context(), c.Client(), psmdbRestore, func() error {
		// Mark as managed by data import so other controllers don't create DBR for it
		if psmdbRestore.Annotations == nil {
			psmdbRestore.Annotations = make(map[string]string)
		}
		psmdbRestore.Annotations[ManagedByDataImportAnnotation] = ManagedByDataImportAnnotationValue

		// Labels to identify the object
		if psmdbRestore.Labels == nil {
			psmdbRestore.Labels = make(map[string]string)
		}
		psmdbRestore.Labels["app.kubernetes.io/managed-by"] = "openeverest"
		psmdbRestore.Labels["app.kubernetes.io/instance"] = c.Name()
		psmdbRestore.Labels["app.kubernetes.io/component"] = "import"

		// Set owner reference for cleanup
		if err := controllerutil.SetOwnerReference(c.Instance(), psmdbRestore, c.Client().Scheme()); err != nil {
			return fmt.Errorf("failed to set owner reference: %w", err)
		}

		psmdbRestore.Spec = psmdbv1.PerconaServerMongoDBRestoreSpec{
			ClusterName: c.Name(),
			BackupSource: &psmdbv1.PerconaServerMongoDBBackupStatus{
				Type:        pbmdefs.LogicalBackup,
				Destination: destination,
				S3: &psmdbv1.BackupStorageS3Spec{
					Bucket:                s3Cfg.Bucket,
					Region:                s3Cfg.Region,
					EndpointURL:           s3Cfg.EndpointURL,
					CredentialsSecret:     s3CredSecretName,
					Prefix:                prefix,
					InsecureSkipTLSVerify: !verifyTLS,
					ForcePathStyle:        &forcePathStyle,
				},
			},
		}
		return nil
	})

	return err
}

// observeImportRestoreStatus checks the PerconaServerMongoDBRestore status and updates the data source status.
func observeImportRestoreStatus(c *controller.Context, restoreName string, l interface {
	Info(msg string, keysAndValues ...any)
}) error {
	psmdbRestore := &psmdbv1.PerconaServerMongoDBRestore{}
	if err := c.Get(psmdbRestore, restoreName); err != nil {
		if apierrors.IsNotFound(err) {
			c.SetDataSourceStatus(controller.DataSourceStatus{
				Done:    false,
				State:   controller.DataSourceStateWaiting,
				Reason:  corev1alpha1.ReasonDataSourceWaitingForCluster,
				Message: "waiting for import restore CR to be created",
			})
			return nil
		}
		return fmt.Errorf("failed to get import restore %q: %w", restoreName, err)
	}

	switch psmdbRestore.Status.State {
	case psmdbv1.RestoreStateReady:
		l.Info("Import restore completed successfully", "restore", restoreName)
		c.SetDataSourceStatus(controller.DataSourceStatus{
			Done:    true,
			State:   controller.DataSourceStateSucceeded,
			Reason:  corev1alpha1.ReasonDataSourceSucceeded,
			Message: fmt.Sprintf("Import completed successfully via restore %q", restoreName),
		})
		return nil

	case psmdbv1.RestoreStateError:
		l.Info("Import restore failed", "restore", restoreName, "error", psmdbRestore.Status.Error)
		c.SetDataSourceStatus(controller.DataSourceStatus{
			Done:    true,
			State:   controller.DataSourceStateFailed,
			Reason:  corev1alpha1.ReasonDataSourceFailed,
			Message: fmt.Sprintf("Import restore %q failed: %s", restoreName, psmdbRestore.Status.Error),
		})
		return nil

	default:
		// Restore is still running (Waiting, Requested, Running, etc.)
		c.SetDataSourceStatus(controller.DataSourceStatus{
			Done:    false,
			State:   controller.DataSourceStateRestoring,
			Reason:  corev1alpha1.ReasonDataSourceRestoring,
			Message: fmt.Sprintf("Import restore %q in progress (state: %s)", restoreName, psmdbRestore.Status.State),
		})
		return nil
	}
}

// =============================================================================
// Job Mode Import (TODO: Implement for formats not supported by PBM)
// =============================================================================

// reconcileJobModeImport handles import using an external Kubernetes Job.
// This is for formats that cannot be restored by PBM and require direct database access.
//
// TODO: Implement Job-mode import for formats like:
// - JSON files (mongoimport)
// - CSV files (mongoimport)
// - Custom application-specific formats
//
// The Job should:
// 1. Download data from S3 directly (using AWS SDK, s3cmd, or similar)
// 2. Connect to the database using credentials from the payload secret
// 3. Run the import tool (mongoimport, psql, mysql) directly
// 4. Exit with success/failure based on the tool's exit code
//
// This is TRUE Job mode - the Job does the actual import work, not just
// creating another CR and waiting for it.
func reconcileJobModeImport(
	_ *controller.Context,
	_ *backupv1alpha1.DataSourceExternal,
	_ pbm.PerconaImportConfig,
	_ *backupv1alpha1.BackupClass,
) error {
	return fmt.Errorf("Job-mode import is not yet implemented")
}
