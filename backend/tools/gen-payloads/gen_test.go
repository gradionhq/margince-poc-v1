// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"strings"
	"testing"
)

// fixtureSpec is a minimal components-only OpenAPI doc: one plain schema,
// one carrying the x-event-type / x-entity-type extensions the generator
// projects into EventType()/EntityType() methods plus an explicit x-version,
// and one carrying the event extensions but NO x-version (defaults to 1).
const fixtureSpec = `openapi: 3.1.0
info:
  title: gen-payloads fixture
  version: "1"
components:
  schemas:
    PlainThing:
      type: object
      properties:
        name:
          type: string
    WebhookPayloadX:
      type: object
      x-event-type: x.happened
      x-entity-type: widget
      x-version: 2
      properties:
        id:
          type: string
    WebhookPayloadY:
      type: object
      x-event-type: y.happened
      x-entity-type: widget2
      properties:
        id:
          type: string
`

// nilPayloadFixtureSpec is a standalone fixture (kept separate from
// fixtureSpec so its longer event-type name never perturbs gofmt's map-
// literal column alignment in the other tests' expected output): one
// event-tagged schema with zero properties, the shape that used to
// generate as oapi-codegen's default map[string]interface{} alias.
const nilPayloadFixtureSpec = `openapi: 3.1.0
info:
  title: gen-payloads nil-payload fixture
  version: "1"
components:
  schemas:
    WebhookPayloadNil:
      type: object
      x-event-type: nil.happened
      x-entity-type: widget3
      additionalProperties: false
`

func TestGenerateSourceEmitsTypesAndEventMethods(t *testing.T) {
	src, err := generateSource([]byte(fixtureSpec), "testpkg")
	if err != nil {
		t.Fatalf("generateSource: %v", err)
	}

	want := []string{
		"package testpkg",
		"type PlainThing struct",
		"type WebhookPayloadX struct",
		`func (WebhookPayloadX) EventType() string { return "x.happened" }`,
		`func (WebhookPayloadX) EntityType() string { return "widget" }`,
	}
	for _, w := range want {
		if !strings.Contains(src, w) {
			t.Errorf("generated source missing %q\n---\n%s", w, src)
		}
	}
}

// A schema without the event extensions gets no methods appended — only the
// struct. Guards against blanket method emission.
func TestGenerateSourceOmitsMethodsForPlainSchema(t *testing.T) {
	src, err := generateSource([]byte(fixtureSpec), "testpkg")
	if err != nil {
		t.Fatalf("generateSource: %v", err)
	}
	if strings.Contains(src, "func (PlainThing) EventType()") {
		t.Errorf("plain schema must not get an EventType method\n---\n%s", src)
	}
}

// TestGenerateSourceEmitsWebhookPayloadVersions proves the generated
// WebhookPayloadVersions map carries one entry per event-tagged schema: the
// explicit x-version for WebhookPayloadX, and the default-to-1 for
// WebhookPayloadY (which declares no x-version at all) — the single
// generated source of truth the coverage and version gates read.
func TestGenerateSourceEmitsWebhookPayloadVersions(t *testing.T) {
	src, err := generateSource([]byte(fixtureSpec), "testpkg")
	if err != nil {
		t.Fatalf("generateSource: %v", err)
	}
	want := []string{
		"var WebhookPayloadVersions = map[string]int{",
		`"x.happened": 2,`,
		`"y.happened": 1,`,
	}
	for _, w := range want {
		if !strings.Contains(src, w) {
			t.Errorf("generated source missing %q\n---\n%s", w, src)
		}
	}
	if strings.Contains(src, `"PlainThing"`) {
		t.Errorf("WebhookPayloadVersions must not carry a plain (non-event) schema\n---\n%s", src)
	}
}

// TestGenerateSourceStructifiesEmptyEventPayload proves a nil-payload event
// schema (event-tagged, zero properties) generates as a real empty struct,
// never oapi-codegen's default map[string]interface{} alias for an empty
// object — a type alias to a builtin map cannot carry the EventType()/
// EntityType() methods this generator projects below it, so the map shape
// would fail to compile the moment those methods were appended.
func TestGenerateSourceStructifiesEmptyEventPayload(t *testing.T) {
	src, err := generateSource([]byte(nilPayloadFixtureSpec), "testpkg")
	if err != nil {
		t.Fatalf("generateSource: %v", err)
	}
	if !strings.Contains(src, "type WebhookPayloadNil struct{}") {
		t.Errorf("generated source missing the structified empty type\n---\n%s", src)
	}
	if strings.Contains(src, "WebhookPayloadNil = map[string]interface{}") {
		t.Errorf("generated source still aliases the nil-payload event to a map\n---\n%s", src)
	}
	for _, w := range []string{
		`func (WebhookPayloadNil) EventType() string { return "nil.happened" }`,
		`func (WebhookPayloadNil) EntityType() string { return "widget3" }`,
	} {
		if !strings.Contains(src, w) {
			t.Errorf("generated source missing %q\n---\n%s", w, src)
		}
	}
}
