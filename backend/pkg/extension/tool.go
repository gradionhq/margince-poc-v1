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

// Tool is the BEHAVIOR half of a governed agent tool: the verb, and the Go
// function that runs it. Nothing else — no tier, no scope, no description, no
// schemas. Those are governance and prose, and they live in the unit's
// contract fragment (extensions/<name>/api/<contract>.yaml), reach the process
// as an extension.Verb, and are what an operator resolves. The contract IS the
// surface; this struct is the only part of it a static document cannot hold.
//
// A tool whose verb the contract declares and which carries a Handle here is
// SERVED — registered into the same agent registry and admission gate as the
// core tools, at the tier the contract declares. A verb the contract declares
// with no entry here is inert: it still appears in the unit manifest as a
// governed request, but nothing runs. A Tool entry whose verb NO contract
// operation declares is a wiring defect and fails the boot — behavior with no
// published surface would be a capability nothing declared.
type Tool struct {
	// Name is the tool verb, lower snake_case, and must equal the x-mcp-tool
	// verb of one of the declaring unit's contract operations.
	Name string
	// Handle is the tool's behavior. It is optional: a nil Handle is the same
	// as no entry at all (the verb stays a manifest request and serves
	// nothing), which is why a unit with no Go behavior for a declared verb
	// writes no Tools entry rather than an entry holding nil. The manifest
	// generator does not read it — behavior is not a static declaration.
	Handle ToolHandler
}

// Validate enforces the verb grammar, and nothing more: every other rule a
// tool is subject to is a rule about its DECLARATION, which is the contract's
// (see Verb.Validate). Boot registration refuses the composed set on a
// violation.
func (t Tool) Validate() error {
	if !toolNameGrammar.MatchString(t.Name) {
		return fmt.Errorf("tool name %q is not a valid verb (lower snake_case, e.g. qualify_lead)", t.Name)
	}
	return nil
}
