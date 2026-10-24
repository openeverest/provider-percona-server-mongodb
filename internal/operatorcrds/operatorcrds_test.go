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

package operatorcrds

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var operatorCRDNames = []string{
	"perconaservermongodbbackups.psmdb.percona.com",
	"perconaservermongodbclustersyncs.psmdb.percona.com",
	"perconaservermongodbrestores.psmdb.percona.com",
	"perconaservermongodbs.psmdb.percona.com",
}

func TestCRDs(t *testing.T) {
	t.Parallel()

	crds, err := CRDs()

	require.NoError(t, err)
	names := make([]string, 0, len(crds))
	for _, crd := range crds {
		names = append(names, crd.GetName())
		assert.NotContains(t, crd.Object, "status", "a server-side apply must not carry status")
	}
	assert.ElementsMatch(t, operatorCRDNames, names)
}

func TestApply(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, apiextensionsv1.AddToScheme(scheme))
	stale := &apiextensionsv1.CustomResourceDefinition{}
	stale.Name = "perconaservermongodbs.psmdb.percona.com"
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(stale).Build()

	require.NoError(t, Apply(context.Background(), c))

	for _, name := range operatorCRDNames {
		crd := &apiextensionsv1.CustomResourceDefinition{}
		require.NoError(t, c.Get(context.Background(), client.ObjectKey{Name: name}, crd), name)
		assert.Equal(t, "psmdb.percona.com", crd.Spec.Group, name)
		assert.NotEmpty(t, crd.Spec.Versions, name)
	}
}
