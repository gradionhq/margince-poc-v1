// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"go/ast"
	"sort"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// readIngress reads an Ingress field's slice literal into the manifest's
// declared sources.
//
// What it records is REACH in the other direction from a subscription: which
// providers a unit brings records in from, and which kinds it lands in the
// shared timeline. That matters to an operator before anything runs, because a
// landed record carries the declared system in its provenance forever — and it
// matters at the call, since the core stamps `source_system` from this list
// rather than from whatever the unit passes.
func (r *unitReader) readIngress(expr ast.Expr, file *ast.File) ([]ingressSource, error) {
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil, r.errAt(expr, "Ingress must be a slice literal")
	}
	ext := importAlias(file, extensionPkgPath)
	out := make([]ingressSource, 0, len(lit.Elts))
	seen := map[string]bool{}
	for _, elt := range lit.Elts {
		src, err := r.readIngressSource(elt, ext)
		if err != nil {
			return nil, err
		}
		if seen[src.System] {
			return nil, r.errAt(elt, "ingress source %q declared twice", src.System)
		}
		seen[src.System] = true
		out = append(out, src)
	}
	return out, nil
}

// readIngressSource reads one extension.IngressSource literal and validates it
// through the same published grammar the boot preflight runs (see
// extension.IngressSource.Validate), so gen-time acceptance cannot diverge from
// boot-time.
func (r *unitReader) readIngressSource(elt ast.Expr, ext string) (ingressSource, error) {
	lit, ok := elt.(*ast.CompositeLit)
	if !ok || (lit.Type != nil && !isSelector(lit.Type, ext, "IngressSource")) {
		return ingressSource{}, r.errAt(elt, "an Ingress entry must be an extension.IngressSource literal")
	}
	var src ingressSource
	for _, e := range lit.Elts {
		kv, ok := e.(*ast.KeyValueExpr)
		if !ok {
			return ingressSource{}, r.errAt(e, "IngressSource fields must be keyed")
		}
		k, ok := kv.Key.(*ast.Ident)
		if !ok {
			return ingressSource{}, r.errAt(kv.Key, "IngressSource fields must be keyed by name")
		}
		var err error
		switch k.Name {
		case "System":
			src.System, err = r.stringLit(kv.Value, "IngressSource.System")
		case "Lands":
			src.Lands, err = r.readRecordKinds(kv.Value, ext)
		default:
			err = r.errAt(kv, "IngressSource field %s is not derivable by this generator", k.Name)
		}
		if err != nil {
			return ingressSource{}, err
		}
	}
	if err := declaredIngressSource(src).Validate(); err != nil {
		return ingressSource{}, r.errPos(lit, "%v", err)
	}
	sort.Strings(src.Lands)
	return src, nil
}

// readRecordKinds reads the Lands slice literal. Each entry is a published
// constant (extension.KindActivity) or the string behind it, resolved the same
// way a secret's scope is — the manifest is derived without compiling the unit,
// so a constant is read from the source vocabulary rather than evaluated.
func (r *unitReader) readRecordKinds(expr ast.Expr, ext string) ([]string, error) {
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil, r.errAt(expr, "IngressSource.Lands must be a slice literal")
	}
	out := make([]string, 0, len(lit.Elts))
	for _, elt := range lit.Elts {
		kind, err := r.constValue(elt, ext)
		if err != nil {
			return nil, err
		}
		out = append(out, kind)
	}
	return out, nil
}

// declaredIngressSource rebuilds the published type from what was read, so the
// generator runs the unit's own grammar rather than a second copy of it.
func declaredIngressSource(src ingressSource) extension.IngressSource {
	lands := make([]extension.RecordKind, 0, len(src.Lands))
	for _, kind := range src.Lands {
		lands = append(lands, extension.RecordKind(kind))
	}
	return extension.IngressSource{System: src.System, Lands: lands}
}
