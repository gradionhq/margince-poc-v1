// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The ADR-0055 REST admission layer. Autonomy admission is
// transport-agnostic: a mutating REST call by an AGENT (Passport)
// principal resolves to the SAME 🟢/🟡 tier declared for its MCP tool twin
// and, when 🟡, stages the SAME approval a refused tool call would —
// approved work is redeemed by repeating the identical request with the
// X-Approval-Token header. The generated agentPolicies table (from the
// contract's x-mcp-tool / x-agent-access annotations) is the op→tier map;
// a mutating route with no entry is REFUSED for agents (fail-closed), and
// human-only governance operations (approval decisions, consent, DSR,
// pipeline/stage config) reject an agent outright — an agent may stage a
// 🟡 action but never approve one, including its own.
//
// Human callers never enter this path: their authority is RBAC at the
// store, and a human's direct call is itself the approval.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

const approvalTokenHeader = "X-Approval-Token"

// maxGatedBody bounds what the gate buffers to hash and stage a proposed
// mutation; anything larger is not a plausible contract payload.
const maxGatedBody = 1 << 20

func agentGate(reg *agents.Registry, staging agents.Approvals, stages agents.StageResolver, ownership agents.FieldOwnership, gate *auth.Gate) func(http.Handler) http.Handler {
	deps := tierDeps{stages: stages, ownership: ownership}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			p, ok := principal.Actor(ctx)
			if !ok || p.Type != principal.PrincipalAgent || !mutatingMethod(r.Method) {
				next.ServeHTTP(w, r)
				return
			}
			spec, resolve, pol, body, ok := prepareAgentGate(w, r, reg, deps)
			if !ok {
				return
			}
			ctx, err := gate.Admit(ctx, spec, resolve)
			r = r.WithContext(ctx)
			admitAgentCall(w, r, next, staging, ownership, pol, body, err)
		})
	}
}

// prepareAgentGate resolves the admission inputs for a mutating agent call:
// the op→tier policy for the route, its ToolSpec, the buffered body (reset
// onto the request for the downstream handler), and the lazy tier-resolver
// input. It writes the refusal and reports ok=false when the route is
// unknown, human-only, unresolvable, or over the body cap (fail-closed).
func prepareAgentGate(w http.ResponseWriter, r *http.Request, reg *agents.Registry, deps tierDeps) (mcp.ToolSpec, func() (mcp.TierResolverInput, error), agentPolicy, []byte, bool) {
	ctx := r.Context()
	// The generated table is keyed by the chi route pattern the contract
	// router registered; a mutating route it doesn't know is refused, never
	// admitted ungated (ADR-0055 §2).
	pattern := chi.RouteContext(ctx).RoutePattern()
	pol, known := agentPolicies[r.Method+" "+pattern]
	if !known {
		httperr.Write(w, r, fmt.Errorf(
			"agent gate: %s %s carries no autonomy tier: %w", r.Method, pattern, apperrors.ErrPermissionDenied))
		return mcp.ToolSpec{}, nil, agentPolicy{}, nil, false
	}
	if pol.Access != "tool" {
		// human-only governance (self-approval class) and the
		// session/bootstrap machinery: an agent principal is rejected
		// outright, whatever its scope or seat.
		httperr.Write(w, r, fmt.Errorf(
			"agent gate: %s is %s: %w", pol.Op, pol.Access, apperrors.ErrPermissionDenied))
		return mcp.ToolSpec{}, nil, agentPolicy{}, nil, false
	}
	spec, ok := operationSpec(pol, reg)
	if !ok {
		httperr.Write(w, r, fmt.Errorf(
			"agent gate: %s declares a dynamic tier with no resolvable tool: %w", pol.Op, apperrors.ErrPermissionDenied))
		return mcp.ToolSpec{}, nil, agentPolicy{}, nil, false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxGatedBody+1))
	if err != nil || len(body) > maxGatedBody {
		httperr.Write(w, r, httperr.Validation("body", "too_large", "request body unreadable or exceeds the gated limit"))
		return mcp.ToolSpec{}, nil, agentPolicy{}, nil, false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	resolve, ok := tierInput(ctx, spec, pol, deps, r, body)
	if !ok {
		httperr.Write(w, r, fmt.Errorf(
			"agent gate: %s: no REST tier resolver for dynamic tool %s: %w", pol.Op, pol.Tool, apperrors.ErrPermissionDenied))
		return mcp.ToolSpec{}, nil, agentPolicy{}, nil, false
	}
	return spec, resolve, pol, body, true
}

