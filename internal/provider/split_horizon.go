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

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"

	"github.com/openeverest/provider-percona-server-mongodb/definition"
	"github.com/openeverest/provider-percona-server-mongodb/definition/components"
	replicaset "github.com/openeverest/provider-percona-server-mongodb/definition/topologies/replicaSet"
	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
)

// applySplitHorizon applies horizon DNS entries to the first replset.
// When UseDefaults is true, values are read from the Provider CR topology defaults.
// Returns the TLS secret name to set on the PSMDB spec, or empty string if not applicable.
func applySplitHorizon(c *controller.Context, replsets []*psmdbv1.ReplsetSpec) string {
	// Split horizon DNS is not applicable to sharded clusters.
	if c.Instance().Spec.Topology != nil && c.Instance().Spec.Topology.Type != string(definition.TopologyTypeReplicaSet) {
		return ""
	}

	splitHorizon, ok := c.Instance().Spec.Components[common.ComponentDnsConfig]
	if !ok {
		return ""
	}

	var spec components.SplitHorizonCustomSpec
	if !c.TryDecodeComponentCustomSpec(splitHorizon, &spec) {
		return ""
	}

	// When UseDefaults is true, read domain and tlsSecretName from the
	// Provider CR topology defaults for the dnsConfig component.
	if spec.UseDefaults {
		defaults, err := getDnsConfigDefaults(c)
		if err != nil || defaults == nil {
			return ""
		}

		spec.Domain = defaults.CustomSpec.Domain
		spec.TLSSecretName = defaults.CustomSpec.TLSSecretName
	}

	if spec.Domain == "" {
		return ""
	}

	if len(replsets) == 0 {
		return ""
	}

	in := c.Instance()
	rs := replsets[0]

	horizons := make(psmdbv1.HorizonsSpec)
	for i := int32(0); i < rs.Size; i++ {
		podName := fmt.Sprintf("%s-%s-%d", in.GetName(), rs.Name, i)
		horizons[podName] = map[string]string{
			"external": fmt.Sprintf("%s-%s.%s", podName, in.GetNamespace(), spec.Domain),
		}
	}

	rs.Horizons = horizons

	return spec.TLSSecretName
}

// getDnsConfigDefaults gets the DNS Config component defaults from the
// Provider CR replicaSet topology definition. Returns nil for sharded topology.
func getDnsConfigDefaults(c *controller.Context) (*replicaset.SplitHorizonDefaults, error) {
	providerSpec, err := c.ProviderSpec()
	if err != nil {
		return nil, err
	}

	topo, ok := providerSpec.Topologies[string(definition.TopologyTypeReplicaSet)]
	if !ok {
		return nil, nil
	}

	topComp, ok := topo.Components[common.ComponentDnsConfig]
	if !ok || topComp.Defaults == nil {
		return nil, nil
	}

	var defaults replicaset.SplitHorizonDefaults
	if err := json.Unmarshal(topComp.Defaults.Raw, &defaults); err != nil {
		return nil, fmt.Errorf("failed to unmarshal dnsConfig defaults: %w", err)
	}

	return &defaults, nil
}
