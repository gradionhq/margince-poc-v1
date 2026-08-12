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
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

const approvalTokenHeader = "X-Approval-Token"

// maxGatedBody bounds what the gate buffers to hash and stage a proposed
// mutation; anything larger is not a plausible contract payload.
const maxGatedBody = 1 << 20

func agentGate(reg *agents.Registry, staging agents.Approvals, stages agents.StageResolver, records datasource.SystemOfRecordProvider, ownership agents.FieldOwnership, gate *auth.Gate) func(http.Handler) http.Handler {
	// ONE set of read-side dependencies for both questions this door asks of a
	// command: what tier it runs at, and what an approval of it would bind to.
	// They were two structs while the tier had its own table to feed.
	deps := restCommandDeps{records: records, stages: stages, channels: channelKinds{}}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			p, ok := principal.Actor(ctx)
			if !ok || p.Type != principal.PrincipalAgent {
				next.ServeHTTP(w, r)
				return
			}
			if !mutatingMethod(r.Method) {
				refuseAgentRead(w, r, next, gate, reg)
				return
			}
			spec, resolve, pol, body, ok := prepareAgentGate(w, r, reg, deps)
			if !ok {
				return
			}
			ctx, err := gate.Admit(ctx, spec, resolve)
			r = r.WithContext(ctx)
			// The quotas the gate just READ on this door are paid on it too, or
			// they are counters nothing increments: a credential that only ever
			// uses /v1 would sit at zero forever and no threshold could be
			// crossed. ADR-0055's claim is that both doors are governed alike,
			// and a bound enforced on one and unpaid on the other is not alike.
			//
			// Charged where the MCP door charges: the ceiling before anything
			// happens (and refusing what it cannot count), the act after it has.
			// Charged where the call is known to RUN. A plainly-admitted call is
			// charged here, before anything happens, and may still be refused if
			// it cannot be counted. A 🟡 retry is charged after its token is
			// redeemed (stageOrRedeem) — counting it here would let a caller
			// suspend its own Passport with malformed or replayed tokens that
			// open nothing.
			if err == nil {
				if chargeErr := reg.ChargeAdmittedCall(ctx, spec); chargeErr != nil {
					httperr.Write(w, r, chargeErr)
					return
				}
			}
			admitAgentCall(w, r, next, admissionOutcome{
				staging: staging, ownership: ownership, pol: pol, body: body,
				commands: deps, err: err, spec: spec, registry: reg,
			})
		})
	}
}

// refuseAgentRead answers a NON-mutating agent call: its governance class
// first, then its volume.
//
// The order is deliberate. `x-agent-access: human-only` says this route is not
// an agent's to read AT ALL, and that answer must not depend on how much the
// agent happens to have read today — a caller told "you are over your read
// budget" would reasonably retry tomorrow against a route that will never be
// theirs.
//
// The bound itself binds BOTH doors: a Passport that spent its window through
// the MCP surface must not keep reading the same records over /v1. One
// credential governed two ways is the hole ADR-0055 exists to close.
//
// And the door that refuses on the bound PAYS INTO it: the handler answers
// through a servedMeter, so the records this read hands over are charged where
// they leave. Consulting a counter nothing increments bounds nobody — a
// credential reading only over /v1 would sit at zero forever, however much it
// read.
func refuseAgentRead(w http.ResponseWriter, r *http.Request, next http.Handler, gate *auth.Gate, reg *agents.Registry) {
	if refusedAsHumanOnly(w, r) {
		return
	}
	if err := gate.AdmitRead(r.Context()); err != nil {
		httperr.Write(w, r, err)
		return
	}
	next.ServeHTTP(&servedMeter{ResponseWriter: w, r: r, reg: reg, mayRefuse: nothingHasHappenedYet}, r)
}

