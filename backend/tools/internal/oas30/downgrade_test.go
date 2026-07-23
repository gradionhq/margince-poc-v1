// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package oas30

import (
	"strings"
	"testing"
)

// TestDowngradeTransforms proves the four faithful 3.1 -> 3.0.3 rewrites the
// generator relies on: version, the [T, null] union, schema-level plural
// examples, and const -> single-value enum.
func TestDowngradeTransforms(t *testing.T) {
	src := `
openapi: 3.1.0
components:
  schemas:
    Thing:
      type: object
      properties:
        nick:
          type: [string, "null"]
        count:
          type: integer
          const: 30000
        note:
          type: string
          examples:
            - hello
`
	out, err := Bytes([]byte(src))
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	got := string(out)
	for _, want := range []string{"3.0.3", "nullable: true", "enum:", "30000", "example: hello"} {
		if !strings.Contains(got, want) {
			t.Errorf("downgraded doc missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "const:") {
		t.Errorf("const must be rewritten to enum, still present:\n%s", got)
	}
	if strings.Contains(got, "3.1.0") {
		t.Errorf("openapi version must be downgraded, still 3.1.0:\n%s", got)
	}
}

// TestDowngradeFailsLoudlyOnUnsupportedKeyword proves a 3.1-only construct
// with no 3.0 equivalent errors rather than silently passing into a
// 3.0.3-labeled doc.
func TestDowngradeFailsLoudlyOnUnsupportedKeyword(t *testing.T) {
	src := `
openapi: 3.1.0
components:
  schemas:
    Thing:
      type: object
      properties:
        tuple:
          type: array
          prefixItems:
            - type: string
`
	if _, err := Bytes([]byte(src)); err == nil {
		t.Fatal("prefixItems (3.1-only) must fail the downgrade, not pass silently")
	}
}

// TestDowngradeLeavesExampleDataOpaque proves the walker does NOT interpret a
// data member named like a schema keyword: an example object carrying "type",
// "openapi", or "const" is data, not a keyword to rewrite (the example-
// corruption bug). It must round-trip untouched and must not error.
func TestDowngradeLeavesExampleDataOpaque(t *testing.T) {
	src := `
openapi: 3.1.0
components:
  schemas:
    Thing:
      type: object
      example:
        type: widget
        openapi: "3.1"
        const: keep-me
`
	out, err := Bytes([]byte(src))
	if err != nil {
		t.Fatalf("Bytes: example data must not trip keyword handling: %v", err)
	}
	got := string(out)
	// The example's data members survive verbatim (not rewritten to enum, not
	// bumped to 3.0.3, not flagged unsupported).
	for _, want := range []string{"type: widget", `openapi: "3.1"`, "const: keep-me"} {
		if !strings.Contains(got, want) {
			t.Errorf("example data member %q was corrupted:\n%s", want, got)
		}
	}
}

// TestDowngradeDoesNotFlagPropertyNames proves a property legitimately NAMED
// like a 3.1 keyword (e.g. "const") is not mistaken for the keyword.
func TestDowngradeDoesNotFlagPropertyNames(t *testing.T) {
	src := `
openapi: 3.1.0
components:
  schemas:
    Thing:
      type: object
      properties:
        const:
          type: string
        if:
          type: integer
`
	if _, err := Bytes([]byte(src)); err != nil {
		t.Fatalf("property names that look like keywords must not fail the downgrade: %v", err)
	}
}
