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

// TestNullEnumMemberIsDropped proves the null member of a 3.1 nullable enum
// does not survive into the 3.0.3 document.
//
// It used to. oapi-codegen renders an enum member with %v, so a YAML null
// became the four-character string "<nil>" and the identifier sanitiser turned
// `<` into `LessThan` — ActivityMeetingStatusLessThannil = "<nil>", 84 such
// constants across 42 enums in two generated files. Every generated Valid()
// then answered true for a value the database CHECK refuses, so the one method
// that looks like a guard was not one.
//
// Nullability is not lost: rewriteTypeUnion emits `nullable: true` for the
// same schema, which is how 3.0 spells it.
func TestNullEnumMemberIsDropped(t *testing.T) {
	src := `
openapi: 3.1.0
components:
  schemas:
    Activity:
      type: object
      properties:
        meeting_status:
          type: [string, "null"]
          enum: [null, booked, held, no_show, canceled]
`
	out, err := Bytes([]byte(src))
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	got := string(out)
	if strings.Contains(got, "null\n") && strings.Contains(got, "- null") {
		t.Errorf("a null enum member survived the downgrade:\n%s", got)
	}
	if !strings.Contains(got, "nullable: true") {
		t.Errorf("nullability was lost with the null member — 3.0 spells it `nullable: true`:\n%s", got)
	}
	for _, want := range []string{"booked", "held", "no_show", "canceled"} {
		if !strings.Contains(got, want) {
			t.Errorf("real enum member %q was dropped:\n%s", want, got)
		}
	}
}

// TestEnumMembersAreNotDescendedInto keeps the example-corruption guarantee
// while `enum` is handled in rewriteKeyword rather than left opaque: a member
// spelled like a schema keyword is DATA and must pass through unrewritten.
func TestEnumMembersAreNotDescendedInto(t *testing.T) {
	src := `
openapi: 3.1.0
components:
  schemas:
    Thing:
      type: object
      properties:
        kind:
          type: string
          enum: [type, openapi, examples]
`
	out, err := Bytes([]byte(src))
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	got := string(out)
	for _, want := range []string{"type", "openapi", "examples"} {
		if !strings.Contains(got, want) {
			t.Errorf("enum member %q was rewritten as a schema keyword:\n%s", want, got)
		}
	}
	// The document version is rewritten; a member spelled "openapi" is not.
	if strings.Contains(got, "- 3.0.3") {
		t.Errorf("an enum member was rewritten as the document version:\n%s", got)
	}
}