// admitAgentCall dispatches a mutating agent call on the admission outcome:
// admitted 🟢 work runs (a field-shaped update_record edit routes through
// the per-field owner check first); a 🟡 refusal stages or redeems the
// approval; any other admission error is surfaced as-is.
func admitAgentCall(w http.ResponseWriter, r *http.Request, next http.Handler, staging agents.Approvals, ownership agents.FieldOwnership, pol agentPolicy, body []byte, err error) {
	switch {
	case err == nil:
		if pol.Tool == "update_record" && !actionShapedUpdateOps[pol.Op] {
			splitOrRedeemUpdate(w, r, next, staging, ownership, pol, body)
			return
		}
		next.ServeHTTP(w, r)
	case !errors.Is(err, apperrors.ErrRequiresApproval) || staging == nil:
		httperr.Write(w, r, err)
	default:
		stageOrRedeem(w, r, next, staging, pol, body)
	}
}

// stageOrRedeem handles the 🟡 outcome. The identical call is the
// redemption key — a content hash over operation + concrete path +
// canonicalized body, computed the same way at staging and at retry: an
// X-Approval-Token redeems a previously approved identical call and lets
// it through; otherwise the call is staged as a new approval and refused
// with the redemption instructions.
func stageOrRedeem(w http.ResponseWriter, r *http.Request, next http.Handler, staging agents.Approvals, pol agentPolicy, body []byte) {
	if redeemIfPresented(w, r, next, staging, pol, body) {
		return
	}
	stageRefusal(w, r, staging, pol, body)
}

// redeemIfPresented consumes an X-Approval-Token when the request carries
// one: a valid token bound to this exact call lets it through to the
// handler; an invalid one is answered with the failure — asserted
// authority is validated, never ignored. Reports whether the request was
// fully handled (no token → false, the caller continues its own flow).
func redeemIfPresented(w http.ResponseWriter, r *http.Request, next http.Handler, staging agents.Approvals, pol agentPolicy, body []byte) bool {
	token := r.Header.Get(approvalTokenHeader)
	if token == "" {
		return false
	}
	approvalID, pErr := ids.Parse(token)
	if pErr != nil {
		httperr.Write(w, r, fmt.Errorf("agent gate: malformed %s: %w", approvalTokenHeader, apperrors.ErrApprovalTokenInvalid))
		return true
	}
	_, diffHash, cErr := canonicalRESTCall(pol.Op, r.URL.Path, body)
	if cErr != nil {
		httperr.Write(w, r, cErr)
		return true
	}
	if staging == nil {
		httperr.Write(w, r, fmt.Errorf("agent gate: %s presented but this surface has no approvals engine: %w",
			approvalTokenHeader, apperrors.ErrApprovalTokenInvalid))
		return true
	}
	if rErr := staging.Redeem(r.Context(), approvalID, pol.Tool, diffHash); rErr != nil {
		httperr.Write(w, r, rErr)
		return true
	}
	next.ServeHTTP(w, r)
	return true
}

// stageRefusal stages the refused call as a pending approval and answers
// with the redemption instructions — the whole request, unapplied, is the
// staged change, so the approved retry is this exact request again.
func stageRefusal(w http.ResponseWriter, r *http.Request, staging agents.Approvals, pol agentPolicy, body []byte) {
	ctx := r.Context()
	canonical, diffHash, cErr := canonicalRESTCall(pol.Op, r.URL.Path, body)
	if cErr != nil {
		httperr.Write(w, r, cErr)
		return
	}
	// Stage only what a human can actually decide: a kind with no
	// decision-grant mapping would sit undecidable in every inbox
	// — refuse instead of minting a zombie authority object.
	if !approvals.KindHasDecisionGrants(pol.Tool) {
		httperr.Write(w, r, fmt.Errorf(
			"agent gate: %s (%s) has no approval decision mapping: %w", pol.Op, pol.Tool, apperrors.ErrPermissionDenied))
		return
	}
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	var targetID ids.UUID
	if raw := chi.URLParam(r, "id"); raw != "" {
		var err error
		if targetID, err = ids.Parse(raw); err != nil {
			httperr.Write(w, r, apperrors.ErrNotFound)
			return
		}
	}
	approvalID, sErr := staging.Stage(ctx, agents.StageRequest{
		Tool:           pol.Tool,
		ProposedChange: canonical,
		DiffHash:       diffHash,
		TargetType:     pol.RecordType,
		TargetID:       targetID,
		TargetVersion:  ifVersion,
		Summary:        fmt.Sprintf("Agent REST %s %s", r.Method, r.URL.Path),
	})
	if sErr != nil {
		httperr.Write(w, r, sErr)
		return
	}
	httperr.Write(w, r, fmt.Errorf(
		"staged as approval %s — once a human approves it, repeat this exact request with the %s: %s header: %w",
		approvalID, approvalTokenHeader, approvalID, apperrors.ErrRequiresApproval))
}

