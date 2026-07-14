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

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
)

const (
	// ImportJobNameSuffix is appended to the Instance name to form the import Job name.
	ImportJobNameSuffix = "-import"
	// ImportPayloadSecretSuffix is appended to the Instance name to form the payload Secret name.
	ImportPayloadSecretSuffix = "-import-payload"
	// PayloadMountPath is where the payload secret is mounted in the import job container.
	PayloadMountPath = "/payload"
	// PayloadFileName is the name of the file containing the import request JSON.
	PayloadFileName = "request.json"
)

// ImportPayload represents the JSON payload mounted into the import job container.
// This matches the dataimporterspec.Spec shape from v1.
type ImportPayload struct {
	Source ImportPayloadSource `json:"source"`
	Target ImportPayloadTarget `json:"target"`
}

// ImportPayloadSource contains the S3 source configuration.
type ImportPayloadSource struct {
	S3   ImportPayloadS3 `json:"s3"`
	Path string          `json:"path"`
}

// ImportPayloadS3 contains S3 connection details.
type ImportPayloadS3 struct {
	Bucket         string `json:"bucket"`
	Region         string `json:"region"`
	EndpointURL    string `json:"endpointURL"`
	AccessKeyID    string `json:"accessKeyID"`
	SecretKey      string `json:"secretKey"`
	VerifyTLS      bool   `json:"verifyTLS"`
	ForcePathStyle bool   `json:"forcePathStyle"`
}

// ImportPayloadTarget contains the target database configuration.
type ImportPayloadTarget struct {
	DatabaseClusterRef ImportClusterRef `json:"databaseClusterRef"`
	Host               string           `json:"host"`
	Port               string           `json:"port"`
	User               string           `json:"user"`
	Password           string           `json:"password"`
	Type               string           `json:"type"`
}

// ImportClusterRef references the target database cluster.
type ImportClusterRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// ImportConfig represents the parsed import config from Instance.spec.dataSource.external.config.
type ImportConfig struct {
	Path                  string `json:"path"`
	CredentialsSecretName string `json:"credentialsSecretName"`
}

// ReconcileExternalDataSource handles the import workflow for type=External data sources.
// It creates a payload Secret and import Job, then monitors the Job status.
func ReconcileExternalDataSource(c *controller.Context, connectionDetails controller.ConnectionDetails) error {
	l := log.FromContext(c.Context())
	ds := c.Instance().Spec.DataSource
	if ds == nil || ds.Type != backupv1alpha1.DataSourceTypeExternal {
		return nil
	}

	ext := ds.External
	l.Info("Reconciling external data source import", "backupClass", ext.BackupClassName, "storage", ext.StorageName)

	// Parse import config
	var importCfg ImportConfig
	if err := json.Unmarshal(ext.Config.Raw, &importCfg); err != nil {
		return fmt.Errorf("failed to parse import config: %w", err)
	}

	// Get BackupClass for import job spec
	bc, err := c.BackupClass(ext.BackupClassName)
	if err != nil {
		return fmt.Errorf("failed to get BackupClass %q: %w", ext.BackupClassName, err)
	}

	// Get BackupStorage for S3 credentials
	storage, err := c.BackupStorage(ext.StorageName)
	if err != nil {
		return fmt.Errorf("failed to get BackupStorage %q: %w", ext.StorageName, err)
	}

	// Get S3 credentials from BackupStorage's referenced secret
	s3Creds, err := getS3Credentials(c, storage)
	if err != nil {
		return fmt.Errorf("failed to get S3 credentials: %w", err)
	}

	// Get database credentials from the user-provided secret
	dbCreds, err := getDBCredentials(c, importCfg.CredentialsSecretName)
	if err != nil {
		return fmt.Errorf("failed to get DB credentials: %w", err)
	}

	// Build the import payload
	payload := buildImportPayload(c, s3Creds, importCfg, connectionDetails, dbCreds)

	// Create or update the payload secret
	payloadSecretName := c.Name() + ImportPayloadSecretSuffix
	if err := ensurePayloadSecret(c, payloadSecretName, payload); err != nil {
		return fmt.Errorf("failed to ensure payload secret: %w", err)
	}

	// Create or update the import job
	jobName := c.Name() + ImportJobNameSuffix
	if err := ensureImportJob(c, jobName, payloadSecretName, bc); err != nil {
		return fmt.Errorf("failed to ensure import job: %w", err)
	}

	// Check job status and update data source status
	return updateImportStatus(c, jobName)
}

