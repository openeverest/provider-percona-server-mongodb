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
	"testing"

	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mongodb/definition"
)

// tlsContext builds a context for an Instance named my-mongo carrying tls as
// its TLS parameters (nil leaves spec.parameters out), with maintenance
// tolerance up to approved.
func tlsContext(t *testing.T, tls *definition.TLSParameters, approved string, objs ...client.Object) *controller.Context {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, psmdbv1.SchemeBuilder.AddToScheme(scheme))

	in := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "my-mongo", Namespace: "db", UID: "uid"},
	}
	if tls != nil {
		raw, err := json.Marshal(definition.Parameters{TLS: tls})
		require.NoError(t, err)
		in.Spec.Parameters = &runtime.RawExtension{Raw: raw}
	}
	if approved != "" {
		in.Spec.Maintenance = &corev1alpha1.MaintenanceSpec{Approved: approved}
	}

	// The fake client writes to the objects it is seeded with, and subtests
	// running in parallel share their fixtures.
	seed := make([]client.Object, 0, len(objs)+1)
	for _, obj := range objs {
		seed = append(seed, obj.DeepCopyObject().(client.Object))
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(seed, in)...).Build()
	return controller.NewContext(context.Background(), cl, in, "psmdb")
}

func certificateSecret(name string, keys ...string) *corev1.Secret {
	data := map[string][]byte{}
	for _, k := range keys {
		data[k] = []byte("pem")
	}
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "db"}, Data: data}
}

// runningPSMDB is the PSMDB resource of an existing instance.
func runningPSMDB(mutate func(*psmdbv1.PerconaServerMongoDB)) *psmdbv1.PerconaServerMongoDB {
	psmdb := &psmdbv1.PerconaServerMongoDB{
		ObjectMeta: metav1.ObjectMeta{Name: "my-mongo", Namespace: "db"},
		Spec: psmdbv1.PerconaServerMongoDBSpec{
			Replsets: []*psmdbv1.ReplsetSpec{{Name: "rs0", Configuration: psmdbDefaultConfigurationTemplate}},
			Secrets:  &psmdbv1.SecretsSpec{SSLInternal: "my-mongo-ssl-internal"},
		},
	}
	if mutate != nil {
		mutate(psmdb)
	}
	return psmdb
}

func TestTLSParameters(t *testing.T) {
	t.Parallel()

	t.Run("TLS is preferred by default", func(t *testing.T) {
		t.Parallel()

		tls, err := tlsParameters(tlsContext(t, nil, ""))

		require.NoError(t, err)
		assert.Equal(t, definition.TLSParameters{Mode: definition.TLSModePrefer}, tls)
	})

	t.Run("an issuer without a name is no issuer", func(t *testing.T) {
		t.Parallel()

		tls, err := tlsParameters(tlsContext(t, &definition.TLSParameters{
			Issuer: &definition.IssuerReference{Kind: definition.IssuerKindIssuer},
		}, ""))

		require.NoError(t, err)
		assert.Nil(t, tls.Issuer)
	})

	t.Run("an issuer is an Issuer by default", func(t *testing.T) {
		t.Parallel()

		tls, err := tlsParameters(tlsContext(t, &definition.TLSParameters{
			Issuer: &definition.IssuerReference{Name: "ca"},
		}, ""))

		require.NoError(t, err)
		assert.Equal(t, definition.IssuerKindIssuer, tls.Issuer.Kind)
	})
}

