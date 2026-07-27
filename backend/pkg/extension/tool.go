// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
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
type ToolHandler func(ctx context.Context, in json.RawMessage) (json.RawMessage, error)

// Tier is the risk tier an extension REQUESTS for a governed tool:
// auto-execute runs without confirmation, confirmation-
// required stages every call for human approval. The constant names are
// semantic; their string values are the core "green"/"yellow" wire tiers,
// which the boot registration maps to the internal RiskTier. A dynamic
// (argument-dependent) tier needs a resolver — behavior a static
// declaration cannot carry — so it is not requestable through this
// surface. The declared tier is a REQUEST an operator resolves,
// never a fact: an unresolved request never lowers a bar.
type Tier string

const (
	// TierAutoExecute REQUESTS auto-execution without human confirmation
	// (the 🟢 wire tier). Effective only once an operator resolves it.
	TierAutoExecute Tier = "green"
	// TierConfirmationRequired REQUESTS confirm-first staging — every call
	// waits for human approval (the 🟡 wire tier).
	TierConfirmationRequired Tier = "yellow"
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

// validateSchemaObject checks a declared schema, when present, is a JSON
// object rooted at `"type": "object"` — the shape MCP requires of a tool's
// input/output schema in tools/list. Absent (nil) is allowed: the served
// spec defaults a missing input schema to an empty object.
func validateSchemaObject(field string, raw json.RawMessage) error {
	if raw == nil {
		return nil
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("%s must be a JSON object", field)
	}
	if doc["type"] != "object" {
		return fmt.Errorf(`%s must be a JSON Schema object rooted at "type":"object"`, field)
	}
	return nil
}
