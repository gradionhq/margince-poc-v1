// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"go/ast"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// readSecrets reads a Secrets field's slice literal into the manifest's
// secret requests. Like Tools, this is inert data an operator resolves —
// declaring a key mints nothing and reads nothing (see
// extension.SecretsRequest) — so the reader accepts the same literal-only
// shape as everything else in the declaration.
func (r *unitReader) readSecrets(expr ast.Expr, file *ast.File) ([]secretsRequest, error) {
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil, r.errAt(expr, "Secrets must be a slice literal")
	}
	ext := importAlias(file, extensionPkgPath)
	out := make([]secretsRequest, 0, len(lit.Elts))
	seen := map[string]bool{}
	// The scope the first entry asked for, and the key that asked. Every later
	// entry must agree: a unit's secret scope is what the SPA derives its
	// settings placement from (unitSecretScope), and a unit holding both an
	// installation credential and a per-member one has no answer to "whose
	// settings page is this". Refused here rather than resolved by a
	// tie-break, because either tie-break hides half the unit from whoever
	// holds the other half.
	var scope, scopedBy string
	for _, elt := range lit.Elts {
		sr, err := r.readSecret(elt, ext)
		if err != nil {
			return nil, err
		}
		dedupe := sr.Scope + "/" + sr.Key
		if seen[dedupe] {
			return nil, r.errAt(elt, "secret %q declared twice in scope %q", sr.Key, sr.Scope)
		}
		if scope != "" && sr.Scope != scope {
			return nil, r.errAt(elt, "secret %q requests scope %q but %q already requested %q — a unit declares ONE scope, because its settings entry derives from it; split the unit, or move the secret into the scope the other one uses", sr.Key, sr.Scope, scopedBy, scope)
		}
		if scope == "" {
			scope, scopedBy = sr.Scope, sr.Key
		}
		seen[dedupe] = true
		out = append(out, sr)
	}
	return out, nil
}

// readSecret reads one extension.SecretsRequest literal and validates it
// through the same published grammar the boot preflight runs (see
// extension.SecretsRequest.Validate), so gen-time acceptance cannot diverge
// from boot-time.
func (r *unitReader) readSecret(elt ast.Expr, ext string) (secretsRequest, error) {
	lit, ok := elt.(*ast.CompositeLit)
	if !ok || (lit.Type != nil && !isSelector(lit.Type, ext, "SecretsRequest")) {
		return secretsRequest{}, r.errAt(elt, "a Secrets entry must be an extension.SecretsRequest literal")
	}
	var key, scope string
	for _, e := range lit.Elts {
		kv, ok := e.(*ast.KeyValueExpr)
		if !ok {
			return secretsRequest{}, r.errAt(e, "SecretsRequest fields must be keyed")
		}
		k, ok := kv.Key.(*ast.Ident)
		if !ok {
			return secretsRequest{}, r.errAt(kv.Key, "SecretsRequest fields must be keyed by name")
		}
		var err error
		switch k.Name {
		case "Key":
			key, err = r.stringLit(kv.Value, "SecretsRequest.Key")
		case "Scope":
			scope, err = r.constValue(kv.Value, ext)
		default:
			err = r.errAt(kv, "SecretsRequest field %s is not derivable by this generator", k.Name)
		}
		if err != nil {
			return secretsRequest{}, err
		}
	}
	declared := extension.SecretsRequest{Key: key, Scope: extension.SecretScope(scope)}
	if err := declared.Validate(); err != nil {
		return secretsRequest{}, r.errPos(lit, "%v", err)
	}
	return secretsRequest{Key: key, Scope: scope}, nil
}