// refusedAsHumanOnly applies x-agent-access to a NON-mutating agent call, and
// reports whether it answered the request itself.
//
// A read has no tier to admit and no change to stage, but it does have a
// governance class, and `x-agent-access: human-only` binds a `get:` exactly
// as it binds a `post:`. auth.RequireHuman is the in-handler twin of this
// check and about a dozen human-only reads call it; the rest — attachment
// bytes and their extractions, AI call logs, voice profiles, automation run
// history, webhook subscriptions — did not, and the gate could not cover
// them because it returned early on every non-mutating method. It no longer
// does.
//
// The default is the OPPOSITE of the mutating side's, deliberately: a
// mutating route absent from the table is refused, a read absent from it is
// admitted. The table now carries every ANNOTATED read, and an unannotated
// read is ordinary agent-readable data whose authority is the granting
// human's RBAC and row scope at the store, unchanged.
func refusedAsHumanOnly(w http.ResponseWriter, r *http.Request) bool {
	pattern := chi.RouteContext(r.Context()).RoutePattern()
	pol, known := agentPolicies[r.Method+" "+pattern]
	if !known || pol.Access == accessTool {
		return false
	}
	httperr.Write(w, r, fmt.Errorf(
		"agent gate: %s is %s: %w", pol.Op, pol.Access, apperrors.ErrPermissionDenied))
	return true
}

// prepareAgentGate resolves the admission inputs for a mutating agent call:
// the op→tier policy for the route, its ToolSpec, the buffered body (reset
// onto the request for the downstream handler), and the lazy tier-resolver
// input. It writes the refusal and reports ok=false when the route is
// unknown, human-only, unresolvable, or over the body cap (fail-closed).
func prepareAgentGate(w http.ResponseWriter, r *http.Request, reg *agents.Registry, deps restCommandDeps) (mcp.ToolSpec, func() (mcp.TierResolverInput, error), agentPolicy, []byte, bool) {
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
	if pol.Access != accessTool {
		// human-only governance (self-approval class) and the
		// session/bootstrap machinery: an agent principal is rejected
		// outright, whatever its scope or seat.
		httperr.Write(w, r, fmt.Errorf(
			"agent gate: %s is %s: %w", pol.Op, pol.Access, apperrors.ErrPermissionDenied))
		return mcp.ToolSpec{}, nil, agentPolicy{}, nil, false
	}
	spec, registered, ok := operationSpec(pol, reg)
	if !ok {
		// Two faults reach here and an operator fixes them in different
		// places: an unregistered verb is a registry/contract disagreement,
		// a tier mismatch is a resolver nobody wired.
		reason := "declares a dynamic tier with no resolvable tool"
		if !registered {
			reason = "declares an agent tool the registry does not serve"
		}
		httperr.Write(w, r, fmt.Errorf(
			"agent gate: %s %s: %w", pol.Op, reason, apperrors.ErrPermissionDenied))
		return mcp.ToolSpec{}, nil, agentPolicy{}, nil, false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxGatedBody+1))
	if err != nil || len(body) > maxGatedBody {
		httperr.Write(w, r, httperr.Validation("body", "too_large", "request body unreadable or exceeds the gated limit"))
		return mcp.ToolSpec{}, nil, agentPolicy{}, nil, false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return spec, tierInput(ctx, spec, pol, deps, r, body), pol, body, true
}

// admissionOutcome carries the result of the autonomy gate's Admit call
// (err) plus the fields needed to act on it.
type admissionOutcome struct {
	staging   agents.Approvals
	ownership agents.FieldOwnership
	// commands carries what a staged call's own resolver needs to answer for
	// itself, so the target the REST door stages is the one the tool door would
	// have staged for the same operation.
	commands restCommandDeps
	pol      agentPolicy
	body     []byte
	err      error
	// spec and registry are what the effect below is charged against — the
	// tool twin this call admitted as, and the surface that owns the counters.
	spec     mcp.ToolSpec
	registry *agents.Registry
}

