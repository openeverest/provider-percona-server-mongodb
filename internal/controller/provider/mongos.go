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
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	corev1 "k8s.io/api/core/v1"
)

// configureMongos configures the mongos (proxy) component for sharded clusters.
func configureMongos(c *controller.Context) *psmdbv1.MongosSpec {
	in := c.Instance()
	spec := in.Spec
	proxy := spec.Components[common.ComponentProxy]

	mongosSpec := &psmdbv1.MongosSpec{
		Size: 3,
		MultiAZ: psmdbv1.MultiAZ{
			Resources: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{},
			},
		},
	}

	if proxy.Replicas != nil {
		mongosSpec.Size = *proxy.Replicas
	}

	if proxy.Resources != nil && proxy.Resources.Limits != nil {
		if !proxy.Resources.Limits.Cpu().IsZero() {
			mongosSpec.MultiAZ.Resources.Limits[corev1.ResourceCPU] = *proxy.Resources.Limits.Cpu()
		}
		if !proxy.Resources.Limits.Memory().IsZero() {
			mongosSpec.MultiAZ.Resources.Limits[corev1.ResourceMemory] = *proxy.Resources.Limits.Memory()
		}
	}

	// TODO: implement exposing mongos
	mongosSpec.Expose = psmdbv1.MongosExpose{
		Expose: psmdbv1.Expose{
			ExposeType:         corev1.ServiceTypeClusterIP,
			ServiceAnnotations: map[string]string{},
		},
	}

	return mongosSpec
}
