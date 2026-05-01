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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SplitHorizonSpec defines split horizon DNS configuration for PSMDB replsets.
type SplitHorizonSpec struct {
	// BaseDomainNameSuffix is the base domain appended to generate horizon DNS entries.
	// Example: "mycompany.com"
	// +optional
	BaseDomainNameSuffix string `json:"baseDomainNameSuffix,omitempty"`

	// TLS configures TLS for split horizon connections.
	// +optional
	TLS *SplitHorizonTLS `json:"tls,omitempty"`
}

// SplitHorizonTLS defines TLS configuration for split horizon.
type SplitHorizonTLS struct {
	// SecretName is the name of the Kubernetes Secret containing TLS certificates
	// for split horizon connections.
	SecretName string `json:"secretName,omitempty"`
}

// InstanceTemplateComponentSpec defines template defaults for a single component.
// It mirrors the structure of Instance's ComponentSpec so that
// the template follows the same path format.
type InstanceTemplateComponentSpec struct {
	// Replicas specifies the default number of replicas for this component.
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// CustomSpec provides default custom configuration for this component.
	// The schema depends on the component type.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	CustomSpec *SplitHorizonSpec `json:"customSpec,omitempty"`
}

// InstanceTemplateSpec defines the desired state of InstanceTemplate.
// It mirrors the Instance spec structure so that template and instance
// follow the same path format (e.g., spec.components.splitHorizon.customSpec).
type InstanceTemplateSpec struct {
	// Components defines default component configurations.
	// Keys are component names matching those in the Instance spec
	// (e.g., "engine", "splitHorizon", "monitoring").
	// +optional
	Components map[string]InstanceTemplateComponentSpec `json:"components,omitempty"`
}

// InstanceTemplateStatus defines the observed state of InstanceTemplate.
type InstanceTemplateStatus struct {
	// InUse indicates whether this template is currently referenced by any Instance.
	// +optional
	InUse bool `json:"inUse,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=itpl

// InstanceTemplate is a provider-scoped template that defines default
// configuration for PSMDB instances. Instances reference a template
// via annotations, and the provider controller merges template defaults
// with instance-level overrides (instance fields take precedence).
type InstanceTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InstanceTemplateSpec   `json:"spec,omitempty"`
	Status InstanceTemplateStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// InstanceTemplateList contains a list of InstanceTemplate.
type InstanceTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InstanceTemplate `json:"items"`
}
