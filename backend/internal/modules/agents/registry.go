// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package agents is the governed MCP tool surface (03b Layer 1,
// interfaces.md §2): the ONE artifact every agent surface consumes — the
// local stdio server (A1) today, the hosted HTTPS server (A2) and the
// first-party Surface-B runner later. All of them dispatch through this
// registry, and the registry admits every call through platform/auth
// before a handler runs: no back door, no privileged registry (ADR-0013).
package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// Registry implements mcp.Registry. Registration happens at composition
// time and is then read-only; Invoke is safe for concurrent callers.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]mcp.Tool
	// specs[tool] is what every surface is SERVED for that tool: its own spec
	// with the declared output shape wrapped in the result envelope. Held here
	// rather than re-derived per request because tools/list embeds it verbatim
	// and a client caches it — one derivation, one set of bytes, forever.
	specs map[string]mcp.ToolSpec
	// idArgs[tool] is what that tool's schema says about its uuid arguments,
	// read off the schema once at registration. Invoke enforces it, so the
	// schema's claims are true of the surface rather than of whichever handlers
	// remembered to check them.
	idArgs map[string]idArgSpec
	// numArgs[tool] is the range its schema advertises for each numeric
	// argument, read off the schema once at registration. Invoke holds a
	// supplied value to it, so `minimum`/`maximum` bind the surface instead of
	// describing an intention.
	numArgs map[string][]numBound
	// requiredArgs[tool] is what that tool's schema says it cannot run without,
	// read off the schema once at registration. Invoke holds a call to it, so
	// `required` binds the surface rather than describing an intention that
	// each handler then re-states in its own words.
	requiredArgs map[string][]string
	// approvals closes the 🟡 loop (stage on refusal, redeem on retry).
	// Nil is a legal composition — the gate still refuses; refused calls
	// just have nowhere to land.
	approvals Approvals
	// gate is the platform/auth admission point; it re-derives the
	// granting human's authority live per call. A nil gate fails closed
	// for agent principals (Gate.Admit owns that rule).
	gate *auth.Gate
	// quota is the MCP-SESS-* meter this surface CHARGES. The gate holds the
	// same meter and does the refusing; the split is deliberate — a bound is
	// enforced where admission is decided and paid where records and effects
	// leave.
	quota QuotaCharger
	// cost answers the SOFT budget-share counter, whose only effect is a
	// warning on the answer (quota.go).
	cost CostShareReader
}