func TestConfigureTLS(t *testing.T) {
	t.Parallel()

	t.Run("generated certificates", func(t *testing.T) {
		t.Parallel()

		psmdb := &psmdbv1.PerconaServerMongoDB{}
		configureTLS(psmdb, definition.TLSParameters{Mode: definition.TLSModeRequire}, false)

		assert.Equal(t, &psmdbv1.TLSSpec{Mode: psmdbv1.TLSModeRequire}, psmdb.Spec.TLS)
	})

	t.Run("cert-manager issuer", func(t *testing.T) {
		t.Parallel()

		psmdb := &psmdbv1.PerconaServerMongoDB{}
		configureTLS(psmdb, definition.TLSParameters{
			Mode:   definition.TLSModePrefer,
			Issuer: &definition.IssuerReference{Name: "corp-ca", Kind: definition.IssuerKindClusterIssuer},
		}, false)

		assert.Equal(t, "corp-ca", psmdb.Spec.TLS.IssuerConf.Name)
		assert.Equal(t, "ClusterIssuer", psmdb.Spec.TLS.IssuerConf.Kind)
		assert.Equal(t, "cert-manager.io", psmdb.Spec.TLS.IssuerConf.Group)
	})

	t.Run("kept certificates are never replaced", func(t *testing.T) {
		t.Parallel()

		psmdb := &psmdbv1.PerconaServerMongoDB{}
		configureTLS(psmdb, definition.TLSParameters{Mode: definition.TLSModePrefer}, true)

		assert.Equal(t, psmdbv1.CertManagementUserProvidedOnly, psmdb.Spec.TLS.CertManagementPolicy)
	})
}

