// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package extension

import (
	"fmt"
	"regexp"
)

// Tier is the autonomy class an extension REQUESTS for a governed tool
// (ADR-0026/A34): 🟢 auto-executes, 🟡 stages for confirm-first approval.
// The values mirror the core RiskTier wire strings, which the boot
// registration maps to the internal type. Argument-dependent (dynamic)
// tiers are core-only — they need a resolver, which is behavior an
// extension cannot declare statically — so an extension may request only
// green or yellow. Per §7 the declared tier is a REQUEST an operator
// resolves, never a fact: an unresolved request never lowers a bar.
type Tier string

const (
	// TierGreen requests auto-execution (🟢).
	TierGreen Tier = "green"
	// TierYellow requests confirm-first staging (🟡).
	TierYellow Tier = "yellow"
)

// Validate refuses any tier an extension may not request; the manifest
// generator and the boot preflight both call it, so gen-time acceptance
// cannot drift from boot-time validation.
func (t Tier) Validate() error {
	switch t {
	case TierGreen, TierYellow:
		return nil
	}
	return fmt.Errorf("autonomy tier %q is not one an extension may request (green or yellow; dynamic tiers are core-only)", string(t))
}

// Scope is a Passport verb class a governed tool requires
// (interfaces.md §0), its values mirroring the core scope vocabulary the
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
// surface: a named operation running at a requested autonomy Tier and
// requiring Scopes. It is a GOVERNED capability — its tier and scopes are
// requests an operator resolves (§7), recorded in the unit manifest.
//
// Declaring a tool records the request; SERVING it — registration into
// the agent surface behind the operator-approval gate — arrives in a
// later slice. Until then a declared tool is inert: present in the
// manifest, not yet callable.
type Tool struct {
	// Name is the tool verb, lower snake_case, unique within the unit.
	Name string
	// Version is the tool's own version, recorded for the registry; it
	// carries no authority (§7 binds decisions to digests, not versions).
	Version string
	// Tier is the requested autonomy class (green or yellow).
	Tier Tier
	// RequiredScope is the single Passport verb class the tool requires —
	// one scope per tool, as core tools declare it (mcp.ToolSpec). A call
	// admits only when the granting principal holds it.
	RequiredScope Scope
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
	if err := t.RequiredScope.Validate(); err != nil {
		return fmt.Errorf("tool %q: %w", t.Name, err)
	}
	return nil
}
