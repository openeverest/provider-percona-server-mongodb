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

	psmdbv1 "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	"github.com/percona/percona-server-mongodb-operator/pkg/util/numstr"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
)

// pitrSpanFieldOwner is the server-side field manager that owns
// spec.backup.pitr.oplogSpanMin, kept apart from the main apply's manager.
const pitrSpanFieldOwner = "provider-percona-server-mongodb-pitr-span"

// detachOplogSpanMin removes oplogSpanMin from the desired object and returns
// it. numstr.NumberString marshals as a JSON number only through a pointer
// receiver, which the unstructured conversion in Context.Apply does not use,
// so the value would be sent as a string and rejected by the operator CRD
// (type: number). It is written separately by syncOplogSpanMin.
func detachOplogSpanMin(psmdb *psmdbv1.PerconaServerMongoDB) numstr.NumberString {
	span := psmdb.Spec.Backup.PITR.OplogSpanMin
	psmdb.Spec.Backup.PITR.OplogSpanMin = ""
	return span
}

// syncOplogSpanMin writes spec.backup.pitr.oplogSpanMin as a JSON number
// (or clears it when span is empty) using a merge patch, and only when the
// live value differs. Must run after the main Apply created the object.
func syncOplogSpanMin(c *controller.Context, span numstr.NumberString) error {
	live := &psmdbv1.PerconaServerMongoDB{}
	if err := c.Get(live, c.Name()); err != nil {
		return err
	}

	var want any // nil clears the field in a merge patch
	if span != "" {
		want = json.Number(span)
	}
	// Unset and 0 are equivalent: the operator defaults the field to 0.
	liveSpan := live.Spec.Backup.PITR.OplogSpanMin
	if liveSpan.Float64() == span.Float64() {
		return nil
	}

	body, err := json.Marshal(map[string]any{
		"spec": map[string]any{
			"backup": map[string]any{
				"pitr": map[string]any{"oplogSpanMin": want},
			},
		},
	})
	if err != nil {
		return err
	}
	if err := c.Client().Patch(c.Context(), live,
		client.RawPatch(types.MergePatchType, body),
		client.FieldOwner(pitrSpanFieldOwner),
	); err != nil {
		return fmt.Errorf("patch oplogSpanMin: %w", err)
	}
	return nil
}
