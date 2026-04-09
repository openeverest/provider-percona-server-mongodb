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

	"github.com/AlekSi/pointer"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	"github.com/openeverest/provider-percona-server-mongodb/definition/components"
	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// configureExporter returns a sidecar container configuration for MongoDB Exporter.
// It exposes metrics on localhost:9216/metrics and creates a Prometheus PodMonitor
// to scrape metrics.
// It returns the container configuration if exporter is enabled or nil if disabled.
func configureExporter(c *controller.Context, secretName string) (*corev1.Container, error) {
	exporter, ok := c.Instance().Spec.Components[common.ComponentMetricsExporter]
	if !ok {
		return nil, nil
	}

	if exporter.CustomSpec == nil || exporter.CustomSpec.Raw == nil {
		return nil, nil
	}

	var customSpec components.ExporterCustomSpec
	if err := c.DecodeComponentCustomSpec(exporter, &customSpec); err != nil {
		return nil, fmt.Errorf("failed to decode custom spec: %w", err)
	}

	if !customSpec.Enabled {
		return nil, nil
	}

	spec, err := c.ProviderSpec()
	if err != nil {
		return nil, err
	}

	container := &corev1.Container{
		Name:  "exporter",
		Image: controller.GetDefaultImageForComponent(spec, common.ComponentMetricsExporter),
		Args:  []string{"--discovering-mode", "--compatible-mode", "--collect-all", "--mongodb.uri=$(MONGODB_URI)"},
		// Use environment variables to pass MongoDB credentials and connection info to the exporter container.
		// https://github.com/percona/mongodb_exporter?tab=readme-ov-file#mongodb-authentication
		Ports: []corev1.ContainerPort{
			{
				Name:          "metrics",
				ContainerPort: 9216,
				Protocol:      corev1.ProtocolTCP,
			},
		},
		Env: []corev1.EnvVar{
			{
				Name: "MONGODB_USER",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: secretName,
						},
						Key: "MONGODB_CLUSTER_MONITOR_USER",
					},
				},
			},
			{
				Name: "MONGODB_PASSWORD",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: secretName,
						},
						Key: "MONGODB_CLUSTER_MONITOR_PASSWORD",
					},
				},
			},
			{
				Name: "POD_NAME",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						APIVersion: "v1",
						FieldPath:  "metadata.name",
					},
				},
			},
			{
				Name:  "MONGODB_URI",
				Value: "mongodb://$(POD_NAME)",
			},
		},
	}

	if err := configurePodMonitor(c); err != nil {
		return nil, fmt.Errorf("failed to configure pod monitor: %w", err)
	}

	return container, nil
}

// configurePodMonitor sets up a PodMonitor for the given instance.
func configurePodMonitor(c *controller.Context) error {
	podMonitor := &monitoringv1.PodMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      c.Name() + "-monitor",
			Namespace: c.Namespace(),
			Labels: map[string]string{
				"provider":   "percona-server-mongodb",
				"managed-by": "openeverest",
				"release":    "prometheus", // required for the prometheus-operator to discover this PodMonitor
			},
		},
		Spec: monitoringv1.PodMonitorSpec{
			Selector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app.kubernetes.io/instance": c.Name(),
				},
			},
			PodMetricsEndpoints: []monitoringv1.PodMetricsEndpoint{
				{
					Port: pointer.ToString("metrics"),
					Path: "/metrics",
				},
			},
		},
	}

	if err := c.Apply(podMonitor); err != nil {
		return fmt.Errorf("failed to apply PodMonitor: %w", err)
	}

	if err := controllerutil.SetControllerReference(c.Instance(), podMonitor, c.Client().Scheme()); err != nil {
		return fmt.Errorf("failed to set controller reference for PodMonitor: %w", err)
	}

	return nil
}
