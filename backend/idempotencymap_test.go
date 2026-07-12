// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The idempotency allowlist as a fitness function: the contract is the
// authority on which operations promise Idempotency-Key retry safety,
// and internal/compose's hand-maintained idempotentOperations map must
// mirror it exactly. A declared operation missing from the map silently
// drops the promise (a retried create duplicates the row); a mapped
// operation the contract no longer declares claims keys for a promise
// nobody made. Like the other root fitness tests, this walks the
// authoritative api/crm.yaml (never the generated Go) and reads the map
// keys out of the compose source, so an operation added tomorrow is
// covered the moment its parameter list gains the IdempotencyKey $ref.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

const idempotencyMapSource = "internal/compose/idempotency.go"

// idempotencyKeyDeclarations enumerates every contract operation that
// declares the IdempotencyKey parameter, keyed like the runtime map:
// "METHOD /v1<path>" — the /v1 prefix lives on the server URL in
// crm.yaml but on the chi route pattern the middleware matches.
func idempotencyKeyDeclarations(t *testing.T) map[string]bool {
	t.Helper()
	doc := loadContract(t)
	paths, ok := doc["paths"].(map[string]any)
	if !ok {
		t.Fatal("api/crm.yaml has no top-level paths map — the contract failed to parse as expected")
	}
	declared := map[string]bool{}
	for path, itemAny := range paths {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		for method, opAny := range item {
			if !httpMethods[method] {
				continue
			}
			op, ok := opAny.(map[string]any)
			if !ok {
				continue
			}
			params, ok := op["parameters"].([]any)
			if !ok {
				continue
			}
			for _, paramAny := range params {
				param, ok := paramAny.(map[string]any)
				if !ok {
					continue
				}
				if param["$ref"] == "#/components/parameters/IdempotencyKey" {
					declared[strings.ToUpper(method)+" /v1"+path] = true
				}
			}
		}
	}
	return declared
}

// mappedIdempotentOperations reads the idempotentOperations map literal
// out of the compose source (the rbacgate AST technique — the root
// component walks the contract, it never imports compose).
func mappedIdempotentOperations(t *testing.T) map[string]bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), idempotencyMapSource, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", idempotencyMapSource, err)
	}
	mapped := map[string]bool{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Names) != 1 || value.Names[0].Name != "idempotentOperations" || len(value.Values) != 1 {
				continue
			}
			lit, ok := value.Values[0].(*ast.CompositeLit)
			if !ok {
				t.Fatalf("%s: idempotentOperations is no longer a composite map literal — teach this extractor the new shape", idempotencyMapSource)
			}
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				keyLit, ok := kv.Key.(*ast.BasicLit)
				if !ok || keyLit.Kind != token.STRING {
					continue
				}
				key, err := strconv.Unquote(keyLit.Value)
				if err != nil {
					t.Fatalf("%s: unquoting map key %s: %v", idempotencyMapSource, keyLit.Value, err)
				}
				mapped[key] = true
			}
		}
	}
	return mapped
}

func TestIdempotentOperationsMirrorTheContract(t *testing.T) {
	declared := idempotencyKeyDeclarations(t)
	mapped := mappedIdempotentOperations(t)
	// Vacuous-pass guards: both sides carry dozens of operations; finding
	// almost none means a walk stopped seeing its source, not that the
	// API shrank.
	if len(declared) < 20 {
		t.Fatalf("found only %d IdempotencyKey declarations in api/crm.yaml — the contract walk no longer sees the schema", len(declared))
	}
	if len(mapped) < 20 {
		t.Fatalf("found only %d entries in idempotentOperations — the source extractor no longer sees the map", len(mapped))
	}

	for op := range declared {
		if !mapped[op] {
			t.Errorf("%s declares the IdempotencyKey parameter but is missing from idempotentOperations — a retried request re-executes instead of replaying", op)
		}
	}
	for op := range mapped {
		if !declared[op] {
			t.Errorf("idempotentOperations maps %s but the contract does not declare IdempotencyKey on it — the map claims keys for a promise the contract never made", op)
		}
	}
}