// getS3Credentials retrieves S3 credentials from the BackupStorage's referenced secret.
func getS3Credentials(c *controller.Context, storage *backupv1alpha1.BackupStorage) (ImportPayloadS3, error) {
	if storage.Spec.S3 == nil {
		return ImportPayloadS3{}, fmt.Errorf("BackupStorage does not have S3 config")
	}

	s3Cfg := storage.Spec.S3
	result := ImportPayloadS3{
		Bucket:         s3Cfg.Bucket,
		Region:         s3Cfg.Region,
		EndpointURL:    s3Cfg.EndpointURL,
		VerifyTLS:      s3Cfg.VerifyTLS == nil || *s3Cfg.VerifyTLS,
		ForcePathStyle: s3Cfg.ForcePathStyle != nil && *s3Cfg.ForcePathStyle,
	}

	// Get credentials from secret if referenced
	if s3Cfg.CredentialsSecretName != "" {
		secret := &corev1.Secret{}
		if err := c.Get(secret, s3Cfg.CredentialsSecretName); err != nil {
			return ImportPayloadS3{}, fmt.Errorf("failed to get S3 credentials secret %q: %w", s3Cfg.CredentialsSecretName, err)
		}
		result.AccessKeyID = string(secret.Data["AWS_ACCESS_KEY_ID"])
		result.SecretKey = string(secret.Data["AWS_SECRET_ACCESS_KEY"])
	} else {
		// Use inline credentials if provided (write-only fields, may be empty)
		result.AccessKeyID = s3Cfg.AccessKeyID
		result.SecretKey = s3Cfg.SecretAccessKey
	}

	return result, nil
}

// getDBCredentials retrieves database credentials from the user-provided secret.
func getDBCredentials(c *controller.Context, secretName string) (map[string]string, error) {
	secret := &corev1.Secret{}
	if err := c.Get(secret, secretName); err != nil {
		return nil, fmt.Errorf("failed to get credentials secret %q: %w", secretName, err)
	}

	result := make(map[string]string)
	for k, v := range secret.Data {
		result[k] = string(v)
	}
	return result, nil
}

// buildImportPayload constructs the ImportPayload for the import job.
func buildImportPayload(
	c *controller.Context,
	s3Creds ImportPayloadS3,
	importCfg ImportConfig,
	connDetails controller.ConnectionDetails,
	dbCreds map[string]string,
) ImportPayload {
	return ImportPayload{
		Source: ImportPayloadSource{
			S3:   s3Creds,
			Path: importCfg.Path,
		},
		Target: ImportPayloadTarget{
			DatabaseClusterRef: ImportClusterRef{
				Name:      c.Name(),
				Namespace: c.Namespace(),
			},
			Host:     connDetails.Host,
			Port:     connDetails.Port,
			User:     dbCreds["MONGODB_DATABASE_ADMIN_USER"],
			Password: dbCreds["MONGODB_DATABASE_ADMIN_PASSWORD"],
			Type:     "mongodb",
		},
	}
}

// ensurePayloadSecret creates or updates the payload secret for the import job.
func ensurePayloadSecret(c *controller.Context, name string, payload ImportPayload) error {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: c.Namespace(),
		},
	}

	_, err = controllerutil.CreateOrUpdate(c.Context(), c.Client(), secret, func() error {
		if secret.Labels == nil {
			secret.Labels = make(map[string]string)
		}
		secret.Labels["app.kubernetes.io/managed-by"] = "openeverest"
		secret.Labels["app.kubernetes.io/instance"] = c.Name()
		secret.Labels["app.kubernetes.io/component"] = "import"

		secret.Data = map[string][]byte{
			PayloadFileName: payloadJSON,
		}

		return controllerutil.SetControllerReference(c.Instance(), secret, c.Client().Scheme())
	})

	return err
}

