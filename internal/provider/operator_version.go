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
	"os"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
)

// operatorImageRepo identifies the bundled PSMDB operator's container image,
// independent of the Helm release name its Deployment carries.
const operatorImageRepo = "percona/percona-server-mongodb-operator"

const inClusterNamespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

// currentNamespace returns the namespace this provider runs in — the chart's
// release namespace, which is also where the operator subchart deploys.
// Overridable in tests.
var currentNamespace = func() (string, error) {
	if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		return ns, nil
	}
	data, err := os.ReadFile(inClusterNamespaceFile)
	if err != nil {
		return "", fmt.Errorf("determining provider namespace: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

// operatorVersion returns the version of the PSMDB operator actually running,
// parsed from its Deployment's image tag (the same approach the v1 platform
// used). The Deployment is discovered by image rather than name because the
// subchart derives its name from the Helm release.
func operatorVersion(c *controller.Context) (string, error) {
	namespace, err := currentNamespace()
	if err != nil {
		return "", err
	}
	deployments := &appsv1.DeploymentList{}
	if err := c.Client().List(c.Context(), deployments, client.InNamespace(namespace)); err != nil {
		return "", fmt.Errorf("listing deployments in %q: %w", namespace, err)
	}
	for _, d := range deployments.Items {
		for _, container := range d.Spec.Template.Spec.Containers {
			repo, tag, ok := strings.Cut(container.Image, ":")
			if !ok || !strings.HasSuffix(repo, operatorImageRepo) || strings.Contains(tag, "@") {
				continue
			}
			return strings.TrimPrefix(tag, "v"), nil
		}
	}
	return "", errors.New("PSMDB operator deployment not found in namespace " + namespace)
}
