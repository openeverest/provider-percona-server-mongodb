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
	"os"

	goversion "github.com/hashicorp/go-version"
	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
)

// skewWindowMinors is how far the PSMDB operator tolerates an engine CR
// lagging behind it: the operator supports the last three minor versions,
// so a distance of three or more minors is unmanageable.
const skewWindowMinors = 3

// targetOperatorVersionEnv is set by the chart's pre-upgrade hook Job to the
// bundled operator subchart version the target release would install.
const targetOperatorVersionEnv = "TARGET_OPERATOR_VERSION"

var _ controller.UpgradeProvider = (*PSMDBProvider)(nil)

// CheckUpgrade blocks a provider upgrade that would push an Instance's
// deferred compatibility upgrade outside the target operator's skew window
// (bounded deferral): a held convergence restart may be deferred indefinitely
// on the current operator, but not across upgrades that would leave the
// engine CR unmanageable.
func (p *PSMDBProvider) CheckUpgrade(c *controller.Context, _ *corev1alpha1.ProviderSpec, instances []corev1alpha1.Instance) []controller.UpgradeIssue {
	targetRaw := os.Getenv(targetOperatorVersionEnv)
	if targetRaw == "" {
		// Not running inside the hook; nothing to check.
		return nil
	}
	targetVer, err := goversion.NewVersion(targetRaw)
	if err != nil {
		return []controller.UpgradeIssue{{
			Severity: controller.UpgradeWarning,
			Reason:   "SkewUnverified",
			Message:  fmt.Sprintf("cannot verify operator skew: target operator version %q is not a valid version", targetRaw),
		}}
	}

	var issues []controller.UpgradeIssue
	for i := range instances {
		in := &instances[i]
		if in.DeletionTimestamp != nil {
			continue
		}
		if issue := p.checkInstanceSkew(c, in, targetVer); issue != nil {
			issues = append(issues, *issue)
		}
	}
	return issues
}

func (p *PSMDBProvider) checkInstanceSkew(c *controller.Context, in *corev1alpha1.Instance, targetVer *goversion.Version) *controller.UpgradeIssue {
	issue := &controller.UpgradeIssue{
		InstanceName: in.Name,
		Namespace:    in.Namespace,
	}

	current := &psmdbv1.PerconaServerMongoDB{}
	err := c.Client().Get(c.Context(), client.ObjectKey{Namespace: in.Namespace, Name: in.Name}, current)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		issue.Severity = controller.UpgradeWarning
		issue.Reason = "SkewUnverified"
		issue.Message = fmt.Sprintf("cannot verify operator skew: fetching the engine resource failed: %v", err)
		return issue
	}
	if current.Spec.CRVersion == "" {
		return nil
	}
	crVer, err := goversion.NewVersion(current.Spec.CRVersion)
	if err != nil {
		issue.Severity = controller.UpgradeWarning
		issue.Reason = "SkewUnverified"
		issue.Message = fmt.Sprintf("cannot verify operator skew: engine compatibility version %q is not a valid version", current.Spec.CRVersion)
		return issue
	}

	if !withinSkewWindow(targetVer, crVer) {
		issue.Severity = controller.UpgradeError
		issue.Reason = "SkewWindowExceeded"
		issue.Message = fmt.Sprintf(
			"this database has a deferred compatibility upgrade (still at %s) that the target operator %s would no longer manage; approve the pending restart (spec.maintenance.approved) and let it converge before upgrading the provider",
			crVer.Original(), targetVer.Original())
		return issue
	}
	return nil
}

// withinSkewWindow reports whether an engine CR at crVer stays manageable by
// an operator at targetVer. Different majors are always outside the window.
func withinSkewWindow(targetVer, crVer *goversion.Version) bool {
	t, c := targetVer.Segments(), crVer.Segments()
	if t[0] != c[0] {
		return false
	}
	return t[1]-c[1] < skewWindowMinors
}
