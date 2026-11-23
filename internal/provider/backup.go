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
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/AlekSi/pointer"
	"github.com/percona/percona-backup-mongodb/pbm/compress"
	"github.com/percona/percona-backup-mongodb/pbm/defs"
	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	"github.com/percona/percona-server-mongodb-operator/pkg/util/numstr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	s3class "github.com/openeverest/openeverest/v2/backupstorageclasses/s3"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mongodb/definition/backupclasses"
	perconabackupmongodb "github.com/openeverest/provider-percona-server-mongodb/definition/backupclasses/percona-backup-mongodb"
	"github.com/openeverest/provider-percona-server-mongodb/definition/components"
	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
)

const (
	// psmdbDeleteBackupFinalizer is the PSMDB operator finalizer
	// that protects S3 storage during backup deletion.
	psmdbDeleteBackupFinalizer = "percona.com/delete-backup"

	// psmdbBackupAncestorLabel is the label key that PSMDB stamps on
	// PerconaServerMongoDBBackup objects produced by a scheduled task. The
	// value is the BackupTaskSpec.Name. Backups created on demand (no task)
	// do not carry this label. Mirrors `naming.LabelBackupAncestor` in the
	// PSMDB operator (kept as a string constant here to avoid pulling in the
	// operator's internal naming package).
	psmdbBackupAncestorLabel = "percona.com/backup-ancestor"

	// backupClassName is the BackupClass the provider ships, defined in
	// definition/backupclasses.
	backupClassName = "percona-backup-mongodb"
)

// =============================================================================
// BACKUP SPEC — complete engine configuration built at Sync time
// =============================================================================

// backupAgentImage returns the PBM image of the Instance's version bundle, so
// a provider release with a newer PBM does not restart running instances. The
// runtime resolves versions only for components present on the Instance, and
// backups do not need the backupAgent component.
func backupAgentImage(spec *corev1alpha1.ProviderSpec, in *corev1alpha1.Instance) string {
	version := in.Spec.Components[common.ComponentBackupAgent].Version
	if version == "" {
		if bundle, err := controller.ResolveVersionBundle(spec, controller.EffectiveVersionBundleName(spec, in)); err == nil {
			version = bundle.Components[common.ComponentBackupAgent]
		}
	}
	if image := controller.GetImageForVersion(spec, common.ComponentBackupAgent, version); image != "" {
		return image
	}
	return controller.GetDefaultImageForComponent(spec, common.ComponentBackupAgent)
}

// buildBackupSpec returns the complete psmdbv1.BackupSpec that SyncPSMDB
// stamps onto every PerconaServerMongoDB in a single Apply call. The PSMDB
// operator requires Backup.Image to be set even when backups are disabled
// (the backup-agent image is referenced by the regular DB pods). When the
// Instance has .spec.backup enabled, this function also resolves storages,
// schedules, and the PITR flag so all backup-related fields are written
// together, avoiding the reconcile loop that would occur from multiple writes
// to the same engine CR.
func buildBackupSpec(c *controller.Context) (psmdbv1.BackupSpec, error) {
	spec, err := c.ProviderSpec()
	if err != nil {
		return psmdbv1.BackupSpec{}, fmt.Errorf("get provider spec: %w", err)
	}
	backupAgent := c.Instance().Spec.Components[common.ComponentBackupAgent]
	agentParams, err := decodeBackupAgentParameters(backupAgent.Parameters)
	if err != nil {
		return psmdbv1.BackupSpec{}, &controller.BackupConfigError{
			Reason:  "BackupAgentParametersInvalid",
			Message: err.Error(),
		}
	}
	bs := psmdbv1.BackupSpec{
		Image: backupAgentImage(spec, c.Instance()),
		Configuration: psmdbv1.BackupConfig{
			BackupOptions: &psmdbv1.BackupOptions{
				Timeouts: &psmdbv1.BackupTimeouts{Starting: pointer.ToUint32(defaultBackupStartingTimeout)},
			},
		},
		Resources: backupAgentResources(backupAgent.Resources),
	}
	// These are PBM's cluster-wide "advanced" backup/restore settings: they
	// apply to every backup and restore the cluster runs, not to a single
	// run, so they are configured once on the backupAgent component rather
	// than per schedule or per Backup/Restore CR.
	applyBackupAgentParamsToBackupOptions(bs.Configuration.BackupOptions, agentParams)
	if restoreOptions := buildRestoreOptionsFromBackupAgentParams(agentParams); restoreOptions != nil {
		bs.Configuration.RestoreOptions = restoreOptions
	}

	backupCfg := c.Instance().Spec.Backup
	if backupCfg == nil || !backupCfg.Enabled {
		return bs, nil
	}

	pitrSpec, err := buildPSMDBPITRSpec(backupCfg.Storages)
	if err != nil {
		return psmdbv1.BackupSpec{}, &controller.BackupConfigError{
			Reason:  "PITRConfigInvalid",
			Message: err.Error(),
		}
	}
	tasks, err := buildPSMDBTasks(backupCfg.Storages)
	if err != nil {
		return psmdbv1.BackupSpec{}, &controller.BackupConfigError{
			Reason:  "RetentionTypeUnsupported",
			Message: err.Error(),
		}
	}

	live, err := liveEngine(c)
	if err != nil {
		return psmdbv1.BackupSpec{}, err
	}
	storages, err := buildPSMDBStorages(c, live.Spec.Backup.Storages)
	if err != nil {
		return psmdbv1.BackupSpec{}, err
	}
	// Schedules, PITR and the main storage only reach the storages the engine
	// gets: a task naming a missing storage stops the operator's reconcile.
	rendered := make([]corev1alpha1.InstanceBackupStorage, 0, len(storages))
	for _, s := range backupCfg.Storages {
		if _, ok := storages[s.StorageRef.Name]; ok {
			rendered = append(rendered, s)
		}
	}
	mainName := selectMainStorageName(rendered)
	for name, s := range storages {
		s.Main = name == mainName
		storages[name] = s
	}
	tasks = slices.DeleteFunc(tasks, func(t psmdbv1.BackupTaskSpec) bool {
		_, ok := storages[t.StorageName]
		return !ok
	})
	pitrSpec.Enabled = pitrSpec.Enabled && slices.ContainsFunc(rendered, func(s corev1alpha1.InstanceBackupStorage) bool {
		return s.PITR != nil && s.PITR.Enabled
	})
	bs.Enabled = true
	bs.Storages = storages
	bs.Tasks = tasks
	bs.PITR = pitrSpec
	return bs, nil
}

