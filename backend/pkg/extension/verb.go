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
	// Route is the operation's path AS THE CONTRACT SPELLS IT — relative to the
	// contract's own `servers` url, which ends in /v1. So it reads
	// /ext/<unit>/…, exactly like every core path (/me, /auth/login), and is a
	// verbatim copy of the merged document's `paths` key rather than anything
	// derived from it. It is namespaced to the declaring unit (the composer
	// enforces it) so no fragment can squat a core path.
	//
	// This is NOT the path a server mounts. For that, see ServedPath, which is
	// the one place the base path is prepended. The field holds the contract
	// spelling because that is the spelling that can be CHECKED: it appears in
	// the published document, so it can be compared to it, and the manifest
	// digest pins something an operator can find by reading the contract.
	Route string
	// Method is the HTTP method, upper-case.
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
	// compose/extrbac.go — and it is REQUIRED for the grant on this
	// invocation before the handler runs.
	RbacObject string

	// RbacAction is the verb the grant must carry, and it is declared rather
	// than derived because nothing available here could derive it. The
	// requested Scope is a Passport verb CLASS (read/draft/write/send/enrich)
	// and the RBAC action is one of four object verbs; `write` alone cannot
	// say whether an operation creates, updates or deletes, and a check that
	// accepted any of the three would admit a principal granted delete to a
	// route that creates.
	//
	// It is required exactly when RbacObject is present and refused when it is
	// absent: an action with no object names no grant, and an object with no
	// action would have to be enforced with a guessed verb.
	RbacAction RbacAction
}

// RbacAction is one of the four object-level verbs a grant carries. The values
// mirror the core vocabulary (`principal.Action`, and the contract's own
// RbacAction enum) that the serving side maps them to, on the same footing as
// Tier and Scope: a published surface cannot import the kernel, so what it
// publishes is the same closed set under its own name.
type RbacAction string

const (
	// RbacCreate is the grant a route that brings a record into existence needs.
	RbacCreate RbacAction = "create"
	// RbacRead is the grant a route that only reads needs.
	RbacRead RbacAction = "read"
	// RbacUpdate is the grant a route that mutates an existing record needs.
	RbacUpdate RbacAction = "update"
	// RbacDelete is the grant a route that removes a record needs.
	RbacDelete RbacAction = "delete"
)

// Validate refuses an action outside the four. An unrecognised verb would
// satisfy no grant any role document can hold, so the operation would deny
// every principal forever while looking configured.
func (a RbacAction) Validate() error {
	switch a {
	case RbacCreate, RbacRead, RbacUpdate, RbacDelete:
		return nil
	}
	return fmt.Errorf("RBAC action %q is not one of the four object verbs (create, read, update, delete)", string(a))
}

// APIBasePath is the prefix the contract's `servers` url already carries
// (https://…/v1), spelled once. A contract path is written relative to it —
// core writes /me, an extension writes /ext/<unit>/… — and a server that mounts
// paths on a bare host has to put it back. ServedPath is the only place that
// happens.
const APIBasePath = "/v1"

