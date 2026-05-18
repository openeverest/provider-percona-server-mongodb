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
	"net/url"

	goversion "github.com/hashicorp/go-version"
	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	monitoringv1alpha2 "github.com/openeverest/openeverest/v2/api/monitoring/v1alpha2"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mongodb/definition/components"
	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
)

const (
	// monitoringConfigPath is the field index path for looking up Instances
	// by their referenced MonitoringConfig name.
	monitoringConfigPath = ".spec.components.monitoring.monitoringConfigName"

	// credentialsSecretPath is the field index path for looking up MonitoringConfigs
	// by their referenced credentials Secret name.
	credentialsSecretPath = ".spec.pmm.credentialsSecretName"
)

// resolveMonitoringConfig looks up the MonitoringConfig referenced by the
// instance's monitoring component custom spec. It returns nil without error when
// the monitoring component is absent.
func resolveMonitoringConfig(c *controller.Context) (*monitoringv1alpha2.MonitoringConfig, error) {
	monitoring, ok := c.Instance().Spec.Components[common.ComponentMonitoring]
	if !ok {
		return nil, nil
	}

	var customSpec components.PMMCustomSpec
	if err := c.DecodeComponentCustomSpec(monitoring, &customSpec); err != nil {
		return nil, fmt.Errorf("decode monitoring custom spec: %w", err)
	}

	if customSpec.MonitoringConfigName == nil || *customSpec.MonitoringConfigName == "" {
		return nil, fmt.Errorf("monitoringConfigName is required when monitoring component is set")
	}

	mc := &monitoringv1alpha2.MonitoringConfig{}
	if err := c.Get(mc, *customSpec.MonitoringConfigName); err != nil {
		return nil, fmt.Errorf("get MonitoringConfig %q: %w", *customSpec.MonitoringConfigName, err)
	}

	return mc, nil
}

// configureMonitoring builds the PMMSpec for the PSMDB resource based on the
// instance's monitoring component configuration. The reconciliation handles:
//
//  1. Monitoring not configured (component absent) returns disabled PMMSpec.
//  2. Monitoring enabled: resolves the MonitoringConfig, copies the PMM API key
//     to the users secret, and returns a configured PMMSpec with resource
//     requirements calculated from the engine and requested resources.
//     Resources are never preserved from a previous PMM configuration.
func configureMonitoring(
	c *controller.Context,
	usersSecretName string,
) (*psmdbv1.PMMSpec, error) {
	// Resolve the referenced MonitoringConfig. Returns nil if monitoring
	// is not configured (component absent or monitoringConfigName not set).
	mc, err := resolveMonitoringConfig(c)
	if err != nil {
		return nil, fmt.Errorf("resolve monitoring config: %w", err)
	}

	if mc == nil {
		return &psmdbv1.PMMSpec{Enabled: false}, nil
	}

	spec, err := c.ProviderSpec()
	if err != nil {
		return nil, fmt.Errorf("get provider spec: %w", err)
	}

	u, err := url.Parse(mc.Spec.PMM.URL)
	if err != nil {
		return nil, fmt.Errorf("parse PMM URL %q: %w", mc.Spec.PMM.URL, err)
	}

	// Copy the PMM API key from the MonitoringConfig credentials secret
	// to the PSMDB users secret so the PMM sidecar can authenticate.
	if err := copySecretData(c, mc.Spec.PMM.CredentialsSecretName, usersSecretName, "apiKey", "PMM_SERVER_TOKEN"); err != nil {
		return nil, fmt.Errorf("copy PMM API key to users secret: %w", err)
	}

	pmmImage := controller.GetDefaultImageForComponent(spec, common.ComponentMonitoring)

	monitoring := c.Instance().Spec.Components[common.ComponentMonitoring]
	if monitoring.Version != "" {
		pmmImage = controller.GetImageForVersion(spec, common.ComponentMonitoring, monitoring.Version)
	}

	return &psmdbv1.PMMSpec{
		Enabled:    true,
		ServerHost: u.Host,
		Image:      pmmImage,
		Resources:  getPMMResources(c),
	}, nil
}