// backupAgentResources returns the backup-agent sidecar's resources. The
// defaults (1G memory, 300m CPU) are the minimum PBM needs to run reliably;
// the Instance's backupAgent component can override CPU and/or memory
// (limits and requests) individually, matching mongodResources' merge style.
func backupAgentResources(resources *corev1.ResourceRequirements) corev1.ResourceRequirements {
	out := corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("1G"),
			corev1.ResourceCPU:    resource.MustParse("300m"),
		},
	}
	if resources == nil {
		return out
	}
	if resources.Limits != nil {
		if !resources.Limits.Cpu().IsZero() {
			out.Limits[corev1.ResourceCPU] = *resources.Limits.Cpu()
		}
		if !resources.Limits.Memory().IsZero() {
			out.Limits[corev1.ResourceMemory] = *resources.Limits.Memory()
		}
	}
	if resources.Requests != nil {
		out.Requests = corev1.ResourceList{}
		if !resources.Requests.Cpu().IsZero() {
			out.Requests[corev1.ResourceCPU] = *resources.Requests.Cpu()
		}
		if !resources.Requests.Memory().IsZero() {
			out.Requests[corev1.ResourceMemory] = *resources.Requests.Memory()
		}
	}
	return out
}

// =============================================================================
// BACKUP CLASS PARAMETERS — decoding and applying PerconaBackupParameters,
// PerconaPITRParameters and PerconaRestoreParameters
// =============================================================================

// decodeBackupParameters unmarshals a Backup.spec.parameters or
// InstanceBackupSchedule.parameters payload into PerconaBackupParameters. A
// nil/empty payload decodes to the zero value, leaving engine defaults
// (type=logical, engine-default compression) in place.
func decodeBackupParameters(raw *runtime.RawExtension) (perconabackupmongodb.PerconaBackupParameters, error) {
	var params perconabackupmongodb.PerconaBackupParameters
	if raw == nil || raw.Raw == nil {
		return params, nil
	}
	if err := json.Unmarshal(raw.Raw, &params); err != nil {
		return params, fmt.Errorf("unmarshal backup parameters: %w", err)
	}
	return params, nil
}

// decodeRestoreParameters unmarshals a Restore.spec.parameters payload into
// PerconaRestoreParameters.
func decodeRestoreParameters(raw *runtime.RawExtension) (perconabackupmongodb.PerconaRestoreParameters, error) {
	var params perconabackupmongodb.PerconaRestoreParameters
	if raw == nil || raw.Raw == nil {
		return params, nil
	}
	if err := json.Unmarshal(raw.Raw, &params); err != nil {
		return params, fmt.Errorf("unmarshal restore parameters: %w", err)
	}
	return params, nil
}

// decodePITRParameters unmarshals an InstanceBackupStoragePITR.parameters
// payload into PerconaPITRParameters.
func decodePITRParameters(raw *runtime.RawExtension) (perconabackupmongodb.PerconaPITRParameters, error) {
	var params perconabackupmongodb.PerconaPITRParameters
	if raw == nil || raw.Raw == nil {
		return params, nil
	}
	if err := json.Unmarshal(raw.Raw, &params); err != nil {
		return params, fmt.Errorf("unmarshal PITR parameters: %w", err)
	}
	return params, nil
}

// decodeBackupAgentParameters unmarshals the backupAgent component's
// .parameters payload (spec.components.backupAgent.parameters) into
// components.BackupParameters. These are cluster-wide "advanced" PBM
// settings — such as NumParallelCollections — that apply to every backup or
// restore the cluster runs, as opposed to the per-schedule/per-run
// parameters in PerconaBackupParameters/PerconaRestoreParameters.
func decodeBackupAgentParameters(raw *runtime.RawExtension) (components.BackupParameters, error) {
	var params components.BackupParameters
	if raw == nil || raw.Raw == nil {
		return params, nil
	}
	if err := json.Unmarshal(raw.Raw, &params); err != nil {
		return params, fmt.Errorf("unmarshal backupAgent parameters: %w", err)
	}
	return params, nil
}

// applyBackupAgentParamsToBackupOptions copies the cluster-wide "advanced"
// backup settings from components.BackupParameters onto dst (PBM's
// backup.* cluster config, applied via psmdbv1.BackupOptions). dst must be
// non-nil. Unset fields leave PBM's own defaults in place.
func applyBackupAgentParamsToBackupOptions(dst *psmdbv1.BackupOptions, params components.BackupParameters) {
	if params.NumParallelCollections != nil {
		dst.NumParallelCollections = int(*params.NumParallelCollections)
	}
	if params.BackupOplogSpanMin != nil {
		dst.OplogSpanMin = *params.BackupOplogSpanMin
	}
	if params.Priority != nil {
		dst.Priority = params.Priority
	}
}

// buildRestoreOptionsFromBackupAgentParams returns the cluster-wide
// "advanced" restore settings from components.BackupParameters as PBM's
// restore.* cluster config, or nil when none of them are set (leaving
// Configuration.RestoreOptions unset so PSMDB applies its own defaults).
func buildRestoreOptionsFromBackupAgentParams(params components.BackupParameters) *psmdbv1.RestoreOptions {
	if params.RestoreNumParallelCollections == nil &&
		params.RestoreBatchSize == nil &&
		params.RestoreNumInsertionWorkers == nil &&
		params.RestoreNumDownloadWorkers == nil &&
		params.RestoreMaxDownloadBufferMB == nil &&
		params.RestoreDownloadChunkMB == nil {
		return nil
	}
	out := &psmdbv1.RestoreOptions{}
	if params.RestoreNumParallelCollections != nil {
		out.NumParallelCollections = int(*params.RestoreNumParallelCollections)
	}
	if params.RestoreBatchSize != nil {
		out.BatchSize = int(*params.RestoreBatchSize)
	}
	if params.RestoreNumInsertionWorkers != nil {
		out.NumInsertionWorkers = int(*params.RestoreNumInsertionWorkers)
	}
	if params.RestoreNumDownloadWorkers != nil {
		out.NumDownloadWorkers = int(*params.RestoreNumDownloadWorkers)
	}
	if params.RestoreMaxDownloadBufferMB != nil {
		out.MaxDownloadBufferMb = int(*params.RestoreMaxDownloadBufferMB)
	}
	if params.RestoreDownloadChunkMB != nil {
		out.DownloadChunkMb = int(*params.RestoreDownloadChunkMB)
	}
	return out
}

