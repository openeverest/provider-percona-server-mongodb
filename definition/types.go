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

// Package definition contains shared types used across the provider definition.
//
// +k8s:openapi-gen=true
package definition

// TopologyType defines the type of deployment topology.
type TopologyType string

const (
	// TopologyTypeReplicaSet represents a replica set topology.
	TopologyTypeReplicaSet TopologyType = "replicaSet"
	// TopologyTypeSharded represents a sharded cluster topology.
	TopologyTypeSharded TopologyType = "sharded"
)

// Parameters defines instance-wide structured parameters that apply to the
// entire cluster (Instance.spec.parameters).
type Parameters struct {
	// TLS configures encryption in transit, between clients and the instance
	// and between its members.
	// +optional
	TLS *TLSParameters `json:"tls,omitempty"`
}

// TLSMode controls which connections must use TLS.
// +enum
type TLSMode string

const (
	// TLSModePrefer encrypts traffic between members and accepts clients with
	// or without TLS. The default.
	TLSModePrefer TLSMode = "preferTLS"
	// TLSModeRequire encrypts all traffic and rejects clients without TLS.
	TLSModeRequire TLSMode = "requireTLS"
	// TLSModeDisabled turns TLS off. It can only be chosen when the instance
	// is created.
	TLSModeDisabled TLSMode = "disabled"
)

// TLSParameters configures TLS for the instance. The certificates come from,
// in order of precedence: the certificate secrets, the cert-manager issuer,
// or else are generated (through cert-manager when it is installed).
// Certificates and issuer can only be set when the instance is created.
type TLSParameters struct {
	// Mode controls which connections must use TLS.
	// +default="preferTLS"
	// +optional
	Mode TLSMode `json:"mode,omitempty"`

	// CertificateSecretName names a Secret in the instance namespace with
	// the certificate clients connect to: tls.crt, tls.key and ca.crt. Its
	// subject alternative names must cover the instance's service and pod
	// names.
	// +optional
	CertificateSecretName string `json:"certificateSecretName,omitempty"`

	// InternalCertificateSecretName names a Secret in the instance
	// namespace with the certificate members authenticate each other with,
	// in the same format. Defaults to the certificate secret.
	// +optional
	InternalCertificateSecretName string `json:"internalCertificateSecretName,omitempty"`

	// Issuer is the cert-manager issuer that signs the instance's
	// certificates, instead of a self-signed CA created for the instance.
	// +optional
	Issuer *IssuerReference `json:"issuer,omitempty"`
}

// IssuerKind is the kind of a cert-manager issuer.
// +enum
type IssuerKind string

const (
	// IssuerKindIssuer is an Issuer in the instance namespace.
	IssuerKindIssuer IssuerKind = "Issuer"
	// IssuerKindClusterIssuer is a cluster-wide ClusterIssuer.
	IssuerKindClusterIssuer IssuerKind = "ClusterIssuer"
)

// IssuerReference refers to a cert-manager issuer.
type IssuerReference struct {
	// Name of the issuer.
	// +optional
	Name string `json:"name,omitempty"`

	// Kind of the issuer.
	// +default="Issuer"
	// +optional
	Kind IssuerKind `json:"kind,omitempty"`
}
