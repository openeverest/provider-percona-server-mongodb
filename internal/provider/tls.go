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
	"errors"
	"fmt"

	cmapi "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/yaml"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mongodb/definition"
)

const (
	certManagerGroup = "cert-manager.io"

	// caKey is the key holding the CA certificate, in the certificate
	// secrets and in the connection secret.
	caKey = "ca.crt"

	// clientCertificateOptionalToken approves the rolling restart that lets
	// an existing instance accept TLS clients without a client certificate.
	clientCertificateOptionalToken = "tls-clients-without-certificates"
)

// certificateSecretKeys are the keys a certificate secret must hold.
var certificateSecretKeys = []string{"tls.crt", "tls.key", caKey}

// tlsParameters returns the instance's TLS parameters with defaults applied.
func tlsParameters(c *controller.Context) (definition.TLSParameters, error) {
	params := definition.Parameters{}
	if raw := c.Instance().Spec.Parameters; raw != nil && len(raw.Raw) > 0 {
		if err := c.DecodeParameters(&params); err != nil {
			return definition.TLSParameters{}, fmt.Errorf("decode parameters: %w", err)
		}
	}

	tls := definition.TLSParameters{}
	if params.TLS != nil {
		tls = *params.TLS
	}
	if tls.Mode == "" {
		tls.Mode = definition.TLSModePrefer
	}
	if tls.Issuer != nil && tls.Issuer.Name == "" {
		tls.Issuer = nil
	}
	if tls.Issuer != nil && tls.Issuer.Kind == "" {
		tls.Issuer.Kind = definition.IssuerKindIssuer
	}

	return tls, nil
}

func tlsEnabled(mode definition.TLSMode) bool {
	return mode != definition.TLSModeDisabled
}

// clientCertificateSecretName is the secret holding the certificate clients
// connect to; the operator generates it under its default name unless the
// instance brings its own.
func clientCertificateSecretName(c *controller.Context, tls definition.TLSParameters) string {
	if tls.CertificateSecretName != "" {
		return tls.CertificateSecretName
	}
	return c.Name() + "-ssl"
}

// configureTLS sets the TLS mode and the certificate source on the PSMDB
// resource; the certificate secrets are set with the other secrets.
// keepCertificates stops the operator from replacing the instance's
// certificates.
func configureTLS(psmdb *psmdbv1.PerconaServerMongoDB, tls definition.TLSParameters, keepCertificates bool) {
	spec := &psmdbv1.TLSSpec{Mode: psmdbv1.TLSMode(tls.Mode)}

	if tls.Issuer != nil {
		spec.IssuerConf = cmmeta.IssuerReference{
			Name:  tls.Issuer.Name,
			Kind:  string(tls.Issuer.Kind),
			Group: certManagerGroup,
		}
	}

	if tls.CertificateSecretName != "" || keepCertificates {
		spec.CertManagementPolicy = psmdbv1.CertManagementUserProvidedOnly
	}

	psmdb.Spec.TLS = spec
}

// keepCertificates reports whether the operator must leave the instance's
// certificates alone. The user's own certificates are theirs: if their
// secret goes missing, a new CA would cut off every client that trusts
// theirs. Certificates the operator generated without cert-manager must stay
// too: once cert-manager is installed, the operator 1.23 migrates them to it
// and never finishes, restarting the pods every couple of minutes while it
// and cert-manager overwrite each other's secret.
func keepCertificates(c *controller.Context, tls definition.TLSParameters) (bool, error) {
	if tls.CertificateSecretName != "" {
		return true, nil
	}
	if !tlsEnabled(tls.Mode) || tls.Issuer != nil {
		return false, nil
	}

	secret := &corev1.Secret{}
	if err := c.Get(secret, clientCertificateSecretName(c, tls)); err != nil {
		if apierrors.IsNotFound(err) {
			// Not generated yet; cert-manager issues them if installed.
			return false, nil
		}
		return false, fmt.Errorf("get certificate secret: %w", err)
	}
	_, issuedByCertManager := secret.Annotations[cmapi.CertificateNameKey]
	return !issuedByCertManager, nil
}