// applyBackupParamsToTask copies the user-chosen type, compression and (for
// external/snapshot backups) VolumeSnapshotClass onto a scheduled backup
// task. Unset fields leave the operator's own defaults in place.
func applyBackupParamsToTask(dst *psmdbv1.BackupTaskSpec, params perconabackupmongodb.PerconaBackupParameters) {
	if params.Type != "" {
		dst.Type = defs.BackupType(params.Type)
	}
	if params.CompressionType != "" {
		dst.CompressionType = compress.CompressionType(params.CompressionType)
	}
	if params.CompressionLevel != nil {
		level := int(*params.CompressionLevel)
		dst.CompressionLevel = &level
	}
	if params.VolumeSnapshotClass != "" {
		vsc := params.VolumeSnapshotClass
		dst.VolumeSnapshotClass = &vsc
	}
}

// applyBackupParamsToBackup is applyBackupParamsToTask's counterpart for an
// on-demand PerconaServerMongoDBBackup.
func applyBackupParamsToBackup(dst *psmdbv1.PerconaServerMongoDBBackupSpec, params perconabackupmongodb.PerconaBackupParameters) {
	if params.Type != "" {
		dst.Type = defs.BackupType(params.Type)
	}
	if params.CompressionType != "" {
		dst.Compression = compress.CompressionType(params.CompressionType)
	}
	if params.CompressionLevel != nil {
		level := int(*params.CompressionLevel)
		dst.CompressionLevel = &level
	}
	if params.VolumeSnapshotClass != "" {
		vsc := params.VolumeSnapshotClass
		dst.VolumeSnapshotClass = &vsc
	}
}

// pitrStorage returns the single storage entry with PITR enabled, or nil when
// no storage has PITR enabled. PSMDB only supports a single PITR stream per
// cluster, so configuring more than one PITR-enabled storage is rejected as a
// configuration error.
func pitrStorage(storages []corev1alpha1.InstanceBackupStorage) (*corev1alpha1.InstanceBackupStorage, error) {
	var found *corev1alpha1.InstanceBackupStorage
	var names []string
	for i := range storages {
		s := &storages[i]
		if s.PITR != nil && s.PITR.Enabled {
			found = s
			names = append(names, s.StorageRef.Name)
		}
	}
	if len(names) > 1 {
		return nil, fmt.Errorf("PSMDB supports at most one PITR-enabled storage, got %d: %v", len(names), names)
	}
	return found, nil
}

// buildPSMDBPITRSpec translates the Instance's per-storage PITR configuration
// into the engine's cluster-global PITR spec. PSMDB supports a single PITR
// stream, so PITR is enabled when exactly one storage has it enabled.
//
// The PITR-enabled storage's .pitr.parameters (oplog span, oplog-only mode,
// compression), validated against the BackupClass's pitrParametersSchema, are
// applied onto the cluster-global spec; unset fields leave PBM's defaults.
func buildPSMDBPITRSpec(storages []corev1alpha1.InstanceBackupStorage) (psmdbv1.PITRSpec, error) {
	storage, err := pitrStorage(storages)
	if err != nil {
		return psmdbv1.PITRSpec{}, err
	}
	if storage == nil {
		return psmdbv1.PITRSpec{}, nil
	}

	params, err := decodePITRParameters(storage.PITR.Parameters)
	if err != nil {
		return psmdbv1.PITRSpec{}, fmt.Errorf("storage %q: %w", storage.StorageRef.Name, err)
	}

	spec := psmdbv1.PITRSpec{Enabled: true, OplogOnly: params.OplogOnly}
	if params.OplogSpanMin != "" {
		spec.OplogSpanMin = numstr.NumberString(params.OplogSpanMin)
	}
	if params.CompressionType != "" {
		spec.CompressionType = compress.CompressionType(params.CompressionType)
	}
	if params.CompressionLevel != nil {
		level := int(*params.CompressionLevel)
		spec.CompressionLevel = &level
	}
	return spec, nil
}

// =============================================================================
// BACKUPPROVIDER — runtime hooks for ProviderManaged BackupClasses
// =============================================================================

// buildPSMDBTasks flattens the per-storage schedule lists into the PSMDB
// operator's BackupTaskSpec list. Each task's StorageName is taken from the
// parent storage entry. Disabled schedules are still recorded (the operator
// honors the Enabled flag) so that toggling a schedule off does not lose its
// definition. An unset Retention means "keep all". The PSMDB operator only
// supports count-based retention, so time-based retention is rejected. Each
// schedule's .parameters (type, compression, volumeSnapshotClass) are applied
// onto the task; unset fields leave the operator's defaults.
func buildPSMDBTasks(storages []corev1alpha1.InstanceBackupStorage) ([]psmdbv1.BackupTaskSpec, error) {
	var out []psmdbv1.BackupTaskSpec
	for _, st := range storages {
		for _, s := range st.Schedules {
			task := psmdbv1.BackupTaskSpec{
				Name:        s.Name,
				Enabled:     s.Enabled,
				Schedule:    s.Cron,
				StorageName: st.StorageRef.Name,
			}
			params, err := decodeBackupParameters(s.Parameters)
			if err != nil {
				return nil, fmt.Errorf("schedule %q: %w", s.Name, err)
			}
			applyBackupParamsToTask(&task, params)
			if s.Retention != nil {
				if s.Retention.Type != corev1alpha1.BackupScheduleRetentionTypeCount {
					return nil, fmt.Errorf("schedule %q: %s retention is not supported by PSMDB, use count", s.Name, s.Retention.Type)
				}
				task.Retention = &psmdbv1.BackupTaskSpecRetention{
					Type:              psmdbv1.BackupTaskSpecRetentionTypeCount,
					Count:             int(*s.Retention.Count),
					DeleteFromStorage: true,
				}
			}
			out = append(out, task)
		}
	}
	return out, nil
}

