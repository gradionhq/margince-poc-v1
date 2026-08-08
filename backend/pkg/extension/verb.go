// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package extension

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Verb is ONE governed operation an extension's contract fragment declares:
// the route, the method, the agent tool verb behind it, and the governance
// an operator resolves (tier, scope) plus the prose and schemas a client is
// shown.
//
// It exists because the CONTRACT is the surface. A unit's Go declaration says
// only which verbs it has behavior for (Tool is {Name, Handle}); everything a
// reader, a client, a reviewer or an operator needs to know about the
// operation lives in extensions/<name>/api/<contract>.yaml, is merged into the
// effective contract by gen-composition, and arrives back at the process as
// this value — re-emitted into the generated composition as LITERALS. That
// last part is the load-bearing one: the boot refusals in
// compose/extensiontools.go read Tier, RequestedScope and Description, and
// they must keep refusing in a bare binary with no repository on disk, so the
// composed program carries the declaration rather than reading the YAML back.
//
// A Verb is inert data, like every other part of this surface — no handle into
// the core, nothing to retain. The generator produces it; nothing an extension
// author writes in Go constructs one.
type Verb struct {
	// Unit is the declaring extension; it is derived from the fragment's
	// directory, never from the document, so a fragment cannot declare
	// operations in another unit's name.
	Unit Name
	// Contract is the base contract the fragment extended ("crm.yaml"). It is
	// the operation's source identity: the same operation id under a different
	// contract is a different published thing, so the manifest digest covers
	// it.
	Contract string
	// OperationID is the merged contract's operationId — the identity every
	// generated client, the docs and the manifest agree on.
	OperationID string
	// Route and Method are the HTTP surface the operation publishes. Route is
	// namespaced /v1/ext/<unit>/… (the composer enforces it) so no fragment can
	// squat a core path.
	Route  string
	Method string
	// Tool is the agent tool verb the operation backs (the fragment's
	// x-mcp-tool). Every extension operation backs one today: an operation
	// with no tool would publish a route this tier has no way to serve, so the
	// composer refuses it rather than declaring a route nothing can register.
	Tool string

	// Title, Description, Version, Tier, RequestedScope, InputSchema and
	// OutputSchema carry exactly what the narrowed Tool used to: see Tier,
	// Scope, and compose/extensiontools.go for what each one decides.
	Title          string
	Description    string
	Version        string
	Tier           Tier
	RequestedScope Scope
	InputSchema    json.RawMessage
	OutputSchema   json.RawMessage

	// RbacObject is the RBAC object name this operation gates on, `ext_<unit>_
	// <object>`, or empty when the unit owns no records (the common case: a
	// tool that reads nothing workspace-specific gates on its scope alone).
	// Declaring one registers it into the vocabulary at boot, which is what
	// lets a role document grant it and /me report the holder's grant — see
	// compose/extrbac.go.
	RbacObject string
}

// routeGrammar: an extension route is /v1/ext/<unit>/<segment>[/<segment>…],
// each segment lower-case [a-z0-9] with single hyphens or underscores. No
// path parameters ({id}) today — a served extension operation is a tool
// invocation, whose arguments are the request body, and admitting a template
// would mean this seam had to route and decode one.
var routeGrammar = regexp.MustCompile(`^/v1/ext/[a-z0-9]+(-[a-z0-9]+)*(/[a-z0-9]+([-_][a-z0-9]+)*)+$`)

// Validate enforces everything about a declared operation that must hold
// wherever it is read — the generator that derives it from the merged
// contract, and the boot that serves it. Both run this, so gen-time
// acceptance cannot drift from boot-time validation.
func (v Verb) Validate() error {
	if err := v.validateSurface(); err != nil {
		return err
	}
	return v.validateGovernance()
}

// validateSurface checks WHERE the operation lives: which unit declares it,
// which contract it came from, and the route and method it publishes. Split
// from validateGovernance because the two answer different questions and are
// wrong in different ways — a bad route is a namespace violation, a bad tier is
// an authority request nobody can honour.
func (v Verb) validateSurface() error {
	if err := v.Unit.Validate(); err != nil {
		return err
	}
	if v.Contract == "" {
		return fmt.Errorf("operation %q declares no source contract", v.OperationID)
	}
	if v.OperationID == "" {
		return fmt.Errorf("%s %s declares no operationId — the identity clients, docs and the manifest agree on", v.Method, v.Route)
	}
	if !routeGrammar.MatchString(v.Route) {
		return fmt.Errorf("route %q is not an extension route (/v1/ext/<unit>/<segment>, no path templates)", v.Route)
	}
	// The route must be the DECLARING unit's. gen-composition already refuses
	// a fragment targeting another namespace, but this value also arrives at
	// the boot through generated code, and a namespace check that lived only
	// in the generator would be a rule the served surface never re-applied.
	if prefix := "/v1/ext/" + string(v.Unit) + "/"; !strings.HasPrefix(v.Route, prefix) {
		return fmt.Errorf("extension %q declares route %q, which is outside its %s namespace", v.Unit, v.Route, prefix)
	}
	return validateMethod(v.Method)
}

