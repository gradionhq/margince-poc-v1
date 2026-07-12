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
	"os"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const idempotencyMapSource = "internal/compose/idempotency.go"

// idempotencyExemptions names the contract operations that declare the
// IdempotencyKey parameter but are deliberately NOT claimed by the
// middleware — each entry carries the recorded reason the promise cannot
// be honored yet. An exemption never outlives its cause: an entry the
// contract stops declaring, or one that shows up in the runtime map
// anyway, fails the gate.
var idempotencyExemptions = map[string]string{
	"POST /v1/public/booking/{host_slug}": "anonymous edge: every visitor shares the" +
		" system:public_booking principal, so the middleware's per-principal claim scope" +
		" cannot tell callers apart — one visitor's key + body would replay another's" +
		" recorded confirmation; the anonymous surface needs its own dedupe scope" +
		" (workspace + request digest) before the promise can be honored, and the slot's" +
		" natural key refuses duplicate bookings meanwhile",
}

// contractOperation is the slice of one crm.yaml operation this gate
// reads: the parameter list, each entry either a $ref or an inline
// declaration. Decoding through these types (rather than untyped map
// assertions) makes any unexpected contract shape a loud failure.
type contractOperation struct {
	Parameters []struct {
		Ref  string `yaml:"$ref"`
		Name string `yaml:"name"`
	} `yaml:"parameters"`
}

// idempotencyKeyDeclarations enumerates every contract operation that
// declares the IdempotencyKey parameter, keyed like the runtime map:
// "METHOD /v1<path>" — the /v1 prefix lives on the server URL in
// crm.yaml but on the chi route pattern the middleware matches.
func idempotencyKeyDeclarations(t *testing.T) map[string]bool {
	t.Helper()
	src, err := os.ReadFile("api/crm.yaml")
	if err != nil {
		t.Fatalf("reading api/crm.yaml: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(src, &doc); err != nil {
		t.Fatalf("parsing api/crm.yaml: %v", err)
	}
	if len(doc.Paths) == 0 {
		t.Fatal("api/crm.yaml has no top-level paths map — the contract failed to parse as expected")
	}
	declared := map[string]bool{}
	for path, item := range doc.Paths {
		for key, node := range item {
			// A path item mixes operations with non-operation keys
			// (summary, description, shared parameters) — only the
			// httpMethods vocabulary carries an operation.
			if !httpMethods[key] {
				continue
			}
			var op contractOperation
			if err := node.Decode(&op); err != nil {
				t.Fatalf("api/crm.yaml %s %s: the operation does not decode as an OpenAPI operation: %v", key, path, err)
			}
			for _, param := range op.Parameters {
				if param.Ref == "#/components/parameters/IdempotencyKey" {
					declared[strings.ToUpper(key)+" /v1"+path] = true
				}
			}
		}
	}
	return declared
}

// mappedIdempotentOperations reads the idempotentOperations map literal
// out of the compose source (the rbacgate AST technique — the root
// component walks the contract, it never imports compose). The decl
// walk skips everything that is not the map being sought; once found,
// any element that is not a plain "key": value entry is a loud failure,
// never a silently dropped operation.
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
					t.Fatalf("%s: idempotentOperations holds a non key/value element — teach this extractor the new shape", idempotencyMapSource)
				}
				keyLit, ok := kv.Key.(*ast.BasicLit)
				if !ok || keyLit.Kind != token.STRING {
					t.Fatalf("%s: idempotentOperations has a non-string-literal key — teach this extractor the new shape", idempotencyMapSource)
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

	for op := range idempotencyExemptions {
		if !declared[op] {
			t.Errorf("idempotencyExemptions names %s but the contract no longer declares IdempotencyKey on it — delete the stale exemption", op)
		}
		if mapped[op] {
			t.Errorf("%s is exempted yet present in idempotentOperations — either honor the promise and delete the exemption, or keep the operation out of the map", op)
		}
	}
	for op := range declared {
		if !mapped[op] && idempotencyExemptions[op] == "" {
			t.Errorf("%s declares the IdempotencyKey parameter but is missing from idempotentOperations — a retried request re-executes instead of replaying", op)
		}
	}
	for op := range mapped {
		if !declared[op] {
			t.Errorf("idempotentOperations maps %s but the contract does not declare IdempotencyKey on it — the map claims keys for a promise the contract never made", op)
		}
	}
}