// Mirror implements controller.BackupMirror. The runtime invokes Mirror once
// per PerconaServerMongoDBBackup event; returning a non-nil Backup causes the
// runtime to create it (idempotently). For on-demand backups (no task
// annotation) and for operator backups whose parent Instance is missing or
// owned by a different provider, Mirror returns nil to skip.
func (p *PSMDBProvider) Mirror(ctx context.Context, c client.Client, obj client.Object) (*backupv1alpha1.Backup, error) {
	ub, ok := obj.(*psmdbv1.PerconaServerMongoDBBackup)
	if !ok {
		return nil, nil
	}
	taskName := ub.Labels[psmdbBackupAncestorLabel]
	if taskName == "" {
		// On-demand backups originate from a Backup CR; no mirroring needed.
		return nil, nil
	}
	instance := &corev1alpha1.Instance{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: ub.Namespace, Name: ub.Spec.ClusterName}, instance); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get parent Instance %q: %w", ub.Spec.ClusterName, err)
	}
	if instance.Spec.ProviderRef.Name != p.Name() {
		return nil, nil
	}
	if instance.Spec.Backup == nil || instance.Spec.Backup.ClassRef.Name == "" {
		return nil, nil
	}
	return &backupv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ub.Name,
			Namespace: ub.Namespace,
		},
		Spec: backupv1alpha1.BackupSpec{
			Origin: backupv1alpha1.BackupOrigin{
				Type:        backupv1alpha1.BackupOriginTypeInstance,
				InstanceRef: &commonv1alpha1.ObjectRef{Name: ub.Spec.ClusterName},
			},
			ClassRef:     commonv1alpha1.ObjectRef{Name: instance.Spec.Backup.ClassRef.Name},
			StorageRef:   commonv1alpha1.ObjectRef{Name: ub.Spec.StorageName},
			ScheduleName: taskName,
		},
	}, nil
}

// OperatorBackupType implements controller.BackupMirror.
func (p *PSMDBProvider) OperatorBackupType() client.Object {
	return &psmdbv1.PerconaServerMongoDBBackup{}
}

// selectMainStorageName returns the name of the storage that should be
// designated as the PBM main storage. The PITR-enabled storage is always
// preferred because PSMDB requires PITR to write to the main storage. When no
// storage has PITR enabled the first entry in the slice is used as the
// fallback. An empty slice returns "".
func selectMainStorageName(storages []corev1alpha1.InstanceBackupStorage) string {
	for _, s := range storages {
		if s.PITR != nil && s.PITR.Enabled {
			return s.StorageRef.Name
		}
	}
	if len(storages) > 0 {
		return storages[0].StorageRef.Name
	}
	return ""
}

// buildPSMDBStorages renders the Instance's backup storages as the engine's
// storages, keyed by BackupStorage name, each writing under the Instance's
// path in its storage. An entry that must not or cannot be rendered keeps what
// the live engine has for it, and stays out while the engine has nothing.
func buildPSMDBStorages(
	c *controller.Context,
	live map[string]psmdbv1.BackupStorageSpec,
) (map[string]psmdbv1.BackupStorageSpec, error) {
	resolved, err := c.BackupStorages()
	if err != nil {
		return nil, err
	}
	out := make(map[string]psmdbv1.BackupStorageSpec, len(resolved))
	for _, s := range resolved {
		if s.Apply() {
			spec, err := psmdbStorage(c, s.Storage, s.Path)
			if err == nil {
				out[s.Name()] = spec
				continue
			}
			c.RejectBackupStorage(s.Name(), err)
		}
		if held, ok := live[s.Name()]; ok {
			out[s.Name()] = held
		}
	}
	return out, nil
}

// psmdbStorage renders an s3 BackupStorage as a PBM storage writing under
// prefix in its bucket. Amazon S3 uses PBM's s3 type; S3-compatible stores,
// and a custom CA which the s3 type cannot carry, use its minio type.
func psmdbStorage(c *controller.Context, bs *backupv1alpha1.BackupStorage, prefix string) (psmdbv1.BackupStorageSpec, error) {
	s3, _, err := c.BackupStorageS3(bs)
	if err != nil {
		return psmdbv1.BackupStorageSpec{}, err
	}
	implementation := s3.EffectiveImplementation()
	if implementation == s3class.ImplementationGCS {
		return psmdbv1.BackupStorageSpec{}, fmt.Errorf("PBM cannot use GCS through its S3 client: %w", controller.ErrImplementationUnsupported)
	}
	tls, err := c.BackupStorageTLS(bs, backupclasses.TLSSupport(backupClassName, s3class.ClassName))
	if err != nil {
		return psmdbv1.BackupStorageSpec{}, err
	}
	// PBM reads the AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY of the storage's Secret.
	credentials := bs.Spec.Authentication.CredentialsSecretRef.Name

	if implementation == s3class.ImplementationAWS && tls.CASecretKeyRef == nil {
		return psmdbv1.BackupStorageSpec{
			Type: psmdbv1.BackupStorageS3,
			S3: psmdbv1.BackupStorageS3Spec{
				Bucket:                bs.Spec.Location.ObjectStore.Bucket,
				Prefix:                prefix,
				Region:                s3.Region,
				EndpointURL:           s3.EndpointURL,
				CredentialsSecret:     credentials,
				InsecureSkipTLSVerify: tls.InsecureSkipVerify,
				ForcePathStyle:        &s3.ForcePathStyle,
			},
		}, nil
	}

	endpoint, err := s3.Endpoint()
	if err != nil {
		return psmdbv1.BackupStorageSpec{}, err
	}
	spec := psmdbv1.BackupStorageSpec{
		Type: psmdbv1.BackupStorageMinio,
		Minio: psmdbv1.BackupStorageMinioSpec{
			Bucket: bs.Spec.Location.ObjectStore.Bucket,
			Prefix: prefix,
			Region: s3.Region,
			// The minio client takes host:port and the scheme as Secure.
			EndpointURL:           endpoint.HostPort(),
			Secure:                endpoint.Secure,
			CredentialsSecret:     credentials,
			InsecureSkipTLSVerify: tls.InsecureSkipVerify,
			ForcePathStyle:        &s3.ForcePathStyle,
		},
	}
	if ref := tls.CASecretKeyRef; ref != nil {
		spec.Minio.CABundle = &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: ref.Name},
			Key:                  ref.Key,
		}
	}
	return spec, nil
}

