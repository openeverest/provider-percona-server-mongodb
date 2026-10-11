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
	"regexp"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation/field"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
)

// maxInstanceNameLength limits cluster names to 22 characters to ensure generated
// resource names (with suffixes like -credentials, -backup-<timestamp>, etc.)
// stay under Kubernetes' Pod name 63-character limit.
const maxInstanceNameLength = 22

var (
	// Minimum resource requirements
	minStorage = resource.MustParse("1Gi")
	minCPU     = resource.MustParse("600m") // 600 millicores
	minMemory  = resource.MustParse("512Mi")
)

// rfc1035Regexp validates RFC1035 compatible names.
// https://kubernetes.io/docs/concepts/overview/working-with-objects/names/#rfc-1035-label-names
var rfc1035Regexp = regexp.MustCompile("^[a-z]([-a-z0-9]{0,61}[a-z0-9])?$")

// supportedServiceTypes are the service types the operator can expose.
var supportedServiceTypes = []corev1.ServiceType{
	corev1.ServiceTypeClusterIP,
	corev1.ServiceTypeLoadBalancer,
	corev1.ServiceTypeNodePort,
}

var componentsPath = field.NewPath("spec", "components")

// Validate checks the rules about the Instance alone. The runtime runs it once
// the Instance matches the topologies, components, versions and parameter
// schemas the Provider declares.
func (p *PSMDBProvider) Validate(in controller.ValidationInput) controller.ValidationResult {
	instance := in.Instance

	errs := validateName(instance.Name)
	errs = append(errs, validateDataSource(instance)...)
	errs = append(errs, validateEngine(instance.Spec.Components)...)
	if instance.GetTopologyType() == "sharded" {
		errs = append(errs, validateShardedTopology(instance.Spec.Components)...)
	}

	return controller.ValidationResult{Errors: errs}
}

// ValidateReferences checks the rules that read other objects: the user
// Secret, the source Backup and the MonitoringConfig.
func (p *PSMDBProvider) ValidateReferences(c *controller.Context) error {
	if err := validateUserSecret(c); err != nil {
		return fmt.Errorf("userSecret validation failed: %w", err)
	}

	if err := validateMonitoring(c); err != nil {
		return fmt.Errorf("monitoring validation failed: %w", err)
	}

	return nil
}

// validateName checks the Instance name.
func validateName(name string) field.ErrorList {
	path := field.NewPath("metadata", "name")

	switch {
	case name == "":
		return field.ErrorList{field.Required(path, "")}
	case !rfc1035Regexp.MatchString(name):
		return field.ErrorList{field.Invalid(path, name,
			"must be RFC-1035 compliant (lowercase alphanumeric or '-', start with a letter and end with an alphanumeric)",
		)}
	case len(name) > maxInstanceNameLength:
		return field.ErrorList{field.TooLong(path, name, maxInstanceNameLength)}
	}

	return nil
}

// validateDataSource checks the dataSource fields the Instance carries.
func validateDataSource(in *corev1alpha1.Instance) field.ErrorList {
	ds := in.Spec.DataSource
	path := field.NewPath("spec", "dataSource")

	if ds == nil {
		return nil
	}

	switch ds.Type {
	case backupv1alpha1.DataSourceTypeBackup:
		if ds.Backup == nil {
			return field.ErrorList{field.Required(path.Child("backup"), "")}
		}

		if ds.Backup.BackupRef.Name == "" {
			return field.ErrorList{field.Required(path.Child("backup", "backupRef", "name"), "")}
		}

	case backupv1alpha1.DataSourceTypePointInTime:
		// The credentials must come from live Instance.
		if in.Spec.UserSecretRef != nil {
			return field.ErrorList{field.Forbidden(field.NewPath("spec", "userSecretRef"),
				"must not be set when seeding from an Instance; the credentials are inherited from the source Instance",
			)}
		}
	}

	return nil
}

// validateEngine validates the engine component spec.
func validateEngine(components map[string]corev1alpha1.ComponentSpec) field.ErrorList {
	path := componentsPath.Key(common.ComponentEngine)

	engine, ok := components[common.ComponentEngine]
	if !ok {
		return field.ErrorList{field.Required(path, "")}
	}

	var errs field.ErrorList

	switch {
	case engine.Replicas == nil:
		errs = append(errs, field.Required(path.Child("replicas"), ""))
	case *engine.Replicas < 1 || *engine.Replicas%2 == 0:
		errs = append(errs, field.Invalid(path.Child("replicas"), *engine.Replicas, "must be a positive odd number"))
	}

	switch {
	case engine.Storage == nil:
		errs = append(errs, field.Required(path.Child("storage"), ""))
	case engine.Storage.Size.Cmp(minStorage) < 0:
		errs = append(errs, field.Invalid(path.Child("storage", "size"), engine.Storage.Size.String(),
			"must be at least "+minStorage.String(),
		))
	}

	errs = append(errs, validateLimits(path.Child("resources", "limits"), engine.Resources)...)
	errs = append(errs, validateService(path, engine.Service)...)

	return errs
}

// validateLimits checks the engine's CPU and memory limits against the
// operator's minimums.
func validateLimits(path *field.Path, resources *corev1.ResourceRequirements) field.ErrorList {
	if resources == nil || resources.Limits == nil {
		return field.ErrorList{field.Required(path, "")}
	}

	var errs field.ErrorList
	for _, limit := range []struct {
		name corev1.ResourceName
		min  resource.Quantity
	}{
		{corev1.ResourceCPU, minCPU},
		{corev1.ResourceMemory, minMemory},
	} {
		value, ok := resources.Limits[limit.name]
		switch {
		case !ok:
			errs = append(errs, field.Required(path.Key(string(limit.name)), "must be at least "+limit.min.String()))
		case value.Cmp(limit.min) < 0:
			errs = append(errs, field.Invalid(path.Key(string(limit.name)), value.String(),
				"must be at least "+limit.min.String(),
			))
		}
	}

	return errs
}

