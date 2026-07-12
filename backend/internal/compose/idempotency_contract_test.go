// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The idempotency allowlist as a fitness function: idempotentOperations
// is hand-maintained, and the contract is the authority on which
// operations promise Idempotency-Key retry safety. A declared operation
// missing from the map silently drops the promise (a retried create
// duplicates the row); a mapped operation the contract no longer
// declares claims keys for a promise nobody made. This walks the
// authoritative api/crm.yaml (the formulafieldscope_test.go pattern), so
// an operation added tomorrow is covered the moment its parameter list
// gains the IdempotencyKey $ref.

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// contractIdempotentOperations enumerates every contract operation that
// declares the IdempotencyKey parameter, keyed exactly like
// idempotentOperations: "METHOD /v1<path>" — the /v1 prefix lives on the
// server URL in crm.yaml but on the chi route pattern at runtime.
func contractIdempotentOperations(t *testing.T) map[string]bool {
	t.Helper()
	src, err := os.ReadFile("../../api/crm.yaml")
	if err != nil {
		t.Fatalf("reading api/crm.yaml: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(src, &doc); err != nil {
		t.Fatalf("parsing api/crm.yaml: %v", err)
	}
	paths, ok := doc["paths"].(map[string]any)
	if !ok {
		t.Fatal("api/crm.yaml has no top-level paths map — the contract failed to parse as expected")
	}

	// The OpenAPI path-item keys that carry an operation; the others
	// (parameters, summary, …) are path-level metadata.
	httpMethods := map[string]bool{
		"get": true, "put": true, "post": true, "delete": true,
		"options": true, "head": true, "patch": true, "trace": true,
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

func TestIdempotentOperationsMirrorTheContract(t *testing.T) {
	declared := contractIdempotentOperations(t)
	// Vacuous-pass guard: the contract declares dozens of idempotent
	// operations; finding almost none means the walk stopped seeing the
	// document, not that the API shrank.
	if len(declared) < 20 {
		t.Fatalf("found only %d IdempotencyKey declarations in api/crm.yaml — the contract walk no longer sees the schema", len(declared))
	}

	for op := range declared {
		if !idempotentOperations[op] {
			t.Errorf("%s declares the IdempotencyKey parameter but is missing from idempotentOperations — a retried request re-executes instead of replaying", op)
		}
	}
	for op := range idempotentOperations {
		if !declared[op] {
			t.Errorf("idempotentOperations maps %s but the contract does not declare IdempotencyKey on it — the map claims keys for a promise the contract never made", op)
		}
	}
}