// SyncBackup creates or updates a PerconaServerMongoDBBackup that references
// the operator-registered storage matching backup.spec.storageRef.name, then
// maps operator status into the BackupExecutionStatus the runtime expects.
// backup.spec.parameters (type, compression, volumeSnapshotClass) are applied
// onto the operator backup; unset fields leave the operator's defaults.
func (p *PSMDBProvider) SyncBackup(c *controller.Context, backup *backupv1alpha1.Backup) (controller.BackupExecutionStatus, error) {
	psmdb := &psmdbv1.PerconaServerMongoDB{}
	if err := c.Get(psmdb, c.Name()); err != nil {
		if apierrors.IsNotFound(err) {
			return controller.BackupExecutionStatus{
				State:   backupv1alpha1.BackupStatePending,
				Message: "Waiting for PerconaServerMongoDB cluster to exist",
			}, nil
		}
		return controller.BackupExecutionStatus{}, fmt.Errorf("get PSMDB: %w", err)
	}

	// The storage name on the PSMDB cluster matches the BackupStorage CR name
	// on the Instance and on the Backup CR. Reject up-front if the user pointed
	// at a storage the cluster doesn't know about.
	if _, ok := psmdb.Spec.Backup.Storages[backup.Spec.StorageRef.Name]; !ok {
		return controller.BackupExecutionStatus{
			State:   backupv1alpha1.BackupStatePending,
			Message: fmt.Sprintf("Waiting for storage %q to be registered on PSMDB cluster", backup.Spec.StorageRef.Name),
		}, nil
	}

	params, err := decodeBackupParameters(backup.Spec.Parameters)
	if err != nil {
		return controller.BackupExecutionStatus{}, fmt.Errorf("decode backup parameters: %w", err)
	}

	psmdbBackup := &psmdbv1.PerconaServerMongoDBBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      backup.Name,
			Namespace: backup.Namespace,
		},
	}

	if err := c.Get(psmdbBackup, backup.Name); err != nil {
		if !apierrors.IsNotFound(err) {
			return controller.BackupExecutionStatus{}, fmt.Errorf("get PSMDB backup: %w", err)
		}
		// Enabling backups adds the backup-agent container, which the operator
		// rolls out with SmartUpdate. SmartUpdate waits for running backups, and
		// a backup requested before the rollout waits for the agent, so the two
		// deadlock. Request the backup only once the cluster has rolled out.
		if !psmdbRolledOut(psmdb) {
			return controller.BackupExecutionStatus{
				State:   backupv1alpha1.BackupStatePending,
				Message: "Waiting for the PerconaServerMongoDB cluster to apply its latest configuration",
			}, nil
		}
	}

	if _, err := controllerutil.CreateOrUpdate(c.Context(), c.Client(), psmdbBackup, func() error {
		psmdbBackup.Spec.ClusterName = c.Name()
		psmdbBackup.Spec.StorageName = backup.Spec.StorageRef.Name
		applyBackupParamsToBackup(&psmdbBackup.Spec, params)
		controllerutil.AddFinalizer(psmdbBackup, psmdbDeleteBackupFinalizer)
		return controllerutil.SetControllerReference(backup, psmdbBackup, c.Client().Scheme())
	}); err != nil {
		return controller.BackupExecutionStatus{}, fmt.Errorf("create or update PSMDB backup: %w", err)
	}

	exec := controller.BackupExecutionStatus{
		OperatorBackupRef: &commonv1alpha1.TypedObjectRef{
			Group: psmdbv1.SchemeGroupVersion.Group,
			Kind:  "PerconaServerMongoDBBackup",
			Name:  psmdbBackup.Name,
		},
	}
	switch psmdbBackup.Status.State {
	case psmdbv1.BackupStateReady:
		exec.State = backupv1alpha1.BackupStateSucceeded
		now := metav1.Now()
		exec.CompletedAt = &now
	case psmdbv1.BackupStateError:
		exec.State = backupv1alpha1.BackupStateFailed
		exec.Message = psmdbBackup.Status.Error
	case psmdbv1.BackupStateRequested, psmdbv1.BackupStateRunning, psmdbv1.BackupStateWaiting:
		exec.State = backupv1alpha1.BackupStateRunning
	default:
		exec.State = backupv1alpha1.BackupStatePending
	}
	return exec, nil
}

// psmdbRolledOut reports whether the operator has applied the cluster's latest
// spec and the cluster is ready, e.g. backup agents are running after backups
// were enabled.
func psmdbRolledOut(psmdb *psmdbv1.PerconaServerMongoDB) bool {
	return psmdb.Status.ObservedGeneration == psmdb.Generation && psmdb.Status.State == psmdbv1.AppStateReady
}

