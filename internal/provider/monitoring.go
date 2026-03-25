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
	monitoringv1alpha1 "github.com/openeverest/openeverest/v2/api/monitoring/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mongodb/definition/components"
	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
)

const (
	// monitoringConfigPath is the path to the monitoring config name in the Instance.
	monitoringConfigPath = ".spec.components.monitoring.monitoringConfigName"

	// credentialsSecretPath is the path to the secret name in the MonitoringConfig.
	credentialsSecretPath = ".spec.credentialsSecretName"
)

// configureMonitoring creates the PMM spec configuration for PSMDB.
// It updates user secrets with PMM token if monitoring is enabled.
// Returns nil if no update is needed.
func configureMonitoring(c *controller.Context, psmdb *psmdbv1.PerconaServerMongoDBSpec, usersSecretName string) (*psmdbv1.PMMSpec, error) {
	spec, err := c.ProviderSpec()
	if err != nil {
		return nil, fmt.Errorf("failed to get provider spec: %w", err)
	}

	monitoring, ok := c.Instance().Spec.Components[common.ComponentMonitoring]
	if !ok {
		// do not update if monitoring component is not specified
		return nil, nil
	}

	var customSpec components.PMMCustomSpec
	if err := c.DecodeComponentCustomSpec(monitoring, &customSpec); err != nil {
		return nil, fmt.Errorf("failed to decode monitoring component custom spec: %w", err)
	}

	// do not update if monitoring config name is not specified
	if customSpec.MonitoringConfigName == nil {
		return nil, nil
	}

	// if monitoring config name key is present but empty, disable monitoring
	if *customSpec.MonitoringConfigName == "" {
		return &psmdbv1.PMMSpec{
			Enabled: false,
		}, nil
	}

	var mc = &monitoringv1alpha1.MonitoringConfig{}
	if err := c.Get(mc, *customSpec.MonitoringConfigName); err != nil {
		return nil, fmt.Errorf("failed to get monitoring config: %w", err)
	}

	u, err := url.Parse(mc.Spec.PMM.URL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse PMM URL %s: %w", mc.Spec.PMM.URL, err)
	}

	if err := copySecretData(c, mc.Spec.CredentialsSecretName, usersSecretName, "apiKey", "PMM_SERVER_TOKEN"); err != nil {
		return nil, fmt.Errorf("failed to apply PMM token to user secrets: %w", err)
	}

	pmmImage := controller.GetDefaultImageForComponent(spec, common.ComponentMonitoring)

	return &psmdbv1.PMMSpec{
		Enabled:    true,
		ServerHost: u.Host,
		Image:      pmmImage,
		Resources:  getPMMResources(c, psmdb),
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

// validateMonitoring validates the monitoring image version is compatible
// with the PMM server version.
func validateMonitoring(c *controller.Context) error {
	monitoring, ok := c.Instance().Spec.Components[common.ComponentMonitoring]
	if !ok {
		// monitoring is not enabled, nothing to do
		return nil
	}

	var customSpec components.PMMCustomSpec
	if err := c.DecodeComponentCustomSpec(monitoring, &customSpec); err != nil {
		return fmt.Errorf("failed to decode monitoring component custom spec: %w", err)
	}

	if customSpec.MonitoringConfigName == nil || *customSpec.MonitoringConfigName == "" {
		return nil
	}

	var mc = &monitoringv1alpha1.MonitoringConfig{}
	if err := c.Get(mc, *customSpec.MonitoringConfigName); err != nil {
		return fmt.Errorf("failed to get monitoring config %s: %w", *customSpec.MonitoringConfigName, err)
	}

	if mc.Status.PMMServerVersion == "" {
		// PMM is not running or server version is not reported yet.
		// Do we prevent using monitoring image with unknown compatibility?
		return nil
	}

	serverVersion, err := goversion.NewVersion(string(mc.Status.PMMServerVersion))
	if err != nil {
		return fmt.Errorf("failed to parse PMM server version: %w", err)
	}

	clientVersion, err := goversion.NewVersion(monitoring.Version)
	if err != nil {
		return fmt.Errorf("failed to parse monitoring image version: %w", err)
	}

	serverMajor := serverVersion.Segments()[0]
	clientMajor := clientVersion.Segments()[0]

	// https://docs.percona.com/percona-operator-for-mongodb/monitoring-tutorial.html#considerations
	if clientMajor != serverMajor {
		return fmt.Errorf("monitoring image version %s is not compatible with PMM server version %s", monitoring.Version, mc.Status.PMMServerVersion)
	}

	return nil
}

// enqueueMonitoringConfig returns a function that enqueues reconcile requests
// for Instances referencing the given MonitoringConfig.
func enqueueMonitoringConfig(p *PSMDBProvider) func(ctx context.Context, obj client.Object) []reconcile.Request {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		mc, ok := obj.(*monitoringv1alpha1.MonitoringConfig)
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

		mcList := &monitoringv1alpha1.MonitoringConfigList{}
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
	mc, ok := obj.(*monitoringv1alpha1.MonitoringConfig)
	if !ok {
		return nil
	}

	if mc.Spec.CredentialsSecretName == "" {
		return nil
	}

	return []string{mc.Spec.CredentialsSecretName}
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
