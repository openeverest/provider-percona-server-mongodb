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
// subchart derives its name from the Helm release. Multiple matches with
// conflicting tags are an error rather than a coin flip.
func operatorVersion(c *controller.Context) (string, error) {
	namespace, err := currentNamespace()
	if err != nil {
		return "", err
	}
	deployments := &appsv1.DeploymentList{}
	if err := c.Client().List(c.Context(), deployments, client.InNamespace(namespace)); err != nil {
		return "", fmt.Errorf("listing deployments in %q: %w", namespace, err)
	}
	version := ""
	for _, d := range deployments.Items {
		for _, container := range d.Spec.Template.Spec.Containers {
			tag, ok := operatorImageTag(container.Image)
			if !ok {
				continue
			}
			if version != "" && version != tag {
				return "", fmt.Errorf("multiple PSMDB operator deployments with conflicting versions (%s, %s) in namespace %q", version, tag, namespace)
			}
			version = tag
		}
	}
	if version == "" {
		return "", errors.New("PSMDB operator deployment not found in namespace " + namespace)
	}
	return version, nil
}

// operatorImageTag returns the version tag when image is an operator image
// reference. The tag is everything after the last colon (a registry may carry
// a port, which sits before the first slash), with any digest stripped first;
// digest-only references carry no version and are skipped.
func operatorImageTag(image string) (string, bool) {
	image, _, _ = strings.Cut(image, "@")
	repo, tag := image, ""
	if i := strings.LastIndex(image, ":"); i >= 0 && !strings.Contains(image[i:], "/") {
		repo, tag = image[:i], image[i+1:]
	}
	if tag == "" || !strings.HasSuffix(repo, operatorImageRepo) {
		return "", false
	}
	return strings.TrimPrefix(tag, "v"), true
}