// SyncRestore creates or updates a PerconaServerMongoDBRestore that points at
// the operator backup the Restore's data source resolves to: the mirror of the
// named Backup CR for type Backup, or the base backup selected for the stream
// for type PointInTime (PBM replays the oplog forward from it).
//
// Two cases are handled:
//
//   - Same-cluster restore (source Backup.spec.origin.instanceRef.name == this
//     Instance): the operator backup lives on the same PSMDB cluster, so we
//     set .spec.backupName to its name.
//   - Cross-cluster restore (e.g. seeding a new Instance from another
//     Instance's Backup via .spec.dataSource): the source operator backup
//     belongs to a different PSMDB CR. The PSMDB operator cannot resolve it
//     by name on this cluster, so we copy its .status into
//     .spec.backupSource, which records where the source wrote it.
func (p *PSMDBProvider) SyncRestore(c *controller.Context, restore *backupv1alpha1.Restore) (controller.RestoreExecutionStatus, error) {
	sourceBackup, desiredPITR, exec, err := resolveRestoreSource(c, restore)
	if err != nil {
		return controller.RestoreExecutionStatus{}, err
	}
	if sourceBackup == nil {
		return exec, nil
	}

	restoreParams, err := decodeRestoreParameters(restore.Spec.Parameters)
	if err != nil {
		return controller.RestoreExecutionStatus{}, fmt.Errorf("decode restore parameters: %w", err)
	}

	operatorBackupName := sourceBackup.Name
	if sourceBackup.Status.OperatorBackupRef != nil && sourceBackup.Status.OperatorBackupRef.Name != "" {
		operatorBackupName = sourceBackup.Status.OperatorBackupRef.Name
	}

	var externalSource *psmdbv1.PerconaServerMongoDBBackupStatus
	var sourceOpBackup *psmdbv1.PerconaServerMongoDBBackup
	var crossCluster bool

	external := sourceBackup.Spec.Origin.Type == backupv1alpha1.BackupOriginTypeExternal

	switch sourceBackup.Spec.Origin.Type {
	case backupv1alpha1.BackupOriginTypeExternal:
		// External backups reference data already sitting in a BackupStorage;
		// there is no PerconaServerMongoDBBackup to resolve by name.
		// Build the restore's BackupSource directly from the storage
		// descriptor and external path.
		externalSource, exec, err = buildExternalBackupSource(c, sourceBackup)
		if err != nil {
			return controller.RestoreExecutionStatus{}, err
		}

		if externalSource == nil {
			return exec, nil
		}

	case backupv1alpha1.BackupOriginTypeInstance:
		// Detect cross-cluster restores. When the source Backup was produced by a
		// different live Instance, fetch its PerconaServerMongoDBBackup so we can copy
		// the destination/storage spec into Spec.BackupSource.
		crossCluster = sourceBackup.Spec.Origin.InstanceRef != nil &&
			sourceBackup.Spec.Origin.InstanceRef.Name != c.Name()
		if crossCluster {
			sourceOpBackup = &psmdbv1.PerconaServerMongoDBBackup{}
			if err := c.Get(sourceOpBackup, operatorBackupName); err != nil {
				if apierrors.IsNotFound(err) {
					return controller.RestoreExecutionStatus{
						State:   backupv1alpha1.RestoreStatePending,
						Message: fmt.Sprintf("waiting for source PerconaServerMongoDBBackup %q", operatorBackupName),
					}, nil
				}
				return controller.RestoreExecutionStatus{}, fmt.Errorf("get source PSMDB backup %q: %w", operatorBackupName, err)
			}
			if sourceOpBackup.Status.Destination == "" {
				return controller.RestoreExecutionStatus{
					State:   backupv1alpha1.RestoreStatePending,
					Message: fmt.Sprintf("source PerconaServerMongoDBBackup %q has no destination yet", operatorBackupName),
				}, nil
			}
		}

	default:
		return controller.RestoreExecutionStatus{}, fmt.Errorf("unsupported restore source type %q", sourceBackup.Spec.Origin.Type)
	}

	psmdbRestore := &psmdbv1.PerconaServerMongoDBRestore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      restore.Name,
			Namespace: restore.Namespace,
		},
	}

	if _, err := controllerutil.CreateOrUpdate(c.Context(), c.Client(), psmdbRestore, func() error {
		psmdbRestore.Spec.ClusterName = c.Name()
		switch {
		case external:
			// PSMDB reads the dump straight from the storage descriptor built
			// from the Backup's storageRef and external path. spec.storageName is
			// deliberately left empty: when set, the operator resolves the
			// cluster's registered storage, which writes under this Instance's
			// own path, instead of the spec.backupSource descriptor.
			psmdbRestore.Spec.BackupSource = externalSource
			psmdbRestore.Spec.StorageName = ""
			psmdbRestore.Spec.BackupName = ""
		case crossCluster:
			// Copy the source operator backup's storage descriptor verbatim
			// (Destination + S3/Azure/GCS/Minio/Filesystem spec) so PSMDB can
			// read the dump without consulting its own backup list. As for
			// external backups, spec.storageName stays empty: this cluster's
			// storage of the same name writes under another path.
			src := sourceOpBackup.Status
			psmdbRestore.Spec.BackupSource = &psmdbv1.PerconaServerMongoDBBackupStatus{
				Destination: src.Destination,
				StorageName: sourceBackup.Spec.StorageRef.Name,
				S3:          src.S3,
				Azure:       src.Azure,
				GCS:         src.GCS,
				Minio:       src.Minio,
				Filesystem:  src.Filesystem,
				Type:        src.Type,
				PBMname:     src.PBMname,
			}
			psmdbRestore.Spec.StorageName = ""
			psmdbRestore.Spec.BackupName = ""
		default:
			psmdbRestore.Spec.BackupName = operatorBackupName
			psmdbRestore.Spec.BackupSource = nil
		}
		psmdbRestore.Spec.PITR = desiredPITR
		applyRestoreParams(&psmdbRestore.Spec, restoreParams)
		return controllerutil.SetControllerReference(restore, psmdbRestore, c.Client().Scheme())
	}); err != nil {
		return controller.RestoreExecutionStatus{}, fmt.Errorf("create or update PSMDB restore: %w", err)
	}

	out := controller.RestoreExecutionStatus{
		OperatorRestoreRef: &commonv1alpha1.TypedObjectRef{
			Group: psmdbv1.SchemeGroupVersion.Group,
			Kind:  "PerconaServerMongoDBRestore",
			Name:  psmdbRestore.Name,
		},
	}
	switch psmdbRestore.Status.State {
	case psmdbv1.RestoreStateReady:
		out.State = backupv1alpha1.RestoreStateSucceeded
		now := metav1.Now()
		out.CompletedAt = &now
	case psmdbv1.RestoreStateError:
		out.State = backupv1alpha1.RestoreStateFailed
		out.Message = psmdbRestore.Status.Error
	case psmdbv1.RestoreStateRequested, psmdbv1.RestoreStateRunning, psmdbv1.RestoreStateWaiting:
		out.State = backupv1alpha1.RestoreStateRunning
	default:
		out.State = backupv1alpha1.RestoreStatePending
	}
	return out, nil
}

// applyRestoreParams copies the user-chosen selective-restore options and
// replset remapping onto the operator restore. Unset fields leave the
// operator's defaults (restore everything, no remapping) in place.
func applyRestoreParams(dst *psmdbv1.PerconaServerMongoDBRestoreSpec, params perconabackupmongodb.PerconaRestoreParameters) {
	if params.Selective != nil {
		dst.Selective = &psmdbv1.SelectiveRestoreOpts{
			WithUsersAndRoles: params.Selective.WithUsersAndRoles,
			Namespaces:        params.Selective.Namespaces,
			NamespaceFrom:     params.Selective.NamespaceFrom,
			NamespaceTo:       params.Selective.NamespaceTo,
		}
	}
	if len(params.ReplsetRemapping) > 0 {
		dst.RSMap = params.ReplsetRemapping
	}
}

