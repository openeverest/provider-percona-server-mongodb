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

// Reasons reported on skew-check issues.
const (
	reasonSkewWindowExceeded = "SkewWindowExceeded"
	reasonSkewUnverified     = "SkewUnverified"
)

var _ controller.UpgradeProvider = (*PSMDBProvider)(nil)

// CheckUpgrade blocks a provider upgrade that would push an Instance's
// deferred compatibility upgrade outside the target operator's skew window
// (bounded deferral): a held convergence restart may be deferred indefinitely
// on the current operator, but not across upgrades that would leave the
// engine CR unmanageable.
func (p *PSMDBProvider) CheckUpgrade(h *controller.HookContext, _ *corev1alpha1.ProviderSpec, instances []corev1alpha1.Instance) []controller.UpgradeIssue {
	targetRaw := os.Getenv(targetOperatorVersionEnv)
	if targetRaw == "" {
		// CheckUpgrade only runs from the hook, whose Job template sets the
		// env; absence means the delivery drifted — say so, don't go silent.
		return []controller.UpgradeIssue{{
			Severity: controller.UpgradeWarning,
			Reason:   reasonSkewUnverified,
			Message:  "cannot verify compatibility skew: " + targetOperatorVersionEnv + " is not set on the preflight job",
		}}
	}
	targetVer, err := goversion.NewVersion(targetRaw)
	if err != nil {
		return []controller.UpgradeIssue{{
			Severity: controller.UpgradeWarning,
			Reason:   reasonSkewUnverified,
			Message:  fmt.Sprintf("cannot verify compatibility skew: target version %q is not a valid version", targetRaw),
		}}
	}

	var issues []controller.UpgradeIssue
	for i := range instances {
		in := &instances[i]
		if in.DeletionTimestamp != nil {
			continue
		}
		if issue := p.checkInstanceSkew(h, in, targetVer); issue != nil {
			issues = append(issues, *issue)
		}
	}
	return issues
}

func (p *PSMDBProvider) checkInstanceSkew(h *controller.HookContext, in *corev1alpha1.Instance, targetVer *goversion.Version) *controller.UpgradeIssue {
	issue := &controller.UpgradeIssue{
		InstanceName: in.Name,
		Namespace:    in.Namespace,
	}

	current := &psmdbv1.PerconaServerMongoDB{}
	err := h.Client().Get(h.Context(), client.ObjectKey{Namespace: in.Namespace, Name: in.Name}, current)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		issue.Severity = controller.UpgradeWarning
		issue.Reason = reasonSkewUnverified
		issue.Message = fmt.Sprintf("cannot verify compatibility skew: fetching the engine resource failed: %v", err)
		return issue
	}
	if current.Spec.CRVersion == "" {
		return nil
	}
	crVer, err := goversion.NewVersion(current.Spec.CRVersion)
	if err != nil {
		issue.Severity = controller.UpgradeWarning
		issue.Reason = reasonSkewUnverified
		issue.Message = fmt.Sprintf("cannot verify compatibility skew: engine compatibility version %q is not a valid version", current.Spec.CRVersion)
		return issue
	}

	lag, ok := skewLag(targetVer, crVer)
	switch {
	case !ok:
		// Newer than the target: this is a downgrade (chart rollback), which
		// the engine does not support at all.
		issue.Severity = controller.UpgradeError
		issue.Reason = reasonSkewWindowExceeded
		issue.Message = fmt.Sprintf(
			"this database's engine compatibility version %s is newer than what the target provider release supports (%s); the change would leave it unmanageable",
			crVer.Original(), targetVer.Original())
		return issue
	case lag >= skewWindowMinors:
		issue.Severity = controller.UpgradeError
		issue.Reason = reasonSkewWindowExceeded
		issue.Message = fmt.Sprintf(
			"this database has a deferred compatibility upgrade (engine compatibility version %s) that would fall outside the target provider release's support window; approve the pending restart (spec.maintenance.approved) and let it converge before upgrading the provider",
			crVer.Original())
		return issue
	}
	return nil
}

// skewLag returns how many minors the engine CR lags the target operator.
// ok is false when the CR is ahead of the target (downgrade) or the majors
// differ — both are outside any support window.
func skewLag(targetVer, crVer *goversion.Version) (int, bool) {
	t, c := targetVer.Segments(), crVer.Segments()
	if t[0] != c[0] {
		return 0, false
	}
	lag := t[1] - c[1]
	if lag < 0 {
		return 0, false
	}
	return lag, true
}