// copySecretData copies the value of a source key from the secret
// and applies it to the destination secret with the destination key.
func copySecretData(c *controller.Context, source, dest, sourceKey, destKey string) error {
	sourceSecret := &corev1.Secret{}
	if err := c.Get(sourceSecret, source); err != nil {
		return fmt.Errorf("failed to get secret %s: %w", source, err)
	}

	destSecret := &corev1.Secret{}
	if err := c.Get(destSecret, dest); err != nil {
		// If the secret doesn't exist, create it
		destSecret = &corev1.Secret{ObjectMeta: c.ObjectMeta(dest)}
	}

	apiKey, ok := sourceSecret.Data[sourceKey]
	if !ok {
		return fmt.Errorf("failed to get key %s from secret %s", sourceKey, source)
	}

	if destSecret.Data == nil {
		destSecret.Data = make(map[string][]byte)
	}

	destSecret.Data[destKey] = apiKey

	return c.Apply(destSecret)
}

// validateMonitoring validates that an explicitly set PMM client version exists
// in the provider spec and is compatible with the PMM server's major version.
// See: https://docs.percona.com/percona-operator-for-mongodb/monitoring-tutorial.html#considerations
func validateMonitoring(c *controller.Context) error {
	mc, err := resolveMonitoringConfig(c)
	if err != nil {
		return fmt.Errorf("resolve monitoring config: %w", err)
	}

	if mc == nil {
		return nil
	}

	if mc.Status.PMM == nil || mc.Status.PMM.ServerVersion == "" {
		// PMM server version not yet reported; skip compatibility check.
		return nil
	}

	monitoring := c.Instance().Spec.Components[common.ComponentMonitoring]

	// version is not configured, nothing to validate
	if monitoring.Version == "" {
		return nil
	}

	spec, err := c.ProviderSpec()
	if err != nil {
		return fmt.Errorf("get provider spec: %w", err)
	}

	if controller.GetImageForVersion(spec, common.ComponentMonitoring, monitoring.Version) == "" {
		return fmt.Errorf("monitoring version %q not found in provider spec", monitoring.Version)
	}

	serverVersion, err := goversion.NewVersion(string(mc.Status.PMM.ServerVersion))
	if err != nil {
		return fmt.Errorf("parse PMM server version %q: %w", mc.Status.PMM.ServerVersion, err)
	}

	clientVersion, err := goversion.NewVersion(monitoring.Version)
	if err != nil {
		return fmt.Errorf("parse monitoring image version %q: %w", monitoring.Version, err)
	}

	if clientVersion.Segments()[0] != serverVersion.Segments()[0] {
		return fmt.Errorf(
			"PMM client version %s is incompatible with server version %s: major versions must match",
			monitoring.Version, mc.Status.PMM.ServerVersion,
		)
	}

	return nil
}

// enqueueMonitoringConfig returns a function that enqueues reconcile requests
// for Instances referencing the given MonitoringConfig.
func enqueueMonitoringConfig(p *PSMDBProvider) func(ctx context.Context, obj client.Object) []reconcile.Request {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		mc, ok := obj.(*monitoringv1alpha2.MonitoringConfig)
		if !ok {
			return []reconcile.Request{}
		}

		c := p.client
		if c == nil {
			return []reconcile.Request{}
		}

		instanceList := &corev1alpha1.InstanceList{}
		listOpts := &client.ListOptions{
			FieldSelector: fields.OneTermEqualSelector(monitoringConfigPath, mc.GetName()),
			Namespace:     mc.GetNamespace(),
		}

		if err := c.List(ctx, instanceList, listOpts); err != nil {
			return []reconcile.Request{}
		}

		requests := make([]reconcile.Request, len(instanceList.Items))
		for i, item := range instanceList.Items {
			requests[i] = reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      item.GetName(),
					Namespace: item.GetNamespace(),
				},
			}
		}

		return requests
	}
}

