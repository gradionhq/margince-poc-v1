// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ToolHandler runs a governed tool after admission. It is the extension's
// BEHAVIOR — the one part of a Tool that is not static declaration — so
// the manifest generator skips it (behavior cannot be derived from the
// AST, and the manifest records only the governed request). A tool
// declared without a handler is inert: it appears in the manifest but the
// boot registers only handler-bearing tools into the live surface. The
// signature mirrors the core mcp.Tool.Handle the boot adapts it to;
// arguments and result are the raw JSON the tool's own typed decode
// validates.
//
// rt is the capability handle, and it arrives HERE — at invocation — rather
// than through the unit's constructor. That is the whole reason a
// declaration can be inert: New() is handed nothing, so a Tool value sitting
// in a slice holds no route into the core, and the boot can validate the
// composed set before any of it applies. The core mints rt for this one call
// and releases it when this function returns, which is why retaining it is a
// mistake the runtime reports (ErrRuntimeExpired) rather than one that
// quietly works. A handler needing no capability ignores the parameter.
type ToolHandler func(ctx context.Context, rt Runtime, in json.RawMessage) (json.RawMessage, error)

// Tier is the risk tier an extension REQUESTS for a governed tool:
// auto-execute runs without confirmation, confirmation-
// required stages every call for human approval. The constant names are
// semantic; their string values match the contract's own tier spelling
// ("auto_execute"/"confirmation_required" — see api_gen.go's AgentToolTier),
// which the boot registration maps to the internal RiskTier. A dynamic
// (argument-dependent) tier needs a resolver — behavior a static
// declaration cannot carry — so it is not requestable through this
// surface. The declared tier is a REQUEST an operator resolves,
// never a fact: an unresolved request never lowers a bar.
type Tier string

const (
	// TierAutoExecute REQUESTS auto-execution without human confirmation
	// (the 🟢 wire tier). Effective only once an operator resolves it.
	TierAutoExecute Tier = "auto_execute"
	// TierConfirmationRequired REQUESTS confirm-first staging — every call
	// waits for human approval (the 🟡 wire tier).
	TierConfirmationRequired Tier = "confirmation_required"
)

// Validate refuses any tier an extension may not request; the manifest
// generator and the boot preflight both call it, so gen-time acceptance
// cannot drift from boot-time validation.
func (t Tier) Validate() error {
	switch t {
	case TierAutoExecute, TierConfirmationRequired:
		return nil
	}
	return fmt.Errorf("risk tier %q is not one an extension may request — declare TierAutoExecute or TierConfirmationRequired (a dynamic per-call tier needs a resolver and is not declarable statically)", string(t))
}

// Scope is a Passport verb class a governed tool requires; its values
// mirror the core scope vocabulary the boot registration maps to the
// internal type.
type Scope string

const (
	// ScopeRead grants read access.
	ScopeRead Scope = "read"
	// ScopeDraft grants creation of drafts that do not leave the workspace.
	ScopeDraft Scope = "draft"
	// ScopeWrite grants mutation of records.
	ScopeWrite Scope = "write"
	// ScopeSend grants actions that leave the workspace (email, webhooks).
	ScopeSend Scope = "send"
	// ScopeEnrich grants enrichment from external sources.
	ScopeEnrich Scope = "enrich"
)

// Validate refuses a scope outside the Passport vocabulary; a tool
// requesting one no principal can hold would look granted while never
// admitting a call.
func (s Scope) Validate() error {
	switch s {
	case ScopeRead, ScopeDraft, ScopeWrite, ScopeSend, ScopeEnrich:
		return nil
	}
	return fmt.Errorf("scope %q is not in the Passport scope vocabulary (read, draft, write, send, enrich)", string(s))
}

// toolNameGrammar: agent tool verbs are lower snake_case (qualify_lead),
// the mcp.ToolSpec.Name convention the core registry keys on.
var toolNameGrammar = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