// operationSpec resolves the ToolSpec the gate admits against. The
// contract annotation may only TIGHTEN the tool's declared tier (the
// A34/ADR-0026 tighten-only rule): an op annotated 🟡 stays 🟡 even where
// the verb's base tier is 🟢 (archive-by-DELETE over update_record). A
// verb with no registered tool is admitted at the annotation's static
// tier under the write scope; a dynamic annotation without a registered
// dynamic tool is unresolvable → fail closed.
func operationSpec(pol agentPolicy, reg *agents.Registry) (mcp.ToolSpec, bool) {
	spec, registered := reg.Spec(pol.Tool)
	if !registered {
		if pol.Tier == "dynamic" {
			return mcp.ToolSpec{}, false
		}
		tier := mcp.TierGreen
		if pol.Tier == "yellow" {
			tier = mcp.TierYellow
		}
		return mcp.ToolSpec{Name: pol.Tool, RequiredScope: principal.ScopeWrite, Tier: tier}, true
	}
	if pol.Tier == "dynamic" && spec.Tier != mcp.TierDynamic {
		return mcp.ToolSpec{}, false
	}
	if pol.Tier == "yellow" && spec.Tier != mcp.TierYellow {
		spec.Tier, spec.TierResolver = mcp.TierYellow, nil
	}
	return spec, true
}

// tierDeps carries the read-side dependencies the dynamic REST tier
// resolvers consult.
type tierDeps struct {
	stages    agents.StageResolver
	ownership agents.FieldOwnership
}

// dynamicTierInputs maps each dynamic tool onto the resolver that reads
// its tier decision out of the tool's REST body shape. The invariant: a
// dynamic tool without an entry here has no REST twin the gate knows how
// to interpret — its tier question cannot be answered, so tierInput
// reports a miss and the caller refuses the request (fail-closed).
var dynamicTierInputs = map[string]func(ctx context.Context, deps tierDeps, pol agentPolicy, r *http.Request, body []byte) (mcp.TierResolverInput, error){
	"advance_deal": advanceDealTierInput,
}

// advanceDealTierInput: 🟢/🟡 turns on whether the destination stage is a
// closing stage, so the resolver needs the concrete stage's semantic.
func advanceDealTierInput(ctx context.Context, deps tierDeps, _ agentPolicy, _ *http.Request, body []byte) (mcp.TierResolverInput, error) {
	var args struct {
		ToStageID ids.UUID `json:"to_stage_id"`
	}
	if err := json.Unmarshal(body, &args); err != nil || args.ToStageID.IsZero() {
		return mcp.TierResolverInput{}, httperr.Validation("to_stage_id", "required", "to_stage_id must be a stage UUID")
	}
	semantic, pipelineID, err := deps.stages.StageSemantic(ctx, args.ToStageID)
	if err != nil {
		return mcp.TierResolverInput{}, err
	}
	return mcp.TierResolverInput{Args: body, TargetStageSemantic: semantic, PipelineID: pipelineID.String()}, nil
}

// tierInput supplies the lazy TierResolverInput for the admitted spec:
// static tiers pass the body through; dynamic tiers dispatch through
// dynamicTierInputs and report a miss for the caller to refuse.
func tierInput(ctx context.Context, spec mcp.ToolSpec, pol agentPolicy, deps tierDeps, r *http.Request, body []byte) (func() (mcp.TierResolverInput, error), bool) {
	if spec.Tier != mcp.TierDynamic {
		return func() (mcp.TierResolverInput, error) { return mcp.TierResolverInput{Args: body}, nil }, true
	}
	resolve, known := dynamicTierInputs[pol.Tool]
	if !known {
		return nil, false
	}
	return func() (mcp.TierResolverInput, error) {
		return resolve(ctx, deps, pol, r, body)
	}, true
}

// canonicalRESTCall canonicalizes the request into the bytes both staging
// and redemption hash: decoding into maps and re-marshaling sorts keys at
// every depth, so "identical call" is a property of content, not of the
// client's serialization habits.
func canonicalRESTCall(op, path string, body []byte) (json.RawMessage, string, error) {
	var payload any
	if len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, "", httperr.Validation("body", "invalid_json", "request body must be valid JSON")
		}
	}
	canonical, err := json.Marshal(map[string]any{"operation": op, "path": path, "body": payload})
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(canonical)
	return canonical, hex.EncodeToString(sum[:]), nil
}

func mutatingMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}
