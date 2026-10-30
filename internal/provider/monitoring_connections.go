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
	"maps"

	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
)

const (
	// mongodContainerName is the operator's container serving mongod.
	mongodContainerName = "mongod"
	// caKeyInCertificateSecret is the CA key of the operator's (and
	// cert-manager's) certificate secrets.
	caKeyInCertificateSecret = "ca.crt"
)

// monitorSecretName is the Instance-owned Secret of the monitor credential.
func monitorSecretName(c *controller.Context) string {
	return c.Name() + "-monitor"
}

// syncMonitorCredential keeps the monitor credential Secret while a bound
// class needs connections: the operator's cluster monitor user
// (clusterMonitor plus read on local), copied out of the users secret so
// agents never get access to the admin credentials stored next to it. It is
// re-copied on every Sync, so a rotated password follows.
func syncMonitorCredential(c *controller.Context, usersSecretName string) error {
	req, err := c.RequiredSources()
	if err != nil {
		return err
	}
	current := &corev1.Secret{}
	err = c.Get(current, monitorSecretName(c))
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("get monitor credential secret: %w", err)
	}
	exists := err == nil

	if !req.Connections.Else(exists) {
		if exists {
			return c.Delete(current)
		}
		return nil
	}

	users := &corev1.Secret{}
	if err := c.Get(users, usersSecretName); err != nil {
		if apierrors.IsNotFound(err) {
			// The operator generates the users secret on first start.
			return nil
		}
		return fmt.Errorf("get users secret: %w", err)
	}
	username, password := users.Data[clusterMonitorUserKey], users.Data[clusterMonitorPasswordKey]
	if len(username) == 0 || len(password) == 0 {
		return nil
	}
	return c.Apply(&corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: monitorSecretName(c), Namespace: c.Namespace()},
		Type:       corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			corev1alpha1.CredentialSecretKeyUsername: username,
			corev1alpha1.CredentialSecretKeyPassword: password,
		},
	})
}

// monitorCredentialPublished reports whether the monitor credential Secret
// exists, which is when connection endpoints are published.
func monitorCredentialPublished(c *controller.Context) (bool, error) {
	err := c.Get(&corev1.Secret{}, monitorSecretName(c))
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get monitor credential secret: %w", err)
	}
	return true, nil
}

// connectionSources are the mongod endpoints agents connect to directly,
// one per serving mongod component; mongos is not one of them, since
// per-instance statistics come from each mongod.
func connectionSources(c *controller.Context, psmdb *psmdbv1.PerconaServerMongoDB) []corev1alpha1.ConnectionEndpoint {
	ports := map[string]int32{}
	if len(psmdb.Spec.Replsets) > 0 {
		ports[common.ComponentEngine] = psmdb.Spec.Replsets[0].GetPort()
	}
	if psmdb.Spec.Sharding.Enabled && psmdb.Spec.Sharding.ConfigsvrReplSet != nil {
		ports[common.ComponentConfigServer] = psmdb.Spec.Sharding.ConfigsvrReplSet.GetPort()
	}

	tls := connectionTLS(c, psmdb)
	var out []corev1alpha1.ConnectionEndpoint
	for _, component := range servingComponents(psmdb) {
		port, isMongod := ports[component]
		if !isMongod {
			continue
		}
		out = append(out, corev1alpha1.ConnectionEndpoint{
			Component: component,
			Kind:      "mongodb",
			PodSelector: map[string]string{
				controller.InstanceLabel:  c.Name(),
				controller.ComponentLabel: component,
			},
			Container: mongodContainerName,
			Port:      intstr.FromInt32(port),
			TLS:       tls,
			Auth:      &corev1alpha1.MetricsEndpointAuth{CredentialProfile: corev1alpha1.CredentialProfileMonitor},
		})
	}
	return out
}

// connectionTLS is set whenever mongod accepts TLS (preferTLS too), so agents
// keep the same configuration when the mode is tightened to requireTLS. No
// client certificate is published: mongod accepts clients without one (see
// acceptClientsWithoutCertificates), and the only certificate at hand is the
// server's own, whose key must not be handed to agents.
func connectionTLS(c *controller.Context, psmdb *psmdbv1.PerconaServerMongoDB) *corev1alpha1.ConnectionTLS {
	if psmdb.Spec.TLS == nil || psmdb.Spec.TLS.Mode == psmdbv1.TLSModeDisabled || psmdb.Spec.TLS.Mode == "" {
		return nil
	}
	secret := c.Name() + "-ssl"
	if psmdb.Spec.Secrets != nil && psmdb.Spec.Secrets.SSL != "" {
		secret = psmdb.Spec.Secrets.SSL
	}
	return &corev1alpha1.ConnectionTLS{
		Required: psmdb.Spec.TLS.Mode == psmdbv1.TLSModeRequire,
		CA: &commonv1alpha1.CABundleSource{SecretKeyRef: &commonv1alpha1.SecretKeyRef{
			Name: secret,
			Key:  caKeyInCertificateSecret,
		}},
	}
}

// renderMonitoringPodAnnotations puts the annotations requested by the bound
// monitoring classes on each component's pods, in the same apply as the
// rest of the engine CR.
func renderMonitoringPodAnnotations(c *controller.Context, psmdb *psmdbv1.PerconaServerMongoDB) error {
	merge := func(component string, into map[string]string) (map[string]string, error) {
		requested, err := c.MonitoringPodAnnotations(component)
		if err != nil || len(requested) == 0 {
			return into, err
		}
		if into == nil {
			into = make(map[string]string, len(requested))
		}
		maps.Copy(into, requested)
		return into, nil
	}

	var err error
	for _, rs := range psmdb.Spec.Replsets {
		if rs.Annotations, err = merge(common.ComponentEngine, rs.Annotations); err != nil {
			return err
		}
	}
	if !psmdb.Spec.Sharding.Enabled {
		return nil
	}
	if cfg := psmdb.Spec.Sharding.ConfigsvrReplSet; cfg != nil {
		if cfg.Annotations, err = merge(common.ComponentConfigServer, cfg.Annotations); err != nil {
			return err
		}
	}
	if mongos := psmdb.Spec.Sharding.Mongos; mongos != nil {
		if mongos.Annotations, err = merge(common.ComponentProxy, mongos.Annotations); err != nil {
			return err
		}
	}
	return nil
}
