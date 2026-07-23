// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package extension

import (
	"fmt"
	"regexp"
)

// Tier is the risk tier an extension REQUESTS for a governed tool
// : auto-execute runs without confirmation, confirmation-
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

// Scope is a Passport verb class a governed tool requires
// , its values mirroring the core scope vocabulary the
// boot registration maps to the internal type.
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
// requiring Scopes. It is a GOVERNED capability — its tier and scopes are
// requests an operator resolves, recorded in the unit manifest.
//
// Declaring a tool records the request; SERVING it — registration into
// the agent surface behind the operator-approval gate — arrives in a
// later slice. Until then a declared tool is inert: present in the
// manifest, not yet callable.
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
}

// Validate enforces the tool's grammar and vocabularies. Boot
// registration refuses the composed set on a violation, and the manifest
// generator raises the same error at gen time.
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
	return nil
}