func TestKeepCertificates(t *testing.T) {
	t.Parallel()

	generated := certificateSecret("my-mongo-ssl", certificateSecretKeys...)
	issued := certificateSecret("my-mongo-ssl", certificateSecretKeys...)
	issued.Annotations = map[string]string{"cert-manager.io/certificate-name": "my-mongo-ssl"}

	tests := []struct {
		name string
		tls  *definition.TLSParameters
		objs []client.Object
		want bool
	}{
		{name: "the user's certificates", tls: &definition.TLSParameters{CertificateSecretName: "mine"}, want: true},
		{name: "certificates not generated yet"},
		{name: "certificates the operator generated", objs: []client.Object{generated}, want: true},
		{name: "certificates cert-manager issued", objs: []client.Object{issued}},
		{
			name: "certificates from an issuer",
			tls:  &definition.TLSParameters{Issuer: &definition.IssuerReference{Name: "ca"}},
			objs: []client.Object{generated},
		},
		{name: "no TLS", tls: &definition.TLSParameters{Mode: definition.TLSModeDisabled}, objs: []client.Object{generated}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := tlsContext(t, tt.tls, "", tt.objs...)
			tls, err := tlsParameters(c)
			require.NoError(t, err)

			got, err := keepCertificates(c, tls)

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestWithClientCertificatesOptional(t *testing.T) {
	t.Parallel()

	parse := func(t *testing.T, conf psmdbv1.MongoConfiguration) map[string]any {
		t.Helper()
		out := map[string]any{}
		require.NoError(t, yaml.Unmarshal([]byte(conf), &out))
		return out
	}

	t.Run("added to the default configuration", func(t *testing.T) {
		t.Parallel()

		conf, err := withClientCertificatesOptional(psmdbDefaultConfigurationTemplate)

		require.NoError(t, err)
		got := parse(t, conf)
		assert.Equal(t, map[string]any{"net": map[string]any{"tls": map[string]any{"allowConnectionsWithoutCertificates": true}},
			"operationProfiling": map[string]any{"mode": "slowOp"}}, got)
	})

	t.Run("the user's explicit choice is kept", func(t *testing.T) {
		t.Parallel()

		in := psmdbv1.MongoConfiguration("net:\n  tls:\n    allowConnectionsWithoutCertificates: false\n")
		conf, err := withClientCertificatesOptional(in)

		require.NoError(t, err)
		assert.Equal(t, in, conf)
	})

	t.Run("a configuration that isn't a mapping is passed through", func(t *testing.T) {
		t.Parallel()

		for _, in := range []psmdbv1.MongoConfiguration{"not yaml: [", "net: plain"} {
			conf, err := withClientCertificatesOptional(in)

			require.NoError(t, err)
			assert.Equal(t, in, conf)
		}
	})
}

func TestAcceptClientsWithoutCertificates(t *testing.T) {
	t.Parallel()

	desired := func() *psmdbv1.PerconaServerMongoDB {
		psmdb := shardedPSMDB()
		for _, rs := range psmdb.Spec.Replsets {
			rs.Configuration = psmdbDefaultConfigurationTemplate
		}
		psmdb.Spec.Sharding.ConfigsvrReplSet.Configuration = psmdbDefaultConfigurationTemplate
		return psmdb
	}
	prefer := definition.TLSParameters{Mode: definition.TLSModePrefer}

	t.Run("a new instance accepts them", func(t *testing.T) {
		t.Parallel()

		c := tlsContext(t, nil, "")
		psmdb := desired()

		require.NoError(t, acceptClientsWithoutCertificates(c, psmdb, prefer))

		assert.True(t, acceptsClientsWithoutCertificates(psmdb))
		for _, conf := range []psmdbv1.MongoConfiguration{
			psmdb.Spec.Sharding.ConfigsvrReplSet.Configuration,
			psmdb.Spec.Sharding.Mongos.Configuration,
		} {
			assert.Contains(t, string(conf), "allowConnectionsWithoutCertificates: true")
		}
		assert.Empty(t, c.GetPendingMaintenance())
	})

	t.Run("an existing instance waits for approval", func(t *testing.T) {
		t.Parallel()

		c := tlsContext(t, nil, "", runningPSMDB(nil))
		psmdb := desired()

		require.NoError(t, acceptClientsWithoutCertificates(c, psmdb, prefer))

		assert.Equal(t, psmdbDefaultConfigurationTemplate, string(psmdb.Spec.Replsets[0].Configuration),
			"the configuration stays as is, so the pods don't restart")
		pending := c.GetPendingMaintenance()
		require.Len(t, pending, 1)
		assert.Equal(t, clientCertificateOptionalToken, pending[0].ApprovalToken)
	})

	t.Run("an existing instance accepts them once approved", func(t *testing.T) {
		t.Parallel()

		c := tlsContext(t, nil, clientCertificateOptionalToken, runningPSMDB(nil))
		psmdb := desired()

		require.NoError(t, acceptClientsWithoutCertificates(c, psmdb, prefer))

		assert.True(t, acceptsClientsWithoutCertificates(psmdb))
	})

	t.Run("an instance that accepts them keeps doing so", func(t *testing.T) {
		t.Parallel()

		running := runningPSMDB(func(p *psmdbv1.PerconaServerMongoDB) {
			conf, err := withClientCertificatesOptional(p.Spec.Replsets[0].Configuration)
			require.NoError(t, err)
			p.Spec.Replsets[0].Configuration = conf
		})
		c := tlsContext(t, nil, "", running)
		psmdb := desired()

		require.NoError(t, acceptClientsWithoutCertificates(c, psmdb, prefer))

		assert.True(t, acceptsClientsWithoutCertificates(psmdb))
		assert.Empty(t, c.GetPendingMaintenance())
	})

	t.Run("nothing to accept without TLS", func(t *testing.T) {
		t.Parallel()

		c := tlsContext(t, nil, "")
		psmdb := desired()

		require.NoError(t, acceptClientsWithoutCertificates(c, psmdb, definition.TLSParameters{Mode: definition.TLSModeDisabled}))

		assert.False(t, acceptsClientsWithoutCertificates(psmdb))
	})
}

func TestValidateTLS(t *testing.T) {
	t.Parallel()

	userCertificates := runningPSMDB(func(p *psmdbv1.PerconaServerMongoDB) {
		p.Spec.Secrets.SSL = "mine"
		p.Spec.TLS = &psmdbv1.TLSSpec{Mode: psmdbv1.TLSModePrefer, CertManagementPolicy: psmdbv1.CertManagementUserProvidedOnly}
	})

	tests := []struct {
		name      string
		tls       *definition.TLSParameters
		objs      []client.Object
		expectErr string
	}{
		{name: "defaults"},
		{name: "required", tls: &definition.TLSParameters{Mode: definition.TLSModeRequire}},
		{name: "disabled at creation", tls: &definition.TLSParameters{Mode: definition.TLSModeDisabled}},
		{name: "unknown mode", tls: &definition.TLSParameters{Mode: "allowTLS"}, expectErr: "mode must be one of"},
		{
			name:      "unknown issuer kind",
			tls:       &definition.TLSParameters{Issuer: &definition.IssuerReference{Name: "ca", Kind: "Vault"}},
			expectErr: "issuer.kind must be",
		},
		{
			name:      "certificates without TLS",
			tls:       &definition.TLSParameters{Mode: definition.TLSModeDisabled, CertificateSecretName: "mine"},
			expectErr: "apply only with TLS enabled",
		},
		{
			name:      "internal certificate alone",
			tls:       &definition.TLSParameters{InternalCertificateSecretName: "mine"},
			expectErr: "requires certificateSecretName",
		},
		{
			name: "certificates and issuer",
			tls: &definition.TLSParameters{
				CertificateSecretName: "mine",
				Issuer:                &definition.IssuerReference{Name: "ca"},
			},
			expectErr: "either certificate secrets or an issuer",
		},
		{
			name:      "missing certificate secret",
			tls:       &definition.TLSParameters{CertificateSecretName: "mine"},
			expectErr: `get certificate secret "mine"`,
		},
		{
			name:      "incomplete certificate secret",
			tls:       &definition.TLSParameters{CertificateSecretName: "mine"},
			objs:      []client.Object{certificateSecret("mine", "tls.crt", "tls.key")},
			expectErr: `certificate secret "mine" is missing "ca.crt"`,
		},
		{
			name: "complete certificate secret",
			tls:  &definition.TLSParameters{CertificateSecretName: "mine"},
			objs: []client.Object{certificateSecret("mine", certificateSecretKeys...)},
		},
		{
			name: "TLS required on a running instance",
			tls:  &definition.TLSParameters{Mode: definition.TLSModeRequire},
			objs: []client.Object{runningPSMDB(nil)},
		},
		{
			name:      "TLS turned off on a running instance",
			tls:       &definition.TLSParameters{Mode: definition.TLSModeDisabled},
			objs:      []client.Object{runningPSMDB(nil)},
			expectErr: "can't be turned on or off",
		},
		{
			name: "TLS turned on on a running instance",
			objs: []client.Object{runningPSMDB(func(p *psmdbv1.PerconaServerMongoDB) {
				p.Spec.TLS = &psmdbv1.TLSSpec{Mode: psmdbv1.TLSModeDisabled}
			})},
			expectErr: "can't be turned on or off",
		},
		{
			name:      "issuer added to a running instance",
			tls:       &definition.TLSParameters{Issuer: &definition.IssuerReference{Name: "ca"}},
			objs:      []client.Object{runningPSMDB(nil)},
			expectErr: "can't change on an existing instance",
		},
		{
			name: "certificates added to a running instance",
			tls:  &definition.TLSParameters{CertificateSecretName: "mine"},
			objs: []client.Object{
				certificateSecret("mine", certificateSecretKeys...),
				runningPSMDB(nil),
			},
			expectErr: "can't change on an existing instance",
		},
		{
			name: "certificates kept on a running instance",
			tls:  &definition.TLSParameters{CertificateSecretName: "mine"},
			objs: []client.Object{certificateSecret("mine", certificateSecretKeys...), userCertificates},
		},
		{
			name:      "certificates removed from a running instance",
			objs:      []client.Object{userCertificates},
			expectErr: "can't change on an existing instance",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateTLS(tlsContext(t, tt.tls, "", tt.objs...))

			if tt.expectErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.expectErr)
		})
	}
}

func TestTLSConnection(t *testing.T) {
	t.Parallel()

	generated := certificateSecret("my-mongo-ssl", certificateSecretKeys...)
	generated.Data[caKey] = []byte("generated CA")
	mine := certificateSecret("mine", certificateSecretKeys...)
	mine.Data[caKey] = []byte("my CA")

	tests := []struct {
		name         string
		tls          *definition.TLSParameters
		wantCA       string
		wantRequired bool
	}{
		{name: "generated certificates", wantCA: "generated CA"},
		{name: "TLS required", tls: &definition.TLSParameters{Mode: definition.TLSModeRequire}, wantCA: "generated CA", wantRequired: true},
		{name: "user certificates", tls: &definition.TLSParameters{CertificateSecretName: "mine"}, wantCA: "my CA"},
		{name: "TLS disabled", tls: &definition.TLSParameters{Mode: definition.TLSModeDisabled}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ca, required, err := tlsConnection(tlsContext(t, tt.tls, "", generated, mine))

			require.NoError(t, err)
			assert.Equal(t, tt.wantCA, string(ca))
			assert.Equal(t, tt.wantRequired, required)
		})
	}
}
