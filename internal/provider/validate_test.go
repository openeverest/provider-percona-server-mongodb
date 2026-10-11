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
	"testing"

	"github.com/AlekSi/pointer"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller/validationtest"

	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
)

const (
	replicaSetFixture = "testdata/validate/replicaset.yaml"
	shardedFixture    = "testdata/validate/sharded.yaml"
)

// component applies mutate to the named component of the Instance.
func component(name string, mutate func(*corev1alpha1.ComponentSpec)) func(*corev1alpha1.Instance) {
	return func(in *corev1alpha1.Instance) {
		c := in.Spec.Components[name]
		mutate(&c)
		in.Spec.Components[name] = c
	}
}

// TestValidate runs the checks the runtime runs before every Sync: the
// schemas, supportedFields and catalog the provider publishes, then Validate.
func TestValidate(t *testing.T) {
	t.Parallel()
	v := validationtest.New(t, NewPSMDBProviderInterface(), providerSpecPath)

	tests := []struct {
		name    string
		fixture string
		mutate  func(*corev1alpha1.Instance)
		want    []string
	}{
		{name: "valid replica set", fixture: replicaSetFixture},
		{name: "valid sharded cluster", fixture: shardedFixture},
		{
			name:    "name too long",
			fixture: replicaSetFixture,
			mutate:  func(in *corev1alpha1.Instance) { in.Name = "orders-for-the-eu-region" },
			want:    []string{"metadata.name"},
		},
		{
			name:    "name not RFC-1035",
			fixture: replicaSetFixture,
			mutate:  func(in *corev1alpha1.Instance) { in.Name = "1-orders" },
			want:    []string{"metadata.name"},
		},
		{
			name:    "even engine replicas",
			fixture: replicaSetFixture,
			mutate:  component(common.ComponentEngine, func(c *corev1alpha1.ComponentSpec) { c.Replicas = pointer.ToInt32(2) }),
			want:    []string{"spec.components[engine].replicas"},
		},
		{
			name:    "zero engine replicas",
			fixture: replicaSetFixture,
			mutate:  component(common.ComponentEngine, func(c *corev1alpha1.ComponentSpec) { c.Replicas = pointer.ToInt32(0) }),
			want:    []string{"spec.components[engine].replicas"},
		},
		{
			name:    "engine without storage",
			fixture: replicaSetFixture,
			mutate:  component(common.ComponentEngine, func(c *corev1alpha1.ComponentSpec) { c.Storage = nil }),
			want:    []string{"spec.components[engine].storage"},
		},
		{
			name:    "engine storage too small",
			fixture: replicaSetFixture,
			mutate: component(common.ComponentEngine, func(c *corev1alpha1.ComponentSpec) {
				c.Storage.Size = resource.MustParse("512Mi")
			}),
			want: []string{"spec.components[engine].storage.size"},
		},
		{
			name:    "engine without limits",
			fixture: replicaSetFixture,
			mutate:  component(common.ComponentEngine, func(c *corev1alpha1.ComponentSpec) { c.Resources = nil }),
			want:    []string{"spec.components[engine].resources.limits"},
		},
		{
			name:    "engine limits too small or missing",
			fixture: replicaSetFixture,
			mutate: component(common.ComponentEngine, func(c *corev1alpha1.ComponentSpec) {
				c.Resources.Limits = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")}
			}),
			want: []string{
				"spec.components[engine].resources.limits[cpu]",
				"spec.components[engine].resources.limits[memory]",
			},
		},
		{
			name:    "unsupported engine service type",
			fixture: replicaSetFixture,
			mutate: component(common.ComponentEngine, func(c *corev1alpha1.ComponentSpec) {
				c.Service = &corev1alpha1.Service{ServiceType: corev1.ServiceTypeExternalName}
			}),
			want: []string{"spec.components[engine].service.serviceType"},
		},
		{
			name:    "engine version outside the catalog",
			fixture: replicaSetFixture,
			mutate:  component(common.ComponentEngine, func(c *corev1alpha1.ComponentSpec) { c.Version = "5.0.0" }),
			want:    []string{"spec.components[engine].version"},
		},
		{
			name:    "unknown engine parameter",
			fixture: replicaSetFixture,
			mutate: component(common.ComponentEngine, func(c *corev1alpha1.ComponentSpec) {
				c.Parameters = &runtime.RawExtension{Raw: []byte(`{"configration":"net:\n  maxIncomingConnections: 100"}`)}
			}),
			want: []string{"spec.components[engine].parameters.configration"},
		},
		{
			name:    "engine service in a sharded cluster",
			fixture: shardedFixture,
			mutate: component(common.ComponentEngine, func(c *corev1alpha1.ComponentSpec) {
				c.Service = &corev1alpha1.Service{ServiceType: corev1.ServiceTypeLoadBalancer}
			}),
			want: []string{"spec.components[engine].service"},
		},
		{
			name:    "unsupported proxy service type",
			fixture: shardedFixture,
			mutate: component(common.ComponentProxy, func(c *corev1alpha1.ComponentSpec) {
				c.Service = &corev1alpha1.Service{ServiceType: corev1.ServiceTypeExternalName}
			}),
			want: []string{"spec.components[proxy].service.serviceType"},
		},
		{
			name:    "sharded cluster without a proxy",
			fixture: shardedFixture,
			mutate:  func(in *corev1alpha1.Instance) { delete(in.Spec.Components, common.ComponentProxy) },
			want:    []string{"spec.components[proxy]"},
		},
		{
			name:    "no shards",
			fixture: shardedFixture,
			mutate: func(in *corev1alpha1.Instance) {
				in.Spec.Topology.Parameters = &runtime.RawExtension{Raw: []byte(`{"numShards":0}`)}
			},
			want: []string{"spec.topology.parameters.numShards"},
		},
		{
			name:    "even config servers",
			fixture: shardedFixture,
			mutate: component(common.ComponentConfigServer, func(c *corev1alpha1.ComponentSpec) {
				c.Replicas = pointer.ToInt32(2)
			}),
			want: []string{"spec.components[configServer].replicas"},
		},
		{
			name:    "one config server for a multi-node engine",
			fixture: shardedFixture,
			mutate: component(common.ComponentConfigServer, func(c *corev1alpha1.ComponentSpec) {
				c.Replicas = pointer.ToInt32(1)
			}),
			want: []string{"spec.components[configServer].replicas"},
		},
		{
			name:    "one config server for a single-node engine",
			fixture: shardedFixture,
			mutate: func(in *corev1alpha1.Instance) {
				component(common.ComponentEngine, func(c *corev1alpha1.ComponentSpec) { c.Replicas = pointer.ToInt32(1) })(in)
				component(common.ComponentConfigServer, func(c *corev1alpha1.ComponentSpec) { c.Replicas = pointer.ToInt32(1) })(in)
			},
		},
		{
			name:    "backup data source without a backup",
			fixture: replicaSetFixture,
			mutate: func(in *corev1alpha1.Instance) {
				in.Spec.DataSource = &backupv1alpha1.DataSource{Type: backupv1alpha1.DataSourceTypeBackup}
			},
			want: []string{"spec.dataSource.backup"},
		},
		{
			name:    "user secret when seeding from an Instance",
			fixture: replicaSetFixture,
			mutate: func(in *corev1alpha1.Instance) {
				in.Spec.DataSource = &backupv1alpha1.DataSource{Type: backupv1alpha1.DataSourceTypePointInTime}
				in.Spec.UserSecretRef = &commonv1alpha1.SecretRef{Name: "orders-users"}
			},
			want: []string{"spec.userSecretRef"},
		},
		{
			name:    "no topology and no engine",
			fixture: replicaSetFixture,
			mutate: func(in *corev1alpha1.Instance) {
				in.Spec.Topology = nil
				delete(in.Spec.Components, common.ComponentEngine)
			},
			want: []string{"spec.components[engine]"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := validationtest.Load(t, tt.fixture)
			if tt.mutate != nil {
				tt.mutate(in)
			}

			res := v.Run(in, nil)

			var got []string
			for _, err := range res.Errors {
				got = append(got, err.Field)
			}
			assert.Equal(t, tt.want, got, "errors: %v", res.Errors)
		})
	}
}