// NewRegistry builds the tool surface over its approvals engine and admission
// gate. Options add the dependencies only some roles have — today, the meter
// served records are charged against.
func NewRegistry(approvals Approvals, gate *auth.Gate, opts ...RegistryOption) *Registry {
	r := &Registry{
		tools:        map[string]mcp.Tool{},
		specs:        map[string]mcp.ToolSpec{},
		idArgs:       map[string]idArgSpec{},
		numArgs:      map[string][]numBound{},
		requiredArgs: map[string][]string{},
		approvals:    approvals,
		gate:         gate,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

var _ mcp.Registry = (*Registry)(nil)

// Invoke runs the admission gate, then the tool. There is no other path
// to a Handle in this package. A refused 🟡 call is staged for human
// decision; a retry carrying `approval_id` redeems that decision — bound
// to the identical call by content hash — and only then reaches Handle.
func (r *Registry) Invoke(ctx context.Context, name string, in json.RawMessage) (json.RawMessage, error) {
	// The REGISTERED spec, not a fresh Spec() call. The two are the same for a
	// tool whose spec is a literal, and every tool here is — but the registered
	// one is what tools/list advertised and what the argument constraints were
	// read off, so serving a call from anything else would let the version a
	// result reports and the schema a client cached come from two different
	// readings. One reading, taken once, under the same lock as the handler.
	r.mu.RLock()
	t, ok := r.tools[name]
	spec := r.specs[name]
	r.mu.RUnlock()
	if !ok {
		return nil, &UnknownToolError{Name: name}
	}

	args, approvalID, diffHash, err := splitApproval(in)
	if err != nil {
		return nil, err
	}

	admitted, err := r.gate.Admit(ctx, spec, r.tierResolverFor(ctx, t, name, args))
	// Static-tier tools, whose resolver Admit never runs. After authority and
	// before staging, and both halves matter: a caller the gate turns away learns
	// nothing about arguments, while a caller it would send to the approval queue
	// is told about its own bad arguments first — staging an unrunnable call spends
	// a human's yes on something that was never going to happen. A step-up is
	// staging too, and spends the same yes.
	if askedOfAHuman(err) {
		if argErr := r.requireDeclaredArgs(name, args); argErr != nil {
			return nil, argErr
		}
	}
	ctx = admitted
	if stepUp := releasableQuotaRefusal(err); stepUp != nil {
		return nil, r.stageStepUp(ctx, stepUp)
	}
	// The call ceiling is charged where the call is known to RUN, and only
	// there. A refusal, a staged 🟡, and a token that fails redemption all
	// execute nothing — counting them would let a caller suspend its own
	// Passport with requests it was never allowed to make, or with a replayed
	// token that opens nothing.
	//
	// Whether it may REFUSE depends on what has already committed. Before a
	// redemption, nothing has, so an uncountable call is not run. After one, the
	// human's approval is consumed and refusing would burn it on a call that
	// never happened and can never be redeemed again — so the charge is absorbed
	// there instead, the same asymmetry a committed write takes.
	if err == nil && approvalID.IsZero() {
		if chargeErr := r.chargeCall(ctx, spec, nothingHasHappenedYet); chargeErr != nil {
			return nil, chargeErr
		}
	}
	switch {
	case err == nil:
		// An auto-execute call may still carry approval_id: the retry of a
		// per-field precedence staging (interfaces.md §2.1) admits at the
		// auto-execute tier, so its asserted authority is consumed HERE — validated against
		// the identical-call hash, never ignored. The redeemed mark tells
		// the handler the overwrite it is about to make was human-released.
		if !approvalID.IsZero() {
			if r.approvals == nil {
				return nil, fmt.Errorf("crmagents: approval_id presented but this surface has no approvals engine: %w", apperrors.ErrApprovalTokenInvalid)
			}
			marked, _, _, rErr := RedeemAndMark(ctx, r.approvals, approvalID, spec.Name, diffHash)
			if rErr != nil {
				return nil, rErr
			}
			ctx = marked
		}
		return r.handle(ctx, t, spec, args)
	case !errors.Is(err, apperrors.ErrRequiresApproval) || r.approvals == nil:
		return nil, err
	case !approvalID.IsZero():
		marked, _, _, rErr := RedeemAndMark(ctx, r.approvals, approvalID, spec.Name, diffHash)
		if rErr != nil {
			return nil, rErr
		}
		r.ChargeRedeemedCall(marked, spec)
		return r.handle(marked, t, spec, args)
	default:
		return nil, r.stageRefusedCall(ctx, t, spec.Name, args, diffHash, err)
	}
}

// handle runs an admitted call and seals its answer into the result envelope.
//
// Every path out of Invoke that reaches a handler comes through here — the
// straight auto-execute call and both approval redemptions — so the envelope is
// a property of the SURFACE rather than of the paths someone remembered. A
// handler still marshals only its own payload and never sees the envelope,
// which is what keeps thirty tools from carrying thirty spellings of it.
//
// The failure path deliberately seals nothing: a refusal is an error, and an
// error carries the sentinel and the message the caller acts on, not a document
// with an empty payload inside it.
func (r *Registry) handle(ctx context.Context, t mcp.Tool, spec mcp.ToolSpec, args json.RawMessage) (json.RawMessage, error) {
	ctx, trace := withTrace(ctx)
	ctx, facts := withEnvelopeFacts(ctx)
	noteRowScope(ctx)
	out, err := t.Handle(ctx, args)
	if err != nil {
		return nil, err
	}
	r.noteCostShare(ctx)
	sealed, err := sealEnvelope(spec, trace, facts, out)
	if err != nil {
		return nil, err
	}
	// Charged AFTER sealing and BEFORE returning: at this point the answer
	// exists but has not reached the caller, so a charge that cannot be
	// recorded can still refuse it. An answer sealed and then charged in the
	// other order would spend the window on a result a marshalling failure was
	// about to discard.
	if err := r.chargeAnswer(ctx, spec, facts.servedCount()); err != nil {
		return nil, err
	}
	return sealed, nil
}

// tierResolverFor builds the resolver Admit consults for the call's tier. A
// static-tier tool needs nothing but its own arguments; Admit never invokes
// the resolver for one at all.
//
// A dynamic tool's argument checks run HERE, and only for a dynamic tool,
// because a dynamic tool decides its own tier by READING the record an
// argument names: a zero deal_id would reach the stage lookup and come back
// as a bare not-found from inside the gate, where no downstream check can
// reach it. Admit calls the resolver after scope and seat, so this still sits
// behind the authority checks that do not depend on arguments. Static-tier
// tools are covered by the call after Admit.
func (r *Registry) tierResolverFor(ctx context.Context, t mcp.Tool, name string, args json.RawMessage) func() (mcp.TierResolverInput, error) {
	dyn, ok := t.(dynamicTool)
	if !ok {
		return func() (mcp.TierResolverInput, error) {
			return mcp.TierResolverInput{Args: args}, nil
		}
	}
	return func() (mcp.TierResolverInput, error) {
		if err := r.requireDeclaredArgs(name, args); err != nil {
			return mcp.TierResolverInput{}, err
		}
		return dyn.ResolverInput(ctx, args)
	}
}

// stageRefusedCall parks a 🟡 call the gate refused as a staged approval, so
// the human decision the refusal asks for has somewhere to land; a retry
// carrying its approval_id redeems it. A tool that cannot describe its own
// staging target has nothing to park, so the refusal stands as the answer.
func (r *Registry) stageRefusedCall(ctx context.Context, t mcp.Tool, tool string, args json.RawMessage, diffHash string, refusal error) error {
	stageable, ok := t.(stageableTool)
	if !ok {
		return refusal
	}
	info, err := stageable.StageInfo(ctx, args)
	if err != nil {
		// The staging read failed (bad args, out-of-scope target) —
		// that is the real answer, not "needs approval".
		return err
	}
	id, err := r.approvals.Stage(ctx, StageRequest{
		Tool:           tool,
		ProposedChange: args,
		DiffHash:       diffHash,
		TargetType:     info.TargetType,
		TargetID:       info.TargetID,
		TargetVersion:  info.TargetVersion,
		Summary:        info.Summary,
	})
	if err != nil {
		return err
	}
	return &workflow.StagedApprovalError{ApprovalID: id}
}

// Spec returns the registered spec for name — the REST admission path
// (ADR-0055) resolves a mutating operation's tool twin through this.
func (r *Registry) Spec(name string) (mcp.ToolSpec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	spec, ok := r.specs[name]
	return copySchemas(spec), ok
}

// copySchemas hands out a spec whose schemas are the CALLER's bytes.
//
// A json.RawMessage is a slice, so returning the registered one shares its
// backing array: a caller that wrote through it would rewrite what tools/list
// advertises and what results are validated against, for every later request,
// from outside the lock. The schemas are the two members that can be written
// through; everything else on a ToolSpec is copied by the assignment.
func copySchemas(spec mcp.ToolSpec) mcp.ToolSpec {
	spec.InputSchema = bytes.Clone(spec.InputSchema)
	spec.OutputSchema = bytes.Clone(spec.OutputSchema)
	return spec
}

// Specs lists the registered surface, stably ordered for tools/list.
func (r *Registry) Specs() []mcp.ToolSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]mcp.ToolSpec, 0, len(r.specs))
	for _, spec := range r.specs {
		out = append(out, copySchemas(spec))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Offered is the surface THIS caller may invoke: the catalog both the external
// tools/list and a Surface-B run's tool listing are drawn from.
//
// One function serves both, rather than two filters that agree today. A run
// offered a verb its passport cannot spend is being asked to choose among names
// it will be refused for, and every one of them rides in a system prompt that
// elision never touches.
//
// It answers the SCOPE axis only. Invoke remains the authority on the seat
// ceiling and object RBAC, which are re-derived per call — this narrows what is
// advertised and enforces nothing.
func (r *Registry) Offered(ctx context.Context) []mcp.ToolSpec {
	all := r.Specs()
	out := make([]mcp.ToolSpec, 0, len(all))
	for _, spec := range all {
		if invocableByCaller(ctx, spec) {
			out = append(out, spec)
		}
	}
	return out
}

// dynamicTool is implemented by TierDynamic tools that need more than the
// raw args to resolve their tier — advance_deal reads the target stage's
// semantic from pipeline configuration, which costs a database read the
// gate should pay only for dynamic calls.
type dynamicTool interface {
	ResolverInput(ctx context.Context, in json.RawMessage) (mcp.TierResolverInput, error)
}

// UnknownToolError answers a tools/call for a name outside the surface.
type UnknownToolError struct{ Name string }

// maxToolNameEcho bounds the caller-supplied name this error quotes back.
// The name is chosen freely by the model and lands in a transcript that the
// same run's later prompts read, so an unbounded echo is an unbounded write
// into those prompts. Generous next to the longest real tool name, short
// enough that the field cannot carry prose.
const maxToolNameEcho = 64

// Error renders the echo HERE rather than at each surface, so no consumer —
// the tool result, the server log, a future transport — can quote the name
// back raw by forgetting to. Bounded AND escaped: the name is chosen by the
// model, and a newline in it would otherwise open what reads as a new line of
// the transcript that the same run's later prompts go on to read.
func (e *UnknownToolError) Error() string {
	return "unknown tool " + echoSafe(e.Name, maxToolNameEcho)
}
