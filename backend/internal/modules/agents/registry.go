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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
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
	// approvals closes the 🟡 loop (stage on refusal, redeem on retry).
	// Nil is a legal composition — the gate still refuses; refused calls
	// just have nowhere to land.
	approvals Approvals
	// gate is the platform/auth admission point; it re-derives the
	// granting human's authority live per call. A nil gate fails closed
	// for agent principals (Gate.Admit owns that rule).
	gate *auth.Gate
}

func NewRegistry(approvals Approvals, gate *auth.Gate) *Registry {
	return &Registry{tools: map[string]mcp.Tool{}, approvals: approvals, gate: gate}
}

var _ mcp.Registry = (*Registry)(nil)

// Register refuses, at boot, the spec defects that would otherwise surface as
// a runtime authority bug or a broken wire response: a duplicate name (two
// handlers behind one admission decision), a TierDynamic spec with no resolver
// (a tool whose tier nobody computes would default to whatever the gate
// assumes), a missing display title, and a schema that is not an encodable
// object (see assertObjectSchemas — one bad brace takes the whole tools/list
// down, not just its own tool).
//
// This is the ONE door every tool comes through, core and extension alike, so
// none of it is a list of tools someone has to keep current.
func (r *Registry) Register(t mcp.Tool) {
	spec := t.Spec()
	if spec.Name == "" {
		//craft:ignore panic-in-domain composition-time registration assertion — fires only while cmd wiring runs, never on a request path
		panic("crmagents: registering a tool with no name")
	}
	if spec.Tier == mcp.TierDynamic && spec.TierResolver == nil {
		//craft:ignore panic-in-domain composition-time registration assertion — fires only while cmd wiring runs, never on a request path
		panic(fmt.Sprintf("crmagents: %s is TierDynamic without a TierResolver", spec.Name))
	}
	if spec.Tier != mcp.TierDynamic && spec.TierResolver != nil {
		//craft:ignore panic-in-domain composition-time registration assertion — fires only while cmd wiring runs, never on a request path
		panic(fmt.Sprintf("crmagents: %s carries a TierResolver but is not TierDynamic", spec.Name))
	}
	// TrimSpace, because a blank title is worse than none: a client takes it
	// over the name (title outranks name for display) and renders an empty
	// heading, where an absent one would at least have fallen back.
	if strings.TrimSpace(spec.Title) == "" {
		//craft:ignore panic-in-domain composition-time registration assertion — fires only while cmd wiring runs, never on a request path
		panic(fmt.Sprintf("crmagents: %s has no Title — tools/list would render its identifier as its display name", spec.Name))
	}
	if err := assertObjectSchemas(spec); err != nil {
		//craft:ignore panic-in-domain composition-time registration assertion — fires only while cmd wiring runs, never on a request path
		panic("crmagents: " + err.Error())
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.tools[spec.Name]; dup {
		//craft:ignore panic-in-domain composition-time registration assertion — fires only while cmd wiring runs, never on a request path
		panic(fmt.Sprintf("crmagents: duplicate tool %s", spec.Name))
	}
	r.tools[spec.Name] = t
}

// Invoke runs the admission gate, then the tool. There is no other path
// to a Handle in this package. A refused 🟡 call is staged for human
// decision; a retry carrying `approval_id` redeems that decision — bound
// to the identical call by content hash — and only then reaches Handle.
func (r *Registry) Invoke(ctx context.Context, name string, in json.RawMessage) (json.RawMessage, error) {
	r.mu.RLock()
	t, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		return nil, &UnknownToolError{Name: name}
	}
	spec := t.Spec()

	args, approvalID, diffHash, err := splitApproval(in)
	if err != nil {
		return nil, err
	}

	resolve := func() (mcp.TierResolverInput, error) {
		return mcp.TierResolverInput{Args: args}, nil
	}
	if dyn, ok := t.(dynamicTool); ok {
		resolve = func() (mcp.TierResolverInput, error) { return dyn.ResolverInput(ctx, args) }
	}

	ctx, err = r.gate.Admit(ctx, spec, resolve)
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
		return t.Handle(ctx, args)
	case !errors.Is(err, apperrors.ErrRequiresApproval) || r.approvals == nil:
		return nil, err
	case !approvalID.IsZero():
		marked, _, _, rErr := RedeemAndMark(ctx, r.approvals, approvalID, spec.Name, diffHash)
		if rErr != nil {
			return nil, rErr
		}
		return t.Handle(marked, args)
	default:
		stageable, ok := t.(stageableTool)
		if !ok {
			return nil, err
		}
		info, infoErr := stageable.StageInfo(ctx, args)
		if infoErr != nil {
			// The staging read failed (bad args, out-of-scope target) —
			// that is the real answer, not "needs approval".
			return nil, infoErr
		}
		id, stageErr := r.approvals.Stage(ctx, StageRequest{
			Tool:           spec.Name,
			ProposedChange: args,
			DiffHash:       diffHash,
			TargetType:     info.TargetType,
			TargetID:       info.TargetID,
			TargetVersion:  info.TargetVersion,
			Summary:        info.Summary,
		})
		if stageErr != nil {
			return nil, stageErr
		}
		return nil, &workflow.StagedApprovalError{ApprovalID: id}
	}
}

// Spec returns the registered spec for name — the REST admission path
// (ADR-0055) resolves a mutating operation's tool twin through this.
func (r *Registry) Spec(name string) (mcp.ToolSpec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	if !ok {
		return mcp.ToolSpec{}, false
	}
	return t.Spec(), true
}

// Specs lists the registered surface, stably ordered for tools/list.
func (r *Registry) Specs() []mcp.ToolSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]mcp.ToolSpec, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t.Spec())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// assertObjectSchemas holds two promises tools/list and tools/call have to
// keep, at the one door every tool comes through.
//
// The first is ENCODABILITY. Both schemas are hand-written JSON literals
// spliced together from constants, and they reach the client by being embedded
// verbatim into the tools/list response — so ONE misplaced brace does not
// break one tool, it makes the whole listing unencodable and every tool
// disappears behind a 500. That is a boot-time defect discovered on a client's
// first request, which is exactly the wrong end.
//
// The second is that both are OBJECT schemas. MCP requires an object input
// schema, and a declared outputSchema obliges the server to answer with
// structured content conforming to it — which the dispatcher can only do for
// an object, because structuredContent is typed as one. A schema written some
// other way (a $ref, a bare allOf) fails here on purpose: not wrong, but not
// something the dispatcher has been taught to honour, and failing at boot
// beats advertising a shape the results miss.
func assertObjectSchemas(spec mcp.ToolSpec) error {
	if spec.InputSchema == nil {
		// The protocol requires one. A tool taking no arguments still declares
		// `{"type":"object"}`; nil would put a bare null on tools/list.
		return fmt.Errorf("%s declares no InputSchema; MCP requires every tool to advertise an object input schema", spec.Name)
	}
	for _, s := range []struct {
		field string
		raw   json.RawMessage
	}{
		{field: "InputSchema", raw: spec.InputSchema},
		// Optional: a tool promising no output shape owes tools/call no
		// structured content.
		{field: "OutputSchema", raw: spec.OutputSchema},
	} {
		if s.raw == nil {
			continue
		}
		var declared struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(s.raw, &declared); err != nil {
			return fmt.Errorf("%s has an %s that is not valid JSON, which makes the whole tools/list response unencodable: %w",
				spec.Name, s.field, err)
		}
		if declared.Type != "object" {
			return fmt.Errorf("%s declares %s type %q; this surface serves object schemas only",
				spec.Name, s.field, declared.Type)
		}
	}
	return nil
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