// validateGovernance checks WHAT the operation asks for and what a client is
// told about it: the tool verb, the requested tier and scope, the renderable
// strings, the advertised schemas, and the RBAC object it gates on.
func (v Verb) validateGovernance() error {
	if !toolNameGrammar.MatchString(v.Tool) {
		return fmt.Errorf("operation %s declares tool verb %q, which is not a valid verb (lower snake_case, e.g. qualify_lead)", v.OperationID, v.Tool)
	}
	if v.Version == "" {
		return fmt.Errorf("operation %s declares no version", v.OperationID)
	}
	for _, check := range []struct {
		field, text string
	}{{"title", v.Title}, {"description", v.Description}} {
		if err := validateRenderedText(check.field, check.text); err != nil {
			return fmt.Errorf("operation %s: %w", v.OperationID, err)
		}
	}
	if err := v.Tier.Validate(); err != nil {
		return fmt.Errorf("operation %s: %w", v.OperationID, err)
	}
	if err := v.RequestedScope.Validate(); err != nil {
		return fmt.Errorf("operation %s: %w", v.OperationID, err)
	}
	for _, schema := range []struct {
		field string
		raw   json.RawMessage
	}{{"InputSchema", v.InputSchema}, {"OutputSchema", v.OutputSchema}} {
		if err := validateSchemaObject(schema.field, schema.raw); err != nil {
			return fmt.Errorf("operation %s: %w", v.OperationID, err)
		}
	}
	// The NAMESPACE only. The object-name grammar and the collision rules
	// against the core vocabulary belong to the module that owns that
	// vocabulary (identity's internal policy package), and restating them here
	// would be a second copy that could drift. What this surface owns is the
	// rule that a unit's identifiers are namespaced to the unit — the same rule
	// Name.Namespace() states for SQL identifiers.
	if v.RbacObject != "" {
		if want := NamespacePrefix + strings.ReplaceAll(string(v.Unit), "-", "_") + "_"; !strings.HasPrefix(v.RbacObject, want) {
			return fmt.Errorf("operation %s declares RBAC object %q, which is outside extension %q's %s namespace", v.OperationID, v.RbacObject, v.Unit, want)
		}
	}
	return nil
}

// validateMethod admits the closed set of methods this seam can mount. The
// list is short because a served extension operation is a tool invocation
// with a request body; GET and DELETE are absent rather than forgotten —
// neither carries one, so an operation declaring either would publish a route
// whose arguments never arrive.
func validateMethod(method string) error {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		return nil
	}
	return fmt.Errorf("method %q is not one an extension operation may declare (post, put, patch — a served operation is a tool invocation and carries a request body)", method)
}

// validateRenderedText checks one declared string a CLIENT renders verbatim —
// the display title, or the description a model selects by. Absent is allowed
// for both here; what each absence means is decided where the operation is
// served, not in the grammar.
//
// Present-but-blank is not allowed. The core registry refuses a whitespace-only
// title or description, and that refusal is a boot PANIC, so a string a unit
// author can see and fix has to fail here — at gen time, at the declaration's
// own position — rather than crashing the process that composes it. The framing
// and printability rules are Version's, for the same reason: the string is
// rendered by a client that did not write it.
func validateRenderedText(field, text string) error {
	if text == "" {
		return nil
	}
	// One check for two faults, because TrimSpace collapses them: a
	// whitespace-only string trims to empty, a framed one trims to something
	// shorter. Both are the same instruction to the author.
	if strings.TrimSpace(text) != text {
		return fmt.Errorf("%s %q is blank or carries surrounding whitespace — a client renders it verbatim; omit it rather than declaring an empty one", field, text)
	}
	// Before the rune check, not after: ranging a Go string decodes an invalid
	// byte to U+FFFD, which IS printable — so a malformed string would pass the
	// loop below and then be rendered by a client as replacement characters
	// the declaration never wrote.
	if !utf8.ValidString(text) {
		return fmt.Errorf("%s %q is not valid UTF-8", field, text)
	}
	for _, r := range text {
		if !unicode.IsPrint(r) {
			return fmt.Errorf("%s %q carries a non-printable character", field, text)
		}
	}
	return nil
}

// validateSchemaObject checks a declared schema, when present, is a JSON
// object rooted at `"type": "object"` — the shape MCP requires of a tool's
// input/output schema in tools/list. Absent (nil) is allowed: the served
// spec defaults a missing input schema to an empty object.
func validateSchemaObject(field string, raw json.RawMessage) error {
	if raw == nil {
		return nil
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("%s must be a JSON object", field)
	}
	var schemaType string
	if err := json.Unmarshal(doc["type"], &schemaType); err != nil || schemaType != "object" {
		return fmt.Errorf(`%s must be a JSON Schema object rooted at "type":"object"`, field)
	}
	return nil
}