// admitAgentCall dispatches a mutating agent call on the admission outcome:
// admitted 🟢 work runs (a field-shaped update_record edit routes through
// the per-field owner check first); a 🟡 refusal stages or redeems the
// approval; any other admission error is surfaced as-is.
func admitAgentCall(w http.ResponseWriter, r *http.Request, next http.Handler, outcome admissionOutcome) {
	switch {
	case outcome.err == nil:
		if !pinAdmittedWrite(w, r) {
			return
		}
		// The effect is charged on BOTH arms — the field split forwards to the
		// same handler through its own path — and on NEITHER when the handler
		// refused. A quota counts what an agent did, so a rejected mutation that
		// spent a write would let a caller exhaust its own allowance on requests
		// that changed nothing, which is a bound nobody wrote.
		// The meter sits OUTSIDE the recorder, so the handler's WriteJSON finds
		// it by a plain assertion while the recorder still sees every status.
		// A mutation that answers with the row it changed handed over a record,
		// and the MCP door charges that record at chargeAnswer whatever the tool
		// kind — a read-back free on one door and charged on the other is the
		// same asymmetry this gate exists to close.
		performed := &effectRecorder{ResponseWriter: w}
		metered := &servedMeter{ResponseWriter: performed, r: r, reg: outcome.registry, mayRefuse: theEffectAlreadyLanded}
		if outcome.pol.Tool == "update_record" && !actionShapedUpdateOps[outcome.pol.Op] {
			splitOrRedeemUpdate(metered, r, next, outcome.staging, outcome.commands, outcome.ownership, outcome.pol, outcome.body)
		} else {
			next.ServeHTTP(metered, r)
		}
		if performed.done() {
			outcome.registry.ChargeEffect(r.Context(), outcome.spec)
		}
	case !errors.Is(outcome.err, apperrors.ErrRequiresApproval) || outcome.staging == nil:
		httperr.Write(w, r, outcome.err)
	default:
		// A redemption reaches the handler; a fresh staging does not. Charging
		// the effect for both would bill a refusal, so this arm charges only
		// where redeemIfPresented actually forwarded.
		performed := &effectRecorder{ResponseWriter: w}
		metered := &servedMeter{ResponseWriter: performed, r: r, reg: outcome.registry, mayRefuse: theEffectAlreadyLanded}
		ran := stageOrRedeem(metered, r, next, outcome.staging, outcome.commands, outcome.pol, outcome.body)
		if !ran {
			return
		}
		// The redemption committed before the handler ran, so this charge is
		// absorbed rather than refused — see chargeCall's refusable.
		outcome.registry.ChargeRedeemedCall(r.Context(), outcome.spec)
		if performed.done() {
			outcome.registry.ChargeEffect(r.Context(), outcome.spec)
		}
	}
}

// pinAdmittedWrite conditions an auto-executed agent write on the record state
// its tier was decided from, by forwarding the gate's pin as the request's own
// If-Match.
//
// This is redeemIfPresented's forward (agentgatestaging.go) one tier down, and
// for the same reason: the gate resolved a dynamic tier by READING the record,
// that read commits before the handler's transaction opens, and the agent
// controls both sides of the window — its own 🟢 call can close a deal between
// the two. Moving the compare inside the transaction that mutates is what makes
// a record that changed lose to the version check instead of to timing.
//
// A caller that sent its own If-Match keeps it — but only if it names the
// version the gate read, and that is CHECKED. The caller controls the header,
// so a version the gate never saw is a version nothing proved: a caller naming
// the version the racing close will PRODUCE walks straight through, because the
// store's compare then passes on precisely the record the tier decision does not
// describe. Preferring the caller unchecked would turn a coin-toss race into an
// armable one. A disagreement is answered as skew, which is also what a caller
// holding a genuinely stale version already gets, one layer down.
//
// It reports whether the request may proceed; a refusal has already been
// written.
func pinAdmittedWrite(w http.ResponseWriter, r *http.Request) bool {
	version, pinned := auth.AutoExecutePin(r.Context())
	if !pinned {
		return true
	}
	if caller := r.Header.Get("If-Match"); caller != "" {
		// Compared as the numbers they are: the contract's If-Match is a bare
		// integer version, and two spellings of one number must not read as
		// disagreement. A caller header this parser refuses is left for the
		// handler's own IfMatchVersion to answer, which is where that message
		// already lives.
		if got, err := strconv.ParseInt(caller, 10, 64); err != nil || got == version {
			return true
		}
		httperr.Write(w, r, fmt.Errorf(
			"If-Match %s is not the version this record was read at (%d) — re-read it and retry: %w",
			caller, version, apperrors.ErrVersionSkew))
		return false
	}
	r.Header.Set("If-Match", strconv.FormatInt(version, 10))
	return true
}