// enqueueMonitoringConfigSecret returns a function that enqueues reconcile requests
// for Instances referencing the given Secret via their MonitoringConfig.
func enqueueMonitoringConfigSecret(p *PSMDBProvider) func(ctx context.Context, obj client.Object) []reconcile.Request {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		secret, ok := obj.(*corev1.Secret)
		if !ok {
			return []reconcile.Request{}
		}

		c := p.client
		if c == nil {
			return []reconcile.Request{}
		}

		mcList := &monitoringv1alpha2.MonitoringConfigList{}
		mcOpts := &client.ListOptions{
			FieldSelector: fields.OneTermEqualSelector(credentialsSecretPath, secret.GetName()),
			Namespace:     secret.GetNamespace(),
		}

		if err := c.List(ctx, mcList, mcOpts); err != nil {
			return []reconcile.Request{}
		}

		if len(mcList.Items) == 0 {
			// no MonitoringConfig references this secret, nothing to do
			return []reconcile.Request{}
		}

		mc := mcList.Items[0]

		instanceList := &corev1alpha1.InstanceList{}
		instanceOpts := &client.ListOptions{
			FieldSelector: fields.OneTermEqualSelector(monitoringConfigPath, mc.GetName()),
			Namespace:     mc.GetNamespace(),
		}

		if err := c.List(ctx, instanceList, instanceOpts); err != nil {
			return []reconcile.Request{}
		}

		requests := make([]reconcile.Request, len(instanceList.Items))
		for i, item := range instanceList.Items {
			requests[i] = reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      item.GetName(),
					Namespace: item.GetNamespace(),
				},
			}
		}

		return requests
	}
}

// extractMonitoringConfigName extracts the MonitoringConfig name
// from the given object if it is an Instance referencing a MonitoringConfig,
// otherwise returns nil.
func extractMonitoringConfigName(obj client.Object) []string {
	in, ok := obj.(*corev1alpha1.Instance)
	if !ok {
		return nil
	}

	monitoring, ok := in.Spec.Components[common.ComponentMonitoring]
	if !ok {
		return nil
	}

	if monitoring.CustomSpec == nil || monitoring.CustomSpec.Raw == nil {
		return nil
	}

	var customSpec components.PMMCustomSpec
	if err := json.Unmarshal(monitoring.CustomSpec.Raw, &customSpec); err != nil {
		return nil
	}

	if customSpec.MonitoringConfigName == nil || *customSpec.MonitoringConfigName == "" {
		return nil
	}

	return []string{*customSpec.MonitoringConfigName}
}

// extractMonitoringConfigSecretName extracts the credentials secret name
// from the given object if it is a MonitoringConfig referencing a secret,
// otherwise returns nil.
func extractMonitoringConfigSecretName(obj client.Object) []string {
	mc, ok := obj.(*monitoringv1alpha2.MonitoringConfig)
	if !ok {
		return nil
	}

	if mc.Spec.PMM == nil || mc.Spec.PMM.CredentialsSecretName == "" {
		return nil
	}

	return []string{mc.Spec.PMM.CredentialsSecretName}
}

// monitoringConfigPredicate returns a predicate that filters events
// for MonitoringConfig resources.
func monitoringConfigPredicate() predicate.Funcs {
	return predicate.Funcs{
		// Nothing to process on create events
		CreateFunc: func(_ event.CreateEvent) bool {
			return false
		},

		// Allow update events.
		UpdateFunc: func(_ event.UpdateEvent) bool {
			return true
		},

		// Nothing to process on delete events
		DeleteFunc: func(_ event.DeleteEvent) bool {
			return false
		},

		// Nothing to process on generic events
		GenericFunc: func(_ event.GenericEvent) bool {
			return false
		},
	}
}
