// Package agents is the governed MCP tool surface (03b Layer 1,
// interfaces.md §2): the ONE artifact every agent surface consumes — the
// local stdio server (A1) today, the hosted HTTPS server (A2) and the
// first-party Surface-B runner later. All of them dispatch through this
// registry, and the registry admits every call through internal/gate
// before a handler runs: no back door, no privileged registry (ADR-0013).
package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
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
}

func NewRegistry(approvals Approvals) *Registry {
	return &Registry{tools: map[string]mcp.Tool{}, approvals: approvals}
}

var _ mcp.Registry = (*Registry)(nil)

// Register refuses the two spec defects that would otherwise surface as
// runtime authority bugs: a duplicate name (two handlers behind one
// admission decision) and a TierDynamic spec with no resolver (a tool
// whose tier nobody computes would default to whatever the gate assumes).
func (r *Registry) Register(t mcp.Tool) {
	spec := t.Spec()
	if spec.Name == "" {
		panic("crmagents: registering a tool with no name")
	}
	if spec.Tier == mcp.TierDynamic && spec.TierResolver == nil {
		panic(fmt.Sprintf("crmagents: %s is TierDynamic without a TierResolver", spec.Name))
	}
	if spec.Tier != mcp.TierDynamic && spec.TierResolver != nil {
		panic(fmt.Sprintf("crmagents: %s carries a TierResolver but is not TierDynamic", spec.Name))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.tools[spec.Name]; dup {
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

	err = auth.Admit(ctx, spec, resolve)
	switch {
	case err == nil:
		return t.Handle(ctx, args)
	case !errors.Is(err, apperrors.ErrRequiresApproval) || r.approvals == nil:
		return nil, err
	case !approvalID.IsZero():
		if err := r.approvals.Redeem(ctx, approvalID, spec.Name, diffHash); err != nil {
			return nil, err
		}
		return t.Handle(ctx, args)
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
		return nil, fmt.Errorf(
			"staged as approval %s — once a human approves it, repeat this exact call with \"approval_id\": %q: %w",
			id, id.String(), apperrors.ErrRequiresApproval)
	}
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

// dynamicTool is implemented by TierDynamic tools that need more than the
// raw args to resolve their tier — advance_deal reads the target stage's
// semantic from pipeline configuration, which costs a database read the
// gate should pay only for dynamic calls.
type dynamicTool interface {
	ResolverInput(ctx context.Context, in json.RawMessage) (mcp.TierResolverInput, error)
}

// UnknownToolError answers a tools/call for a name outside the surface.
type UnknownToolError struct{ Name string }

func (e *UnknownToolError) Error() string { return "unknown tool " + e.Name }