// acceptClientsWithoutCertificates lets TLS clients connect with a server
// certificate check and password authentication alone, as with managed
// MongoDB services. mongod otherwise demands a client certificate signed by
// the instance CA from every TLS client. Members still authenticate each
// other with x509. On an existing instance this changes the mongod
// configuration, so it waits for maintenance approval.
func acceptClientsWithoutCertificates(c *controller.Context, psmdb *psmdbv1.PerconaServerMongoDB, tls definition.TLSParameters) error {
	if !tlsEnabled(tls.Mode) {
		return nil
	}

	current := &psmdbv1.PerconaServerMongoDB{}
	if err := c.Get(current, c.Name()); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("fetching current PSMDB CR: %w", err)
		}
		current = nil
	}

	if current != nil && !acceptsClientsWithoutCertificates(current) &&
		!c.RequestMaintenance(clientCertificateOptionalToken,
			"Accept TLS clients without a client certificate (causes a brief rolling restart)",
			controller.MaintenanceRollingRestart) {
		return nil
	}

	for _, rs := range psmdb.Spec.Replsets {
		conf, err := withClientCertificatesOptional(rs.Configuration)
		if err != nil {
			return fmt.Errorf("replset %s configuration: %w", rs.Name, err)
		}
		rs.Configuration = conf
	}

	if !psmdb.Spec.Sharding.Enabled {
		return nil
	}

	cfg := psmdb.Spec.Sharding.ConfigsvrReplSet
	conf, err := withClientCertificatesOptional(cfg.Configuration)
	if err != nil {
		return fmt.Errorf("config server configuration: %w", err)
	}
	cfg.Configuration = conf

	mongos := psmdb.Spec.Sharding.Mongos
	conf, err = withClientCertificatesOptional(mongos.Configuration)
	if err != nil {
		return fmt.Errorf("mongos configuration: %w", err)
	}
	mongos.Configuration = conf

	return nil
}

// acceptsClientsWithoutCertificates reports whether the PSMDB resource
// already lets TLS clients connect without a certificate.
func acceptsClientsWithoutCertificates(psmdb *psmdbv1.PerconaServerMongoDB) bool {
	if len(psmdb.Spec.Replsets) == 0 {
		return false
	}
	conf := map[string]any{}
	if err := yaml.Unmarshal([]byte(psmdb.Spec.Replsets[0].Configuration), &conf); err != nil {
		return false
	}
	net, _ := conf["net"].(map[string]any)
	tls, _ := net["tls"].(map[string]any)
	_, set := tls["allowConnectionsWithoutCertificates"]
	return set
}

// withClientCertificatesOptional sets
// net.tls.allowConnectionsWithoutCertificates in a mongod or mongos
// configuration, keeping a value the user set explicitly. A configuration
// that isn't a YAML mapping is passed through for the operator to reject.
func withClientCertificatesOptional(configuration psmdbv1.MongoConfiguration) (psmdbv1.MongoConfiguration, error) {
	conf := map[string]any{}
	if err := yaml.Unmarshal([]byte(configuration), &conf); err != nil {
		return configuration, nil //nolint:nilerr // see above
	}

	net, ok := conf["net"].(map[string]any)
	if !ok {
		if _, set := conf["net"]; set {
			return configuration, nil
		}
		net = map[string]any{}
		conf["net"] = net
	}
	tls, ok := net["tls"].(map[string]any)
	if !ok {
		if _, set := net["tls"]; set {
			return configuration, nil
		}
		tls = map[string]any{}
		net["tls"] = tls
	}
	if _, set := tls["allowConnectionsWithoutCertificates"]; set {
		return configuration, nil
	}
	tls["allowConnectionsWithoutCertificates"] = true

	out, err := yaml.Marshal(conf)
	if err != nil {
		return "", fmt.Errorf("render configuration: %w", err)
	}
	return psmdbv1.MongoConfiguration(out), nil
}