// routeGrammar: an extension route as the CONTRACT spells it —
// /ext/<unit>/<segment>[/<segment>…], each segment lower-case [a-z0-9] with
// single hyphens or underscores. No path parameters ({id}) today — a served
// extension operation is a tool invocation, whose arguments are the request
// body, and admitting a template would mean this seam had to route and decode
// one.
//
// Anchored at /ext, which also makes the one mistake this convention exists to
// prevent a loud refusal rather than a silent double prefix: a fragment that
// writes the full /v1/ext/… — believing it is spelling an absolute URL path —
// does not match, and is told so at generation time. The merged document would
// otherwise publish https://host/v1/v1/ext/… to every generated client, every
// SDK and the docs.
var routeGrammar = regexp.MustCompile(`^/ext/[a-z0-9]+(-[a-z0-9]+)*(/[a-z0-9]+([-_][a-z0-9]+)*)+$`)

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
		return fmt.Errorf("route %q is not an extension route — a contract path is relative to the contract's own servers url (which already ends in %s), so an extension writes /ext/<unit>/<segment>, no path templates", v.Route, APIBasePath)
	}
	// The route must be the DECLARING unit's. gen-composition already refuses
	// a fragment targeting another namespace, but this value also arrives at
	// the boot through generated code, and a namespace check that lived only
	// in the generator would be a rule the served surface never re-applied.
	if prefix := RoutePrefix + string(v.Unit) + "/"; !strings.HasPrefix(v.Route, prefix) {
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
	// Version joins title and description, because it is rendered like them:
	// the composer emits it as the tool's `schema_version`, so a value carrying
	// a control character or invalid UTF-8 reaches a client verbatim — as
	// replacement characters, or as a line break inside an identifier. Only the
	// EMPTINESS of it was checked before, which is the one fault an author
	// notices anyway.
	for _, check := range []struct {
		field, text string
	}{{"title", v.Title}, {"description", v.Description}, {"version", v.Version}} {
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
	// AFTER the scope and the schemas, because both rules read them: the pairing
	// compares the method against a scope that has to be in the vocabulary first,
	// and the query check walks a schema that has to be an object first. Ordered
	// the other way, an author with a typo'd scope would be told about their
	// method instead.
	if err := v.validateMethodAuthority(); err != nil {
		return err
	}
	if !CarriesBody(v.Method) {
		if err := validateQueryEncodable(v.Method, v.InputSchema); err != nil {
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
		if err := v.RbacAction.Validate(); err != nil {
			return fmt.Errorf("operation %s gates on %q: %w", v.OperationID, v.RbacObject, err)
		}
		return nil
	}
	// The other half of the pair. An action with no object names no grant, so
	// nothing could enforce it and the declaration would read as governance
	// that is not there.
	if v.RbacAction != "" {
		return fmt.Errorf("operation %s declares RBAC action %q but no object — an action names a verb ON something, and there is nothing here for a role document to grant", v.OperationID, string(v.RbacAction))
	}
	// A MUTATING operation must name something a role document can withhold.
	//
	// This rule exists because its absence shipped. The object/action pair above
	// is enforced when an object is DECLARED, and nothing required one — so "a
	// governed operation that changes installation state under no RBAC object"
	// was expressible, and the first unit to use the secrets surface expressed
	// it: notes's store-signing-key declared neither, the object check in the
	// serving adapter therefore never ran, and the operation was admitted on
	// scope ∧ seat ∧ tier ∧ quota alone. For a cookie-session human that is any
	// authenticated seat. A read-only user replaced the installation's signing
	// key, on the REST route and through the agent, and every signature after it
	// was made with the key they chose. Found by the tier's UAT re-run (R1).
	//
	// Scope is what this can key on and it is the honest signal: `write` and
	// `draft` are the two Passport classes that persist something. `read` is
	// exempt because a read that owns no records has nothing to gate — that is
	// yogi, de and crm-hello, and refusing them would be refusing the common
	// case. The outbound classes never reach here on a served tool (the adapter
	// refuses them outright), and a handler-LESS declaration is held to the same
	// rule on purpose: it is published, a client can generate a call from it, and
	// it is one commit away from having behavior.
	//
	// Refused at DECLARATION, so it fails at generation with the operation named
	// rather than at the boot of whoever composed the unit — and refused here
	// rather than in the serving adapter, so a contract-only verb is covered too.
	if v.RequestedScope == ScopeWrite || v.RequestedScope == ScopeDraft {
		return fmt.Errorf("operation %s requests the %q scope but declares no RBAC object — an operation that "+
			"changes this installation's state must name something a role document can withhold, or it is "+
			"admitted on scope, seat, tier and quota alone (i.e. any authenticated seat). "+
			"Declare x-rbac-object (ext_%s_<object>) and x-rbac-action",
			v.OperationID, string(v.RequestedScope), strings.ReplaceAll(string(v.Unit), "-", "_"))
	}
	return nil
}

// RoutePrefix opens every extension path in the contract, spelled once: a unit
// owns /ext/<name>/…, and nothing else in the document may begin that way.
const RoutePrefix = "/ext/"

// ServedPath is the path a SERVER mounts this operation at — Route with the
// API base path put back, /v1/ext/<unit>/….
//
// It is a method rather than a second field on purpose. A field would be a
// second statement of one fact, and the two could disagree: a generator that
// emitted the pair could emit them inconsistently and nothing downstream could
// tell which was right. Derived, there is one fact (the contract's own path)
// and one total function over it, and a route that is wrong is wrong in the
// document where a reviewer will see it.
//
// Callers that mount or match HTTP paths want this; callers that speak about
// the contract — the client, the docs, the operator manifest — want Route.
func (v Verb) ServedPath() string { return APIBasePath + v.Route }

// validateMethod admits the closed set of methods this seam can mount.
//
// GET and DELETE were absent until arguments could arrive without a body. The
// reason given for excluding them — "a route whose arguments never arrive" —
// was true of a seam that read the body and nothing else, and it cost the
// surface the one thing the method is FOR: a read declared POST is
// indistinguishable from a write to any intermediary, any generated client, and
// (the defect that prompted this) the seat ceiling, which refused a read seat
// from every extension read because every extension route was unsafe. Now a
// bodyless method carries its arguments in the query string, so the exclusion
// buys nothing and costs the invariant.
func validateMethod(method string) error {
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return nil
	}
	return fmt.Errorf("method %q is not one an extension operation may declare (get, post, put, patch, delete)", method)
}

// CarriesBody reports whether an operation declared on this method takes its
// arguments from the request body. The false case takes them from the query
// string instead — see validateQueryEncodable for what a declaration must look
// like to be encodable that way, and compose/extroutes.go for the decode.
//
// Exported because the serving seam asks the same question this file's rules are
// written against, and two spellings of "does this method carry a body" could
// disagree — the disagreement being a route that reads a body nothing sent, or
// ignores arguments a client did send.
func CarriesBody(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		return true
	}
	return false
}