// buildExternalBackupSource builds the PSMDB restore BackupSource for an
// external Backup (origin.type External). The data already lives at
// origin.external.path within the referenced BackupStorage's location, so the
// descriptor is rendered from the storage like an Instance's storages are.
func buildExternalBackupSource(
	c *controller.Context,
	sourceBackup *backupv1alpha1.Backup,
) (*psmdbv1.PerconaServerMongoDBBackupStatus, controller.RestoreExecutionStatus, error) {
	external := sourceBackup.Spec.Origin.External
	if external == nil || external.Path == "" {
		return nil, controller.RestoreExecutionStatus{
			State:   backupv1alpha1.RestoreStateFailed,
			Message: "External source Backup is missing origin.external.path",
		}, nil
	}

	bs, err := c.BackupStorage(sourceBackup.Spec.StorageRef.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, controller.RestoreExecutionStatus{
				State:   backupv1alpha1.RestoreStatePending,
				Message: fmt.Sprintf("Waiting for BackupStorage %q", sourceBackup.Spec.StorageRef.Name),
			}, nil
		}

		return nil, controller.RestoreExecutionStatus{}, fmt.Errorf("get BackupStorage: %w", err)
	}

	key := path.Join(bs.Spec.Location.ObjectStore.Prefix, strings.Trim(external.Path, "/"))
	var prefix string
	if i := strings.LastIndex(key, "/"); i >= 0 {
		prefix = key[:i]
	}

	spec, err := psmdbStorage(c, bs, prefix)
	if err != nil {
		var apiErr apierrors.APIStatus
		switch {
		case apierrors.IsNotFound(err):
			return nil, controller.RestoreExecutionStatus{
				State:   backupv1alpha1.RestoreStatePending,
				Message: fmt.Sprintf("Waiting for BackupStorage %q: %v", bs.Name, err),
			}, nil
		case errors.As(err, &apiErr):
			return nil, controller.RestoreExecutionStatus{}, fmt.Errorf("render BackupStorage %q: %w", bs.Name, err)
		}
		return nil, controller.RestoreExecutionStatus{
			State:   backupv1alpha1.RestoreStateFailed,
			Message: fmt.Sprintf("BackupStorage %q cannot be used: %v", bs.Name, err),
		}, nil
	}

	source := &psmdbv1.PerconaServerMongoDBBackupStatus{
		Destination: "s3://" + bs.Spec.Location.ObjectStore.Bucket + "/" + key,
		StorageName: sourceBackup.Spec.StorageRef.Name,
	}
	if spec.Type == psmdbv1.BackupStorageMinio {
		source.Minio = &spec.Minio
	} else {
		source.S3 = &spec.S3
	}
	return source, controller.RestoreExecutionStatus{}, nil
}

// resolveRestoreSource translates the Restore's data source into the operator
// inputs: the Backup CR whose operator mirror the restore reads from, and the
// PITR block when rolling the oplog stream forward.
//
// A nil Backup means the source is not usable yet (or at all) and the caller
// should surface the returned status verbatim.
func resolveRestoreSource(
	c *controller.Context,
	restore *backupv1alpha1.Restore,
) (*backupv1alpha1.Backup, *psmdbv1.PITRestoreSpec, controller.RestoreExecutionStatus, error) {
	switch restore.Spec.DataSource.Type {
	case backupv1alpha1.DataSourceTypeBackup:
		backup, exec, err := resolveBackupSource(c, restore)
		return backup, nil, exec, err
	case backupv1alpha1.DataSourceTypePointInTime:
		return resolvePointInTimeSource(c, restore)
	default:
		return nil, nil, controller.RestoreExecutionStatus{
			State:   backupv1alpha1.RestoreStateFailed,
			Message: fmt.Sprintf("Unsupported dataSource type %q", restore.Spec.DataSource.Type),
		}, nil
	}
}

// resolveBackupSource restores the state captured by a named Backup CR. The
// Backup mirrors an operator backup of the same name.
func resolveBackupSource(
	c *controller.Context,
	restore *backupv1alpha1.Restore,
) (*backupv1alpha1.Backup, controller.RestoreExecutionStatus, error) {
	ref := restore.Spec.DataSource.Backup
	if ref == nil || ref.BackupRef.Name == "" {
		return nil, controller.RestoreExecutionStatus{
			State:   backupv1alpha1.RestoreStateFailed,
			Message: "Restore dataSource.backup.backupRef.name is required",
		}, nil
	}
	backup := &backupv1alpha1.Backup{}
	if err := c.Get(backup, ref.BackupRef.Name); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, controller.RestoreExecutionStatus{
				State:   backupv1alpha1.RestoreStatePending,
				Message: "Waiting for source Backup",
			}, nil
		}
		return nil, controller.RestoreExecutionStatus{}, fmt.Errorf("get source Backup: %w", err)
	}
	if backup.Status.State == backupv1alpha1.BackupStateFailed {
		return nil, controller.RestoreExecutionStatus{
			State:   backupv1alpha1.RestoreStateFailed,
			Message: "Source Backup failed; cannot restore",
		}, nil
	}
	return backup, controller.RestoreExecutionStatus{}, nil
}

// resolvePointInTimeSource rolls the oplog stream forward to a recovery target.
//
// The client names the stream (source Instance + storage) and the target, never
// a backup: PBM needs a base backup to restore before replaying the oplog, and
// selecting it is engine knowledge. The base is the newest Succeeded backup on
// the stream that completed at or before the target.
func resolvePointInTimeSource(
	c *controller.Context,
	restore *backupv1alpha1.Restore,
) (*backupv1alpha1.Backup, *psmdbv1.PITRestoreSpec, controller.RestoreExecutionStatus, error) {
	pitr := restore.Spec.DataSource.PointInTime
	if pitr == nil {
		return nil, nil, controller.RestoreExecutionStatus{
			State:   backupv1alpha1.RestoreStateFailed,
			Message: "Restore dataSource.pointInTime is required when type is \"PointInTime\"",
		}, nil
	}
	// A schema rule already enforces this; repeated for paths that bypass
	// admission.
	if pitr.RecoveryTarget == backupv1alpha1.RecoveryTargetDate && pitr.Date == nil {
		return nil, nil, controller.RestoreExecutionStatus{
			State:   backupv1alpha1.RestoreStateFailed,
			Message: "Restore dataSource.pointInTime.date is required when recoveryTarget is \"date\"",
		}, nil
	}

	sourceInstance := restore.Spec.InstanceRef.Name
	if pitr.Source.InstanceRef != nil {
		sourceInstance = pitr.Source.InstanceRef.Name
	}

	base, pending := selectPITRBaseBackup(c, restore.Namespace, sourceInstance, pitr.Source.StorageRef.Name, pitr)
	if pending != nil {
		return nil, nil, *pending, nil
	}

	out := &psmdbv1.PITRestoreSpec{
		Type: psmdbv1.PITRestoreType(pitr.RecoveryTarget),
	}
	if pitr.Date != nil {
		// PSMDB parses the timezone-less date as node-local time, so normalise
		// to UTC first.
		out.Date = &psmdbv1.PITRestoreDate{Time: metav1.NewTime(pitr.Date.UTC())}
	}

	return base, out, controller.RestoreExecutionStatus{}, nil
}

