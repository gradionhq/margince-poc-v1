// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"go/ast"
	"sort"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// readSubscriptions reads a Subscriptions field's slice literal into the
// manifest's declared listeners.
//
// What reaches the manifest is the NAME and the EVENT TYPES, never the handler:
// the behavior is a function, which no static reader can describe, and the
// thing an operator needs to see is which of the installation's facts a unit
// consumes. That list is the whole governance surface of a subscription — there
// is no tier and no scope to resolve — so a manifest carrying it says
// everything there is to say about the request.
func (r *unitReader) readSubscriptions(expr ast.Expr, file *ast.File) ([]subscriptionRequest, error) {
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil, r.errAt(expr, "Subscriptions must be a slice literal")
	}
	ext := importAlias(file, extensionPkgPath)
	out := make([]subscriptionRequest, 0, len(lit.Elts))
	seen := map[string]bool{}
	for _, elt := range lit.Elts {
		sub, err := r.readSubscription(elt, ext)
		if err != nil {
			return nil, err
		}
		if seen[sub.Name] {
			return nil, r.errAt(elt, "subscription %q declared twice", sub.Name)
		}
		seen[sub.Name] = true
		out = append(out, sub)
	}
	return out, nil
}

// readSubscription reads one extension.Subscription literal and validates it
// through the same published grammar the boot preflight runs (see
// extension.Subscription.Validate), so gen-time acceptance cannot diverge from
// boot-time.
//
// Handle is RECOGNIZED and skipped, exactly as a Tool's is: it is the behavior
// half, and a manifest describing a function would be describing something it
// cannot read.
func (r *unitReader) readSubscription(elt ast.Expr, ext string) (subscriptionRequest, error) {
	lit, ok := elt.(*ast.CompositeLit)
	if !ok || (lit.Type != nil && !isSelector(lit.Type, ext, "Subscription")) {
		return subscriptionRequest{}, r.errAt(elt, "a Subscriptions entry must be an extension.Subscription literal")
	}
	var sub subscriptionRequest
	for _, e := range lit.Elts {
		kv, ok := e.(*ast.KeyValueExpr)
		if !ok {
			return subscriptionRequest{}, r.errAt(e, "Subscription fields must be keyed")
		}
		k, ok := kv.Key.(*ast.Ident)
		if !ok {
			return subscriptionRequest{}, r.errAt(kv.Key, "Subscription fields must be keyed by name")
		}
		var err error
		switch k.Name {
		case "Name":
			sub.Name, err = r.stringLit(kv.Value, "Subscription.Name")
		case "Events":
			sub.Events, err = r.readEventTypes(kv.Value)
		case "Handle":
			// The behavior half. Present in the source, absent from the
			// manifest, by the same rule that keeps a Tool's handler out.
		default:
			err = r.errAt(kv, "Subscription field %s is not derivable by this generator", k.Name)
		}
		if err != nil {
			return subscriptionRequest{}, err
		}
	}
	// The published rules, run here rather than restated: the declaration half
	// of Validate is exactly what a static reader can decide, and whether
	// Handle is nil is left to the boot, which is the only place it is
	// observable at all.
	declared := extension.Subscription{Name: sub.Name, Events: sub.Events}
	if err := declared.ValidateDeclaration(); err != nil {
		return subscriptionRequest{}, r.errPos(lit, "%v", err)
	}
	sort.Strings(sub.Events)
	return sub, nil
}

// readEventTypes reads the Events slice literal — plain string literals, like
// every other value this generator derives, because the manifest is read
// without compiling or executing the unit.
func (r *unitReader) readEventTypes(expr ast.Expr) ([]string, error) {
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil, r.errAt(expr, "Subscription.Events must be a slice literal")
	}
	out := make([]string, 0, len(lit.Elts))
	for _, elt := range lit.Elts {
		typ, err := r.stringLit(elt, "Subscription.Events entry")
		if err != nil {
			return nil, err
		}
		out = append(out, typ)
	}
	return out, nil
}