// ensureImportJob creates or updates the import job.
func ensureImportJob(c *controller.Context, jobName, payloadSecretName string, bc *backupv1alpha1.BackupClass) error {
	if bc.Spec.ImportJob == nil || bc.Spec.ImportJob.JobSpec == nil {
		return fmt.Errorf("BackupClass %q does not have importJob.jobSpec defined", bc.Name)
	}

	jobSpec := bc.Spec.ImportJob.JobSpec
	var backoffLimit int32 = 0 // Don't retry failed imports

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: c.Namespace(),
		},
	}

	_, err := controllerutil.CreateOrUpdate(c.Context(), c.Client(), job, func() error {
		if job.Labels == nil {
			job.Labels = make(map[string]string)
		}
		job.Labels["app.kubernetes.io/managed-by"] = "openeverest"
		job.Labels["app.kubernetes.io/instance"] = c.Name()
		job.Labels["app.kubernetes.io/component"] = "import"

		// Only set spec on create (Jobs are immutable)
		if job.CreationTimestamp.IsZero() {
			job.Spec = batchv1.JobSpec{
				BackoffLimit: &backoffLimit,
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app.kubernetes.io/managed-by": "everest",
							"app.kubernetes.io/instance":   c.Name(),
							"app.kubernetes.io/component":  "import",
						},
					},
					Spec: corev1.PodSpec{
						RestartPolicy: corev1.RestartPolicyNever,
						Containers: []corev1.Container{
							{
								Name:    "importer",
								Image:   jobSpec.Image,
								Command: jobSpec.Command,
								VolumeMounts: []corev1.VolumeMount{
									{
										Name:      "payload",
										MountPath: PayloadMountPath,
										ReadOnly:  true,
									},
								},
							},
						},
						Volumes: []corev1.Volume{
							{
								Name: "payload",
								VolumeSource: corev1.VolumeSource{
									Secret: &corev1.SecretVolumeSource{
										SecretName: payloadSecretName,
									},
								},
							},
						},
					},
				},
			}
		}

		return controllerutil.SetControllerReference(c.Instance(), job, c.Client().Scheme())
	})

	return err
}

// updateImportStatus checks the import job status and updates the data source status.
func updateImportStatus(c *controller.Context, jobName string) error {
	job := &batchv1.Job{}
	if err := c.Get(job, jobName); err != nil {
		if apierrors.IsNotFound(err) {
			c.SetDataSourceStatus(controller.DataSourceStatus{
				Done:    false,
				State:   controller.DataSourceStateWaiting,
				Reason:  corev1alpha1.ReasonDataSourceWaitingForCluster,
				Message: "waiting for import job to be created",
			})
			return nil
		}
		return fmt.Errorf("failed to get import job %q: %w", jobName, err)
	}

	// Check job conditions
	for _, cond := range job.Status.Conditions {
		if cond.Type == batchv1.JobComplete && cond.Status == corev1.ConditionTrue {
			c.SetDataSourceStatus(controller.DataSourceStatus{
				Done:    true,
				State:   controller.DataSourceStateSucceeded,
				Reason:  corev1alpha1.ReasonDataSourceSucceeded,
				Message: fmt.Sprintf("Import completed successfully via Job %q", jobName),
			})
			return nil
		}
		if cond.Type == batchv1.JobFailed && cond.Status == corev1.ConditionTrue {
			c.SetDataSourceStatus(controller.DataSourceStatus{
				Done:    true,
				State:   controller.DataSourceStateFailed,
				Reason:  corev1alpha1.ReasonDataSourceFailed,
				Message: fmt.Sprintf("Import job %q failed: %s", jobName, cond.Message),
			})
			return nil
		}
	}

	// Job is still running
	c.SetDataSourceStatus(controller.DataSourceStatus{
		Done:    false,
		State:   controller.DataSourceStateRestoring,
		Reason:  corev1alpha1.ReasonDataSourceRestoring,
		Message: fmt.Sprintf("Import job %q in progress", jobName),
	})

	return nil
}