// validateMethodAuthority holds the method and the requested scope to each
// other. It is the rule that makes the method authoritative again, and it is
// load-bearing rather than tidy: the human seat ceiling classifies a mutation by
// method (identity.serveAsHuman), so a mutating GET would be a read seat's route
// to a write, and admitting GET without this check would OPEN that hole rather
// than merely leave it theoretical.
//
// Both directions, because each is a different lie about the operation. A GET
// that mutates tells every cache, crawler, retry and prefetch that replaying it
// is free. A PUT, PATCH or DELETE that only reads tells a reader the opposite —
// that the call changes something — which is a declaration a reviewer trusts and
// a client guards against for no reason.
//
// POST is the one method with no rule, and that is the model rather than an
// omission: POST is the RPC method, the method that means only "here are some
// arguments, run this". Every other verb states a semantic the scope can
// contradict, so every other verb is held to it.
func (v Verb) validateMethodAuthority() error {
	if v.Method == http.MethodGet && v.RequestedScope != ScopeRead {
		return fmt.Errorf("operation %s declares GET but requests the %q scope — a safe method may not carry "+
			"mutating authority: the seat ceiling classifies a mutation by method, so this would admit a read "+
			"seat to a write. Declare it POST, or request the read scope",
			v.OperationID, string(v.RequestedScope))
	}
	switch v.Method {
	case http.MethodPut, http.MethodPatch, http.MethodDelete:
		if v.RequestedScope == ScopeRead {
			return fmt.Errorf("operation %s declares %s but requests the read scope — a method that means "+
				"\"change this\" may not name an operation that changes nothing. Declare it GET when its "+
				"arguments are flat, POST otherwise",
				v.OperationID, v.Method)
		}
	}
	return nil
}

// validateQueryEncodable refuses an argument shape a bodyless method cannot
// honestly publish. A GET and a DELETE carry their arguments in the query
// string, and a query string is a flat list of name/value pairs where every
// value is text — so the declared schema has to be a flat object of primitives.
//
// Nested objects and arrays are refused rather than encoded. Every convention
// for putting them in a query (bracket notation, repeated keys, comma joins,
// JSON in a value) is a SECOND contract that the published schema does not
// describe, and a client generated from that schema would not produce it. An
// operation whose arguments have structure is a POST, which is the whole reason
// the body path still exists.
//
// The absent schema is fine: an operation taking no arguments is the common
// bodyless case (a list, a status probe), and it needs no query at all.
func validateQueryEncodable(method string, raw json.RawMessage) error {
	if raw == nil {
		return nil
	}
	var doc struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		// The root shape is validateSchemaObject's rule; what this can add is that
		// `properties` and `required` are the shapes they have to be to be read —
		// a `required` that is a bare string rather than a list lands here too.
		return fmt.Errorf("InputSchema declares a `properties` or `required` this reader cannot walk, so a %s route "+
			"cannot be told which query parameters it takes", method)
	}
	for name, prop := range doc.Properties {
		switch prop.Type {
		case "string", "number", "integer", "boolean":
			continue
		}
		return fmt.Errorf("operation declares %s but its argument %q is of type %q — a query string carries flat "+
			"text pairs, so a bodyless method's arguments must each be a string, number, integer or boolean. "+
			"An argument with structure belongs in a POST body",
			method, name, prop.Type)
	}
	// `required` must name declared arguments, and this check is HERE rather than
	// only at the serving seam so that Validate is genuinely the whole rule for a
	// bodyless declaration. It was not: compose's mount refused this shape while
	// Validate blessed it, which made the mount strictly stricter than the gate
	// that is documented as total — and because mounting panics on a refusal, a
	// declaration Validate accepted could still be a boot crash.
	for _, name := range doc.Required {
		if _, declared := doc.Properties[name]; !declared {
			return fmt.Errorf("operation declares %s and requires the argument %q, which its own properties do not "+
				"declare — no call could ever satisfy the route", method, name)
		}
	}
	return nil
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