// validateTLS validates the TLS parameters, and that they keep what can't
// change on a running instance.
func validateTLS(c *controller.Context) error {
	tls, err := tlsParameters(c)
	if err != nil {
		return err
	}

	switch tls.Mode {
	case definition.TLSModePrefer, definition.TLSModeRequire, definition.TLSModeDisabled:
	default:
		return fmt.Errorf("mode must be one of %s, %s or %s, got %q",
			definition.TLSModePrefer, definition.TLSModeRequire, definition.TLSModeDisabled, tls.Mode)
	}

	if tls.Issuer != nil {
		switch tls.Issuer.Kind {
		case definition.IssuerKindIssuer, definition.IssuerKindClusterIssuer:
		default:
			return fmt.Errorf("issuer.kind must be %s or %s, got %q",
				definition.IssuerKindIssuer, definition.IssuerKindClusterIssuer, tls.Issuer.Kind)
		}
	}

	customCertificates := tls.CertificateSecretName != "" || tls.InternalCertificateSecretName != ""
	switch {
	case !tlsEnabled(tls.Mode) && (customCertificates || tls.Issuer != nil):
		return errors.New("certificates and issuer apply only with TLS enabled")
	case tls.InternalCertificateSecretName != "" && tls.CertificateSecretName == "":
		return errors.New("internalCertificateSecretName requires certificateSecretName")
	case tls.CertificateSecretName != "" && tls.Issuer != nil:
		return errors.New("use either certificate secrets or an issuer, not both")
	}

	for _, name := range []string{tls.CertificateSecretName, tls.InternalCertificateSecretName} {
		if name == "" {
			continue
		}
		if err := validateCertificateSecret(c, name); err != nil {
			return err
		}
	}

	return validateTLSChange(c, tls)
}

func validateCertificateSecret(c *controller.Context, name string) error {
	secret := &corev1.Secret{}
	if err := c.Get(secret, name); err != nil {
		return fmt.Errorf("get certificate secret %q: %w", name, err)
	}
	for _, key := range certificateSecretKeys {
		if len(secret.Data[key]) == 0 {
			return fmt.Errorf("certificate secret %q is missing %q", name, key)
		}
	}
	return nil
}

// validateTLSChange rejects changes the operator can't apply to a running
// instance. Turning TLS on or off switches how members authenticate each
// other (x509 or keyfile); a rolling restart would leave the members unable
// to talk to each other halfway. A new certificate source brings a new CA,
// which members still on the old one wouldn't trust.
func validateTLSChange(c *controller.Context, tls definition.TLSParameters) error {
	current := &psmdbv1.PerconaServerMongoDB{}
	if err := c.Get(current, c.Name()); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("fetching current PSMDB CR: %w", err)
	}

	currentMode := psmdbv1.TLSModePrefer
	if current.Spec.TLS != nil && current.Spec.TLS.Mode != "" {
		currentMode = current.Spec.TLS.Mode
	}
	if (currentMode != psmdbv1.TLSModeDisabled) != tlsEnabled(tls.Mode) {
		return errors.New("TLS can't be turned on or off on an existing instance")
	}

	currentIssuer, currentSecret, currentInternalSecret := "/", "", ""
	if current.Spec.TLS != nil {
		currentIssuer = current.Spec.TLS.IssuerConf.Kind + "/" + current.Spec.TLS.IssuerConf.Name
	}
	if current.Spec.Secrets != nil {
		currentInternalSecret = current.Spec.Secrets.SSLInternal
		if current.Spec.TLS != nil && current.Spec.TLS.CertManagementPolicy == psmdbv1.CertManagementUserProvidedOnly {
			currentSecret = current.Spec.Secrets.SSL
		}
	}
	wantIssuer := "/"
	if tls.Issuer != nil {
		wantIssuer = string(tls.Issuer.Kind) + "/" + tls.Issuer.Name
	}
	if currentIssuer != wantIssuer || currentSecret != tls.CertificateSecretName ||
		(currentInternalSecret != "" && currentInternalSecret != internalCertificateSecretName(c, tls)) {
		return errors.New("certificates and issuer can't change on an existing instance")
	}

	return nil
}

// internalCertificateSecretName is the secret holding the certificate
// members authenticate each other with.
func internalCertificateSecretName(c *controller.Context, tls definition.TLSParameters) string {
	if tls.InternalCertificateSecretName != "" {
		return tls.InternalCertificateSecretName
	}
	return c.Name() + "-ssl-internal"
}

// tlsConnection returns the CA clients verify the instance with, and whether
// clients must use TLS.
func tlsConnection(c *controller.Context) (ca []byte, required bool, err error) {
	tls, err := tlsParameters(c)
	if err != nil || !tlsEnabled(tls.Mode) {
		return nil, false, err
	}

	secret := &corev1.Secret{}
	if err := c.Get(secret, clientCertificateSecretName(c, tls)); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, tls.Mode == definition.TLSModeRequire, nil
		}
		return nil, false, fmt.Errorf("get certificate secret: %w", err)
	}

	return secret.Data[caKey], tls.Mode == definition.TLSModeRequire, nil
}
