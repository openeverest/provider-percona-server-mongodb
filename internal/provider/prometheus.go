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
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// configurePodMonitor sets up a PodMonitor for the given instance.
func configurePodMonitor(c *controller.Context) error {
	podMonitor := &monitoringv1.PodMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      c.Name() + "-monitor",
			Namespace: c.Namespace(),
			Labels: map[string]string{
				"provider":   "provider-percona-server-mongodb",
				"managed-by": "openeverest",
			},
		},
		Spec: monitoringv1.PodMonitorSpec{
			Selector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": c.Name(),
				},
			},
			PodMetricsEndpoints: []monitoringv1.PodMetricsEndpoint{
				{
					Port: pointer.ToString("9216"),
					Path: "/metrics",
				},
			},
		},
	}

	if err := c.Apply(podMonitor); err != nil {
		return fmt.Errorf("failed to apply PodMonitor %w:", err)
	}

	if err := controllerutil.SetControllerReference(c.Instance(), podMonitor, c.Client().Scheme()); err != nil {
		return fmt.Errorf("failed to set controller reference for PodMonitor %w:", err)
	}

	return nil
}

// configurePrometheus sets up a Prometheus instance.
//
// TODO: This should be setup globally for the cluster.
func configurePrometheus(c *controller.Context) error {
	prometheus := &monitoringv1.Prometheus{
		ObjectMeta: metav1.ObjectMeta{
			Name: "prometheus",
		},
		Spec: monitoringv1.PrometheusSpec{
			CommonPrometheusFields: monitoringv1.CommonPrometheusFields{
				PodMonitorSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"provider": "provider-percona-server-mongodb",
					},
				},
			},
		},
	}

	if err := c.Apply(prometheus); err != nil {
		return fmt.Errorf("failed to apply Prometheus %w:", err)
	}

	return nil
}