// validateShardedTopology checks the components of a sharded cluster. The
// topology requires configServer and proxy, so the runtime has checked they
// are present.
func validateShardedTopology(components map[string]corev1alpha1.ComponentSpec) field.ErrorList {
	path := componentsPath.Key(common.ComponentConfigServer).Child("replicas")
	cfgSrv := components[common.ComponentConfigServer]

	var errs field.ErrorList

	switch {
	case cfgSrv.Replicas == nil:
		errs = append(errs, field.Required(path, ""))
	case *cfgSrv.Replicas < 1 || *cfgSrv.Replicas%2 == 0:
		errs = append(errs, field.Invalid(path, *cfgSrv.Replicas, "must be a positive odd number"))
	case components[common.ComponentEngine].Replicas != nil &&
		*components[common.ComponentEngine].Replicas > 1 && *cfgSrv.Replicas < 3:
		// Multi-node setup requires at least 3 replicas for configServer
		errs = append(errs, field.Invalid(path, *cfgSrv.Replicas,
			"must be at least 3 when spec.components[engine].replicas is greater than 1",
		))
	}

	errs = append(errs, validateService(componentsPath.Key(common.ComponentProxy), components[common.ComponentProxy].Service)...)

	return errs
}

// validateService checks the service type a component asks for.
func validateService(path *field.Path, service *corev1alpha1.Service) field.ErrorList {
	if service == nil {
		return nil
	}

	for _, t := range supportedServiceTypes {
		if service.ServiceType == t {
			return nil
		}
	}

	return field.ErrorList{field.NotSupported(path.Child("service", "serviceType"), service.ServiceType, supportedServiceTypes)}
}

// userSecretKeys are the credential keys required for restoring from
// an external backup.
var userSecretKeys = []string{
	"MONGODB_BACKUP_USER",
	"MONGODB_BACKUP_PASSWORD",
	"MONGODB_CLUSTER_ADMIN_USER",
	"MONGODB_CLUSTER_ADMIN_PASSWORD",
	"MONGODB_CLUSTER_MONITOR_USER",
	"MONGODB_CLUSTER_MONITOR_PASSWORD",
	"MONGODB_DATABASE_ADMIN_USER",
	"MONGODB_DATABASE_ADMIN_PASSWORD",
	"MONGODB_USER_ADMIN_USER",
	"MONGODB_USER_ADMIN_PASSWORD",
}

// validateUserSecret validates the Secret referenced by spec.userSecretRef
// against the provider's secret schema, and enforces whether the field is
// required or forbidden given the configured data source.
func validateUserSecret(c *controller.Context) error {
	userSecretRef := c.Instance().Spec.UserSecretRef

	ds := c.Instance().Spec.DataSource

	if ds == nil {
		// User secret is optional for fresh instances, but if supplied it must
		// conform to the schema.
		if userSecretRef == nil {
			return nil
		}

		if _, err := validateUserSecretSchema(c, userSecretRef.Name); err != nil {
			return err
		}

		return nil
	}

	switch ds.Type {
	case backupv1alpha1.DataSourceTypeBackup:
		srcBackup := &backupv1alpha1.Backup{}
		if err := c.Get(srcBackup, ds.Backup.BackupRef.Name); err != nil {
			return fmt.Errorf("get Backup %q: %w", ds.Backup.BackupRef.Name, err)
		}

		switch srcBackup.Spec.Origin.Type {
		case backupv1alpha1.BackupOriginTypeInstance:
			// The credentials must come from live Instance.
			if userSecretRef == nil {
				return nil
			}

			return fmt.Errorf(
				"spec.userSecretRef must not be set when seeding " +
					"from an Instance; the credentials are inherited " +
					"from the source Instance",
			)

		case backupv1alpha1.BackupOriginTypeExternal:
			// Backup from a BackupStorage: user must supply userSecretRef.
			if userSecretRef == nil {
				return fmt.Errorf(
					"spec.userSecretRef is required when seeding " +
						"from an external backup; it must match the credentials " +
						"of the Instance that produced the backup",
				)
			}

			secret, err := validateUserSecretSchema(c, userSecretRef.Name)
			if err != nil {
				return err
			}

			// User secret must contain all MONGODB_* keys to be valid.
			// If any are missing, the operator generates missing keys causing
			// the authentication to fail after restore.
			for _, key := range userSecretKeys {
				if v, ok := secret.Data[key]; ok && string(v) != "" {
					continue
				}
				if v, ok := secret.StringData[key]; ok && v != "" {
					continue
				}

				return fmt.Errorf("user secret %q is missing %q", secret.Name, key)
			}

		default:
			return fmt.Errorf("unknown origin type %q", srcBackup.Spec.Origin.Type)
		}
	}

	return nil
}

// validateUserSecretSchema fetches the referenced Secret and validates it against
// the provider's secret schema.
func validateUserSecretSchema(c *controller.Context, secretName string) (*corev1.Secret, error) {
	secret := &corev1.Secret{}
	if err := c.Get(secret, secretName); err != nil {
		return nil, fmt.Errorf("get user secret %q: %w", secretName, err)
	}

	providerSpec, err := c.ProviderSpec()
	if err != nil {
		return nil, fmt.Errorf("get provider spec: %w", err)
	}

	if err := controller.ValidateSecretSchema(secret, providerSpec); err != nil {
		return nil, fmt.Errorf("user secret %q validation failed: %w", secretName, err)
	}

	return secret, nil
}
