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
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	templatev1alpha1 "github.com/openeverest/provider-percona-server-mongodb/api/v1alpha1"
	"github.com/openeverest/provider-percona-server-mongodb/definition/components"
	"github.com/openeverest/provider-percona-server-mongodb/internal/common"
)

const (
	// AnnotationTemplateName is the annotation key for the InstanceTemplate name
	// that an Instance references.
	AnnotationTemplateName = "psmdb.openeverest.io/template-name"

	// templateNamePath is the field index path for looking up Instances
	// by their referenced InstanceTemplate name (from annotation).
	templateNamePath = ".metadata.annotations.psmdb.openeverest.io/template-name"
)

// resolveTemplate looks up the InstanceTemplate referenced by the Instance's
// annotation. Returns nil without error if no template is referenced.
func resolveTemplate(c *controller.Context) (*templatev1alpha1.InstanceTemplate, error) {
	name := c.Instance().GetAnnotations()[AnnotationTemplateName]
	if name == "" {
		return nil, nil
	}

	tpl := &templatev1alpha1.InstanceTemplate{}
	if err := c.Get(tpl, name); err != nil {
		return nil, fmt.Errorf("get InstanceTemplate %q: %w", name, err)
	}

	return tpl, nil
}

// resolveSplitHorizon returns the effective SplitHorizonSpec by merging
// the InstanceTemplate defaults with instance-level overrides from the
// "splitHorizon" component's CustomSpec.
//
// Precedence: instance-level fields override template defaults.
// Both paths use: spec.components.splitHorizon.customSpec
//
// Returns nil if neither template nor instance defines split horizon.
func resolveSplitHorizon(c *controller.Context) (*templatev1alpha1.SplitHorizonSpec, error) {
	// Start with template defaults (if any).
	var base *templatev1alpha1.SplitHorizonSpec

	tpl, err := resolveTemplate(c)
	if err != nil {
		return nil, err
	}

	if tpl != nil {
		comp, ok := tpl.Spec.Components[common.ComponentSplitHorizon]
		if ok && comp.CustomSpec != nil {
			base = comp.CustomSpec.DeepCopy()
		}
	}

	// Read instance-level split horizon from the component's CustomSpec.
	shComp, ok := c.Instance().Spec.Components[common.ComponentSplitHorizon]
	if ok {
		var instanceSH components.SplitHorizonCustomSpec
		if c.TryDecodeComponentCustomSpec(shComp, &instanceSH) {
			if base == nil {
				base = &templatev1alpha1.SplitHorizonSpec{}
			}
			// Instance-level fields take precedence over template defaults.
			if instanceSH.BaseDomainNameSuffix != "" {
				base.BaseDomainNameSuffix = instanceSH.BaseDomainNameSuffix
			}
			if instanceSH.TLSSecretName != "" {
				if base.TLS == nil {
					base.TLS = &templatev1alpha1.SplitHorizonTLS{}
				}
				base.TLS.SecretName = instanceSH.TLSSecretName
			}
		}
	}

	return base, nil
}

// enqueueTemplate returns a handler that enqueues reconcile requests for all
// Instances that reference the given InstanceTemplate (via annotation).
func enqueueTemplate(p *PSMDBProvider) func(ctx context.Context, obj client.Object) []reconcile.Request {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		tpl, ok := obj.(*templatev1alpha1.InstanceTemplate)
		if !ok {
			return nil
		}

		c := p.client
		if c == nil {
			return nil
		}

		instanceList := &corev1alpha1.InstanceList{}
		listOpts := &client.ListOptions{
			FieldSelector: fields.OneTermEqualSelector(templateNamePath, tpl.GetName()),
			Namespace:     tpl.GetNamespace(),
		}

		if err := c.List(ctx, instanceList, listOpts); err != nil {
			return nil
		}

		requests := make([]reconcile.Request, len(instanceList.Items))
		for i, item := range instanceList.Items {
			requests[i] = reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      item.GetName(),
					Namespace: item.GetNamespace(),
				},
			}
		}

		return requests
	}
}

// extractTemplateName extracts the InstanceTemplate name from the Instance's
// annotation for field indexing.
func extractTemplateName(obj client.Object) []string {
	in, ok := obj.(*corev1alpha1.Instance)
	if !ok {
		return nil
	}

	name := in.GetAnnotations()[AnnotationTemplateName]
	if name == "" {
		return nil
	}

	return []string{name}
}

// templatePredicate returns a predicate that filters events for InstanceTemplate resources.
func templatePredicate() predicate.Funcs {
	return predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool {
			return false
		},
		UpdateFunc: func(_ event.UpdateEvent) bool {
			return true
		},
		DeleteFunc: func(_ event.DeleteEvent) bool {
			return true
		},
		GenericFunc: func(_ event.GenericEvent) bool {
			return false
		},
	}
}