// selectPITRBaseBackup returns the newest Succeeded Backup on the given stream
// that completed at or before the recovery target. The oplog is replayed
// forward from it, so a base completed *after* the target cannot be used.
func selectPITRBaseBackup(
	c *controller.Context,
	namespace, instanceName, storageName string,
	pitr *backupv1alpha1.DataSourcePointInTime,
) (*backupv1alpha1.Backup, *controller.RestoreExecutionStatus) {
	list := &backupv1alpha1.BackupList{}
	if err := c.Client().List(c.Context(), list,
		client.InNamespace(namespace),
		client.MatchingFields{controller.IndexBackupInstanceName: instanceName},
	); err != nil {
		return nil, &controller.RestoreExecutionStatus{
			State:   backupv1alpha1.RestoreStatePending,
			Message: fmt.Sprintf("Listing backups for instance %q: %v", instanceName, err),
		}
	}

	var best *backupv1alpha1.Backup
	for i := range list.Items {
		b := &list.Items[i]
		if b.Spec.StorageRef.Name != storageName ||
			b.Status.State != backupv1alpha1.BackupStateSucceeded ||
			b.Status.CompletedAt == nil {
			continue
		}
		if pitr.Date != nil && b.Status.CompletedAt.After(pitr.Date.Time) {
			continue
		}
		if best == nil || b.Status.CompletedAt.After(best.Status.CompletedAt.Time) {
			best = b
		}
	}

	if best == nil {
		msg := fmt.Sprintf("No Succeeded backup of instance %q on storage %q to recover from", instanceName, storageName)
		if pitr.Date != nil {
			msg = fmt.Sprintf("%s at or before %s", msg, pitr.Date.UTC().Format(time.RFC3339))
		}
		return nil, &controller.RestoreExecutionStatus{
			State:   backupv1alpha1.RestoreStatePending,
			Message: msg,
		}
	}

	return best, nil
}

// CleanupBackup removes the operator PerconaServerMongoDBBackup, honoring the
// Backup's DeletionPolicy:
//
//   - Delete (default): leave the percona.com/delete-backup finalizer in
//     place and issue a Delete on the PerconaServerMongoDBBackup. The PSMDB
//     operator will run PBM's delete-backup against the storage to purge
//     the underlying object, then release its own finalizer.
//   - Retain: strip the percona.com/delete-backup finalizer first so PSMDB
//     does NOT touch the storage, then delete the CR. The S3 object is
//     left in place for out-of-band recovery or manual cleanup.
//
// In both cases CleanupBackup returns done=true only once the
// PerconaServerMongoDBBackup has fully gone away.
func (p *PSMDBProvider) CleanupBackup(c *controller.Context, backup *backupv1alpha1.Backup) (bool, error) {
	psmdbBackup := &psmdbv1.PerconaServerMongoDBBackup{}
	err := c.Get(psmdbBackup, backup.Name)
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("get PSMDB backup for cleanup: %w", err)
	}

	if backup.Spec.DeletionPolicy == backupv1alpha1.BackupDeletionPolicyRetain {
		// Strip the storage-protection finalizer BEFORE deletion so the
		// PSMDB operator skips its delete-backup hook and the underlying
		// S3 object is preserved.
		if controllerutil.RemoveFinalizer(psmdbBackup, psmdbDeleteBackupFinalizer) {
			if err := c.Client().Update(c.Context(), psmdbBackup); err != nil {
				return false, fmt.Errorf("remove finalizer from PSMDB backup: %w", err)
			}
		}
	}

	if psmdbBackup.DeletionTimestamp.IsZero() {
		if err := c.Delete(psmdbBackup); err != nil {
			return false, fmt.Errorf("delete PSMDB backup: %w", err)
		}
	}
	// For the Delete path, requeue until PSMDB has run its delete-backup
	// finalizer and the CR is gone. For the Retain path, the CR will
	// disappear as soon as our finalizer-strip update commits.
	return false, nil
}

// CleanupRestore deletes the PerconaServerMongoDBRestore. The restore CR is
// run-to-completion and carries no protective finalizer, so a single delete
// is sufficient.
func (p *PSMDBProvider) CleanupRestore(c *controller.Context, restore *backupv1alpha1.Restore) (bool, error) {
	psmdbRestore := &psmdbv1.PerconaServerMongoDBRestore{}
	err := c.Get(psmdbRestore, restore.Name)
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("get PSMDB restore for cleanup: %w", err)
	}
	if psmdbRestore.DeletionTimestamp.IsZero() {
		if err := c.Delete(psmdbRestore); err != nil {
			return false, fmt.Errorf("delete PSMDB restore: %w", err)
		}
	}
	return false, nil
}

// enqueueRestoreInstance maps a Restore event to a reconcile request for the
// Instance it targets, so the Instance's status tracks the restore's lifecycle.
func enqueueRestoreInstance() func(ctx context.Context, obj client.Object) []reconcile.Request {
	return func(_ context.Context, obj client.Object) []reconcile.Request {
		r, ok := obj.(*backupv1alpha1.Restore)
		if !ok || r.Spec.InstanceRef.Name == "" {
			return nil
		}
		return []reconcile.Request{{
			NamespacedName: types.NamespacedName{
				Namespace: r.Namespace,
				Name:      r.Spec.InstanceRef.Name,
			},
		}}
	}
}
