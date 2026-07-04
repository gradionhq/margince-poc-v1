// Package mcp defines the governed tool contract (interfaces.md §2,
// 03b Layer 1). A tool registers a name, the Passport scope it requires,
// its autonomy tier, and JSON-schema in/out bound to a crm.yaml operation.
// The registry's admission gate — scope ∧ tier (∧ the read/full seat
// ceiling) — runs BEFORE Handle, so per-call policy lives in the typed
// model, never ad-hoc inside a handler. (Per-agent quota/budget is
// specified but not yet enforced here; it joins this gate when the budget
// layer lands.)
package mcp

import (
	"context"
	"encoding/json"

	"github.com/gradionhq/margince/backend/crmctx"
)

// Tool is a single governed MCP tool, exposed identically to every
// compliant agent (BYO Claude/Cursor/Copilot or the first-party runner).
type Tool interface {
	Spec() ToolSpec
	// Handle runs only after admission. Validation is by the handler's typed
	// decode (decodeArgs into a strict struct), NOT a generic check against
	// Spec().InputSchema: InputSchema/OutputSchema are client-facing
	// documentation (what tools/list advertises), so any required-field or
	// range rule that must actually hold has to be enforced in the decode +
	// its tests, not left to the schema alone.
	Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error)
}

// ToolSpec is the registration shape, versioned and contract-bound to
// crm.yaml (one source of truth for wire shape).
type ToolSpec struct {
	Name          string
	Version       string
	RequiredScope crmctx.Scope
	Tier          RiskTier
	// TierResolver is non-nil iff Tier == TierDynamic; the admission gate
	// calls it with the validated args plus the resolved pipeline context.
	TierResolver TierResolver
	// InputSchema/OutputSchema are client-facing documentation advertised by
	// tools/list; they are NOT generically validated (M2) — handlers decode
	// into strict typed structs and enforce their own invariants.
	InputSchema  json.RawMessage
	OutputSchema json.RawMessage
	OpenAPIOp    string // the crm.yaml operationId (or logical op family) this maps to
	Egress       bool   // true if the tool reaches outside the workspace (send_email, webhooks)
}

// RiskTier is the autonomy class (A34/ADR-0026). Green and Yellow are
// static — the declared value is the tool's whole tier. Dynamic means the
// effective tier depends on the call's arguments and MUST carry a
// TierResolver (today only advance_deal/progress_deal: 🟢 open→open,
// 🟡 to won/lost).
type RiskTier int

const (
	TierGreen RiskTier = iota
	TierYellow
	TierDynamic
)

// TierResolver maps one call to its effective static tier. The input
// carries the validated args plus the target stage and pipeline, because
// won/lost is a property of the stage's semantic, not of the request
// arguments (a custom pipeline's renamed "Won" stage still resolves 🟡).
// Invariant: a resolver may only ever RAISE to TierYellow — it never
// returns TierDynamic and never resolves an always-🟡 floor case to green.
type TierResolver func(in TierResolverInput) RiskTier

// TierResolverInput is what the admission gate hands a resolver.
type TierResolverInput struct {
	Args json.RawMessage
	// TargetStageSemantic is the resolved semantic of the stage the call
	// moves to: "open" | "won" | "lost".
	TargetStageSemantic string
	PipelineID          string
}

// Registry admits and dispatches tools. Registration is init()-time,
// one file per tool; a duplicate name fails fast at boot.
type Registry interface {
	Register(t Tool)
	// Invoke runs the admission gate (scope ∧ tier ∧ seat ceiling) and then
	// the tool. A 🟡 call without a valid approval token returns
	// errs.ErrRequiresApproval with the staged approval reference.
	Invoke(ctx context.Context, name string, in json.RawMessage) (json.RawMessage, error)
	Specs() []ToolSpec
}
