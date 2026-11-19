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
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	cmapi "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/yaml"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
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
	return instanceTLSParameters(c.Instance())
}

// instanceTLSParameters returns the TLS parameters of in with defaults applied.
func instanceTLSParameters(in *corev1alpha1.Instance) (definition.TLSParameters, error) {
	params := definition.Parameters{}
	if raw := in.Spec.Parameters; raw != nil && len(raw.Raw) > 0 {
		if err := json.Unmarshal(raw.Raw, &params); err != nil {
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

// tlsModes and issuerKinds are the values the TLS parameters accept.
var (
	tlsModes    = []definition.TLSMode{definition.TLSModePrefer, definition.TLSModeRequire, definition.TLSModeDisabled}
	issuerKinds = []definition.IssuerKind{definition.IssuerKindIssuer, definition.IssuerKindClusterIssuer}
)

// validateTLS checks the TLS parameters on their own. validateTLSReferences
// checks the certificate secrets and what a running instance can't change.
func validateTLS(in *corev1alpha1.Instance) field.ErrorList {
	tls, err := instanceTLSParameters(in)
	if err != nil {
		return field.ErrorList{field.InternalError(field.NewPath("spec", "parameters"), err)}
	}
	path := field.NewPath("spec", "parameters", "tls")

	var errs field.ErrorList
	if !slices.Contains(tlsModes, tls.Mode) {
		errs = append(errs, field.NotSupported(path.Child("mode"), tls.Mode, tlsModes))
	}
	if tls.Issuer != nil && !slices.Contains(issuerKinds, tls.Issuer.Kind) {
		errs = append(errs, field.NotSupported(path.Child("issuer", "kind"), tls.Issuer.Kind, issuerKinds))
	}

	customCertificates := tls.CertificateSecretName != "" || tls.InternalCertificateSecretName != ""
	switch {
	case !tlsEnabled(tls.Mode) && (customCertificates || tls.Issuer != nil):
		errs = append(errs, field.Forbidden(path, "certificates and issuer apply only with TLS enabled"))
	case tls.InternalCertificateSecretName != "" && tls.CertificateSecretName == "":
		errs = append(errs, field.Required(path.Child("certificateSecretName"),
			"internalCertificateSecretName requires it",
		))
	case tls.CertificateSecretName != "" && tls.Issuer != nil:
		errs = append(errs, field.Forbidden(path.Child("issuer"), "use either certificate secrets or an issuer, not both"))
	}

	return errs
}

// validateTLSTransition rejects, on an update of an instance the provider has
// created, the TLS changes a running cluster can't take. Turning TLS on or off
// switches how members authenticate each other (x509 or keyfile); a rolling
// restart would leave the members unable to talk to each other halfway. A new
// certificate source brings a new CA, which members still on the old one
// wouldn't trust. validateTLSChange checks the same at reconcile time, against
// the PerconaServerMongoDB.
func validateTLSTransition(in, old *corev1alpha1.Instance) field.ErrorList {
	if old == nil || !reconciledBefore(old) {
		return nil
	}
	before, err := instanceTLSParameters(old)
	if err != nil {
		return nil
	}
	after, err := instanceTLSParameters(in)
	if err != nil {
		return nil
	}

	path := field.NewPath("spec", "parameters", "tls")
	var errs field.ErrorList
	if tlsEnabled(before.Mode) != tlsEnabled(after.Mode) {
		errs = append(errs, field.Forbidden(path.Child("mode"), "TLS can't be turned on or off on an existing instance"))
	}
	if certificateSource(before) != certificateSource(after) {
		errs = append(errs, field.Forbidden(path, "certificates and issuer can't change on an existing instance"))
	}
	return errs
}

// reconciledBefore reports whether the provider has reconciled the Instance
// before, and so may have created its cluster. The runtime records the
// version bundle it applies the first time a reconcile gets through the
// provider, and keeps it from then on. The phase does not tell: an Instance
// stays Pending until its database is first available, long after its members
// were started.
func reconciledBefore(in *corev1alpha1.Instance) bool {
	return in.Status.Version != ""
}

// certificateSource identifies where an instance's certificates come from.
func certificateSource(tls definition.TLSParameters) string {
	issuer := "/"
	if tls.Issuer != nil {
		issuer = string(tls.Issuer.Kind) + "/" + tls.Issuer.Name
	}
	return issuer + " " + tls.CertificateSecretName + " " + tls.InternalCertificateSecretName
}

// validateTLSReferences checks that the certificate secrets hold a
// certificate, and that the TLS parameters keep what can't change on the
// running cluster.
func validateTLSReferences(c *controller.Context) (field.ErrorList, error) {
	tls, err := tlsParameters(c)
	if err != nil {
		return nil, err
	}

	path := field.NewPath("spec", "parameters", "tls")
	var errs field.ErrorList
	for _, ref := range []struct {
		field, name string
	}{
		{"certificateSecretName", tls.CertificateSecretName},
		{"internalCertificateSecretName", tls.InternalCertificateSecretName},
	} {
		if ref.name == "" {
			continue
		}
		secretErrs, err := validateCertificateSecret(c, path.Child(ref.field), ref.name)
		if err != nil {
			return nil, err
		}
		errs = append(errs, secretErrs...)
	}

	changeErrs, err := validateTLSChange(c, tls)
	if err != nil {
		return nil, err
	}
	return append(errs, changeErrs...), nil
}

func validateCertificateSecret(c *controller.Context, path *field.Path, name string) (field.ErrorList, error) {
	secret := &corev1.Secret{}
	if err := c.Get(secret, name); err != nil {
		if apierrors.IsNotFound(err) {
			return field.ErrorList{field.NotFound(path, name)}, nil
		}
		return nil, fmt.Errorf("get certificate secret %q: %w", name, err)
	}
	var missing []string
	for _, key := range certificateSecretKeys {
		if len(secret.Data[key]) == 0 {
			missing = append(missing, strconv.Quote(key))
		}
	}
	if len(missing) > 0 {
		return field.ErrorList{field.Invalid(path, name, "missing "+strings.Join(missing, ", "))}, nil
	}
	return nil, nil
}

// validateTLSChange is validateTLSTransition against the running
// PerconaServerMongoDB, for reconcile time, when the previous Instance is
// unknown.
func validateTLSChange(c *controller.Context, tls definition.TLSParameters) (field.ErrorList, error) {
	current := &psmdbv1.PerconaServerMongoDB{}
	if err := c.Get(current, c.Name()); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("fetching current PSMDB CR: %w", err)
	}

	path := field.NewPath("spec", "parameters", "tls")
	var errs field.ErrorList
	currentMode := psmdbv1.TLSModePrefer
	if current.Spec.TLS != nil && current.Spec.TLS.Mode != "" {
		currentMode = current.Spec.TLS.Mode
	}
	if (currentMode != psmdbv1.TLSModeDisabled) != tlsEnabled(tls.Mode) {
		errs = append(errs, field.Forbidden(path.Child("mode"), "TLS can't be turned on or off on an existing instance"))
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
		errs = append(errs, field.Forbidden(path, "certificates and issuer can't change on an existing instance"))
	}

	return errs, nil
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