// effectRecorder answers whether the handler behind this door actually
// performed the effect, which is the only thing worth charging.
//
// The status is the honest signal available here: this middleware forwards to a
// generated handler it cannot otherwise ask. A handler that writes no header at
// all answered 200 by the stdlib's own rule, so an unset status counts as done —
// the alternative would leave every 200-by-default mutation free.
type effectRecorder struct {
	http.ResponseWriter
	status int
}

func (e *effectRecorder) WriteHeader(status int) {
	e.status = status
	e.ResponseWriter.WriteHeader(status)
}

// Unwrap exposes the wrapped writer so http.NewResponseController still reaches
// the real connection through this layer — an embedded-only wrapper silently
// swallows Flush and SetWriteDeadline, which the MCP route depends on.
func (e *effectRecorder) Unwrap() http.ResponseWriter { return e.ResponseWriter }

// done reports a 2xx, and treats "no header written" as the 200 it is.
func (e *effectRecorder) done() bool {
	return e.status == 0 || (e.status >= http.StatusOK && e.status < http.StatusMultipleChoices)
}

// operationSpec resolves the ToolSpec the gate admits against. The
// contract annotation may only TIGHTEN the tool's declared tier (the
// A34/ADR-0026 tighten-only rule): an op annotated 🟡 stays 🟡 even where
// the verb's base tier is 🟢 (archive-by-DELETE over update_record). A
// verb with no registered tool is REFUSED, and a dynamic annotation without
// a registered dynamic tool likewise → fail closed.
//
// This function used to synthesize a spec for an unregistered verb, and that
// invention was the defect: it had to guess a cap, and the guess was `write`,
// so verbs that fetch the web or deliver to a counterparty ran on internal
// authority. There is nothing left to guess. Every verb the contract declares
// has a registered tool — TestEveryDeclaredToolVerbIsRegistered fails the build
// otherwise — so reaching this branch means the registry and the contract
// disagree at runtime, and refusing is the only honest answer to that.
// The second result reports whether a tool was registered at all, so the
// caller can name which of the two faults it met rather than blaming a tier
// resolver for a missing tool.
func operationSpec(pol agentPolicy, reg *agents.Registry) (spec mcp.ToolSpec, registered, ok bool) {
	spec, registered = reg.Spec(pol.Tool)
	if !registered {
		return mcp.ToolSpec{}, false, false
	}
	if pol.Tier == tierDynamic && spec.Tier != mcp.TierDynamic {
		return mcp.ToolSpec{}, true, false
	}
	if pol.Tier == tierConfirmationRequired && spec.Tier != mcp.TierConfirmationRequired {
		spec.Tier, spec.TierResolver = mcp.TierConfirmationRequired, nil
	}
	return spec, true, true
}

// tierInput supplies the lazy TierResolverInput for the admitted spec.
//
// A STATIC tier passes the body through: nothing is read to decide it, so there
// is no record for the input to describe. A DYNAMIC tier is answered by the
// operation's own command — the decode restCommands already performs is the one
// parse of this request, and the call it produces answers what the tier gate is
// shown (agents.DynamicTierInput). A second table keyed by the same operations
// used to answer this, and two tables free to disagree is how one door came to
// judge a deal move by its destination alone while the other judged both ends.
//
// Both faults the dynamic path can meet are answered by the CLOSURE rather than
// by a miss the caller reports: an operation with no decoder and a command that
// answers no tier are equally "this door cannot tell whether the call needs a
// human", and the gate refuses on the error rather than admitting at a tier
// nobody resolved.
func tierInput(ctx context.Context, spec mcp.ToolSpec, pol agentPolicy, deps restCommandDeps, r *http.Request, body []byte) func() (mcp.TierResolverInput, error) {
	if spec.Tier != mcp.TierDynamic {
		return func() (mcp.TierResolverInput, error) { return mcp.TierResolverInput{Args: body}, nil }
	}
	return func() (mcp.TierResolverInput, error) {
		decode, described := restCommands[pol.Op]
		if !described {
			return mcp.TierResolverInput{}, fmt.Errorf(
				"agent gate: %s decodes into no governed call, so nothing can say whether it needs a human: %w",
				pol.Op, apperrors.ErrPermissionDenied)
		}
		call, err := decode(pol, deps, r, body)
		if err != nil {
			return mcp.TierResolverInput{}, err
		}
		return agents.DynamicTierInput(ctx, call, body)
	}
}

func mutatingMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}
