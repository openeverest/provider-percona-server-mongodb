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
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
)

// pmmAdminParams returns the pmm-admin flags that label the instance's metrics
// in PMM, so consumers can scope queries to one instance. Label names follow
// the Prometheus form of the pod labels (core_openeverest_io_instance), and
// namespace matches the label on Kubernetes metrics in PMM. The operator runs
// pmm-admin through a shell; Kubernetes names need no quoting.
func pmmAdminParams(c *controller.Context, component string) string {
	labels := []string{
		"namespace=" + c.Namespace(),
		prometheusLabelName(controller.InstanceLabel) + "=" + c.Name(),
	}
	if component != "" {
		labels = append(labels, prometheusLabelName(controller.ComponentLabel)+"="+component)
	}
	return "--custom-labels=" + strings.Join(labels, ",")
}

var invalidPrometheusLabelChars = regexp.MustCompile(`[^a-zA-Z0-9_]`)

// prometheusLabelName converts a Kubernetes label key to a valid Prometheus
// label name the way Prometheus does for Kubernetes service discovery.
func prometheusLabelName(key string) string {
	return invalidPrometheusLabelChars.ReplaceAllString(key, "_")
}

// copySecretData copies the value of a source key from the secret
// and applies it to the destination secret with the destination key.
// A missing source secret or key is a renderFailure.
func copySecretData(c *controller.Context, source, dest, sourceKey, destKey string) error {
	sourceSecret := &corev1.Secret{}
	if err := c.Get(sourceSecret, source); apierrors.IsNotFound(err) {
		return renderFailedf("secret %s not found", source)
	} else if err != nil {
		return fmt.Errorf("failed to get secret %s: %w", source, err)
	}

	apiKey, ok := sourceSecret.Data[sourceKey]
	if !ok {
		return renderFailedf("failed to get key %s from secret %s", sourceKey, source)
	}

	// Server-side apply owns only destKey; the operator's user keys are left alone.
	return c.Apply(&corev1.Secret{
		ObjectMeta: c.ObjectMeta(dest),
		Data:       map[string][]byte{destKey: apiKey},
	})
}

// removeSecretData removes the key copySecretData applied to the secret by
// applying the secret without it, which leaves every other key alone.
func removeSecretData(c *controller.Context, name, key string) error {
	secret := &corev1.Secret{}
	if err := c.Get(secret, name); apierrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to get secret %s: %w", name, err)
	}
	if _, ok := secret.Data[key]; !ok {
		return nil
	}
	return c.Apply(&corev1.Secret{ObjectMeta: c.ObjectMeta(name)})
}