// Tool is a governed agent tool the unit contributes to the agent
// surface: a named operation running at a requested risk Tier and
// requiring a Scope, both recorded in the unit manifest.
//
// A handler-bearing tool is SERVED — registered into the same agent
// registry and admission gate as the core tools, callable at its declared
// tier. A handler-less tool is inert: it still appears in the manifest as
// a governed request, but nothing runs. Resolving a requested tier against
// a durable operator decision is a later governance step; today a composed
// first-party unit serves at its declared tier, the way the jurisdiction
// packs ship enabled.
type Tool struct {
	// Name is the tool verb, lower snake_case, unique within the unit.
	Name string
	// Title is the human-readable label tools/list shows in place of Name
	// (the protocol's display precedence is title > name). Optional: a unit
	// that declares none is listed under its verb, which is what a client
	// falls back to anyway. It carries no authority, so it is no part of the
	// manifest's governance descriptor — but it is still validated, at gen
	// time and at boot, because the core registry refuses a blank one.
	Title string
	// Description is what the tool is FOR, in the caller's terms: the outcome
	// it produces, the limits on producing it, when a neighbouring tool is the
	// better call, and what to keep from its result. It is the text a model
	// selects the tool by, so it is the unit's to write — nothing derivable
	// substitutes for it, and the name it would fall back to is the thing it
	// exists to explain.
	//
	// It is optional HERE, on the declaration, because a handler-less tool is a
	// manifest request no client is ever shown. A SERVED tool is refused
	// without one at boot: see the composition's tool adapter.
	Description string
	// Version is the tool's own version, recorded for the registry; it
	// carries no authority (decisions bind to digests, not versions).
	Version string
	// Tier is the requested risk tier (green or yellow).
	Tier Tier
	// RequestedScope is the single Passport verb class the tool requests —
	// one scope per tool, as core tools declare it
	// (mcp.ToolSpec.RequiredScope). It is a REQUEST an operator
	// resolves into an effective grant, not a fact; once effective, a call
	// admits only when the granting principal holds it.
	RequestedScope Scope

	// InputSchema and OutputSchema are the JSON Schema documents the served
	// tool advertises through tools/list (mapped onto mcp.ToolSpec). They
	// are client-facing DOCUMENTATION: the agent reads them to shape a
	// call, but the tool's own typed decode — not a generic schema check —
	// enforces its invariants. Optional, and both must be valid JSON when
	// set. They are not part of the governance descriptor, so the
	// manifest generator does not read them.
	InputSchema  json.RawMessage
	OutputSchema json.RawMessage

	// Handle is the tool's behavior. It is optional: a nil Handle declares
	// the tool (it still appears in the manifest as a governed request) but
	// leaves it inert; boot serves only handler-bearing tools. The manifest
	// generator does not read it — behavior is not a static declaration.
	Handle ToolHandler
}

// Validate enforces the tool's grammar and vocabularies. The name, tier,
// and scope checks run at BOTH gen time (the manifest generator) and boot;
// the schema checks run only at boot, because the manifest does not carry
// the schemas (the generator never reads them). Boot registration refuses
// the composed set on any violation.
func (t Tool) Validate() error {
	if !toolNameGrammar.MatchString(t.Name) {
		return fmt.Errorf("tool name %q is not a valid verb (lower snake_case, e.g. qualify_lead)", t.Name)
	}
	if t.Version == "" {
		return fmt.Errorf("tool %q declares no version", t.Name)
	}
	if err := validateRenderedText("title", t.Title); err != nil {
		return fmt.Errorf("tool %q: %w", t.Name, err)
	}
	if err := validateRenderedText("description", t.Description); err != nil {
		return fmt.Errorf("tool %q: %w", t.Name, err)
	}
	if err := t.Tier.Validate(); err != nil {
		return fmt.Errorf("tool %q: %w", t.Name, err)
	}
	if err := t.RequestedScope.Validate(); err != nil {
		return fmt.Errorf("tool %q: %w", t.Name, err)
	}
	if err := validateSchemaObject("InputSchema", t.InputSchema); err != nil {
		return fmt.Errorf("tool %q: %w", t.Name, err)
	}
	if err := validateSchemaObject("OutputSchema", t.OutputSchema); err != nil {
		return fmt.Errorf("tool %q: %w", t.Name, err)
	}
	return nil
}

// validateRenderedText checks one declared string a CLIENT renders verbatim —
// the display title, or the description a model selects by. Absent is allowed
// for both here; what each absence means is decided where the tool is served,
// not in the grammar.
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
