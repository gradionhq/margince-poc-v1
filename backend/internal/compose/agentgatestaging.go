// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The 🟡 loop on the REST door: how a confirm-first call is staged for a human,
// and how the retry that carries their decision is redeemed.
//
// Split from agentgate.go on the 500-line cap, along the boundary that was
// already there: agentgate.go decides whether a call is ADMITTED, and this is
// what happens to the one answer that is neither yes nor no. The MCP door's
// twin of this file is modules/agents/approvals.go, and the two must agree on
// what "the identical call" means even though each hashes it through its own
// spelling — the MCP door through shared/kernel/diffhash
// (modules/agents/reserved.go), this door through its own canonicalRESTCall
// (agentgatecanon.go), because a REST call carries a method, a concrete path
// and headers that a tool call's arguments object has no place for.

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// stageOrRedeem handles the 🟡 outcome. The identical call is the
// redemption key — a content hash over operation + concrete path +
// canonicalized body, computed the same way at staging and at retry: an
// X-Approval-Token redeems a previously approved identical call and lets
// it through; otherwise the call is staged as a new approval and refused
// with the redemption instructions.
// It reports whether the call reached the HANDLER, which is what decides
// whether an effect was performed and so whether one is charged. A redemption
// that forwarded reports true; a refused token and a fresh staging both report
// false, because neither ran anything.
func stageOrRedeem(w http.ResponseWriter, r *http.Request, next http.Handler, staging agents.Approvals, commands restCommandDeps, pol agentPolicy, body []byte) bool {
	handled, ran := redeemIfPresented(w, r, next, staging, pol, body)
	if handled {
		return ran
	}
	stageRefusal(w, r, staging, commands, pol, body)
	return false
}

// tokenRedemption is what consuming a presented X-Approval-Token yielded: the
// context marked as released — the marker the seam's egress backstop reads, and
// the only proof a human released THIS call — plus the version the approval was
// granted against.
type tokenRedemption struct {
	released context.Context
	pin      int64
	pinned   bool
}

// presentedApprovalToken answers the X-Approval-Token this request asserts, or
// "" when it asserts none.
//
// One accessor, because two parts of the gate must agree about whether a call is
// a redemption and they are far apart: the charge point skips its pre-dispatch
// charge for one (agentGate, agentgate.go), and this file spends it. Two
// readings of one header is how the door comes to charge a call it then refuses
// to run.
func presentedApprovalToken(r *http.Request) string {
	return r.Header.Get(approvalTokenHeader)
}

// consumePresentedToken validates and consumes the X-Approval-Token on this
// request: a valid token bound to this exact call is spent, and an invalid one
// is answered with the failure — asserted authority is validated, never ignored.
//
// It answers THREE things, because they are three different facts and both arms
// of the gate need all of them: `presented` says the request asserted an
// authority at all, `ok` says that assertion held, and the redemption carries
// what the release proved. Nothing here forwards to a handler — what a released
// call is then conditioned on differs between the two arms (agentgateauto.go's
// pinAutoExecutedWrite and redeemIfPresented below), and folding the dispatch in
// here is what left one arm redeeming and the other ignoring the same header.
func consumePresentedToken(w http.ResponseWriter, r *http.Request, staging agents.Approvals, pol agentPolicy, body []byte) (redemption tokenRedemption, presented, ok bool) {
	token := presentedApprovalToken(r)
	if token == "" {
		return tokenRedemption{}, false, false
	}
	approvalID, pErr := ids.ParseAs[ids.ApprovalKind](token)
	if pErr != nil {
		httperr.Write(w, r, fmt.Errorf("agent gate: malformed %s: %w", approvalTokenHeader, apperrors.ErrApprovalTokenInvalid))
		return tokenRedemption{}, true, false
	}
	_, diffHash, cErr := canonicalRESTCall(pol.Op, r.URL.Path, r.Header, body, keyBindsTheRetry)
	if cErr != nil {
		httperr.Write(w, r, cErr)
		return tokenRedemption{}, true, false
	}
	if staging == nil {
		httperr.Write(w, r, fmt.Errorf("agent gate: %s presented but this surface has no approvals engine: %w",
			approvalTokenHeader, apperrors.ErrApprovalTokenInvalid))
		return tokenRedemption{}, true, false
	}
	// Redeeming and marking are one step (agents.RedeemAndMark), so this
	// transport cannot forward an approved call without the released marker the
	// seam's egress backstop reads — nor obtain that marker without redeeming.
	released, pin, pinned, rErr := agents.RedeemAndMark(r.Context(), staging, approvalID, pol.Tool, diffHash)
	if rErr != nil {
		httperr.Write(w, r, rErr)
		return tokenRedemption{}, true, false
	}
	return tokenRedemption{released: released, pin: pin, pinned: pinned}, true, true
}

// redeemIfPresented is the 🟡 arm's use of the redemption above: a released call
// goes through to the handler, carrying the approval's pin as its own If-Match.
//
// It answers TWO things, because they are two different facts and a caller
// needs both: `handled` says the request has been answered and the caller
// should stop, `ran` says a handler actually executed. They differ on every
// refusal path — a malformed token is handled and ran nothing — and the
// difference is what decides whether an effect is charged. Reading one off the
// other (by inspecting the status already written, say) would be a second
// reading of one value, which is how these two come to disagree.
//
// The released pin OVERRIDES a caller's own If-Match here, where the 🟢 arm
// checks the two against each other instead (pinAutoExecutedWrite). The two
// arms differ because the caller's header has been proved against something on
// one and against nothing on the other: a 🟢 admission read the record itself
// and pinAutoExecutedWrite already refused an If-Match naming a version it did
// not read, while a 🟡 call was admitted by a human's decision and this door hashes
// no If-Match into the identity that decision bound (agentgatecanon.go says
// why). The approval's own pin is then the only version anything proved.
func redeemIfPresented(w http.ResponseWriter, r *http.Request, next http.Handler, staging agents.Approvals, pol agentPolicy, body []byte) (handled, ran bool) {
	redemption, presented, ok := consumePresentedToken(w, r, staging, pol, body)
	if !presented {
		return false, false
	}
	if !ok {
		return true, false
	}
	// Redemption commits its OWN transaction, and the handler below opens a
	// fresh one to write. The skew check inside the redemption therefore
	// proves the row was at the pinned version when the approval was
	// consumed, not that it still is when the effect lands — and the attacker
	// controls both sides of that window, since the redeeming request and any
	// racing auto-execute mutation come from the same agent. Carrying the pin
	// forward as the request's own If-Match makes the store re-check it
	// inside the transaction that actually mutates, where a concurrent write
	// loses to the version compare instead of to timing.
	if redemption.pinned {
		r.Header.Set(ifMatchHeader, strconv.FormatInt(redemption.pin, 10))
	}
	// WithContext shares the header map set just above, so the pin travels with
	// the released request.
	next.ServeHTTP(w, r.WithContext(redemption.released))
	return true, true
}

// stageRefusal stages the refused call as a pending approval and answers
// with the redemption instructions — the whole request, unapplied, is the
// staged change, so the approved retry is this exact request again.
func stageRefusal(w http.ResponseWriter, r *http.Request, staging agents.Approvals, commands restCommandDeps, pol agentPolicy, body []byte) {
	ctx := r.Context()
	canonical, diffHash, cErr := canonicalRESTCall(pol.Op, r.URL.Path, r.Header, body, keyBindsTheRetry)
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
	target, ok := stagedTarget(w, r, commands, pol, body)
	if !ok {
		return
	}
	// The version a human approves is pinned SERVER-SIDE, inside the staging
	// transaction, by approvals.StageInTx — the one place every stager passes
	// through, so the REST gate, the MCP tool twins and the automation engine
	// cannot each get it differently. The gate deliberately passes NO pin of
	// its own: the only one it could offer is the agent's own If-Match header,
	// which is optional, and an agent that simply omitted it staged
	// target_version NULL — a NULL the redemption skew check short-circuits
	// on. A create (no target id) has nothing to pin, and says so by carrying
	// a zero id.
	approvalID, sErr := staging.Stage(ctx, agents.StageRequest{
		Tool:           pol.Tool,
		ProposedChange: canonical,
		DiffHash:       diffHash,
		TargetType:     target.TargetType,
		TargetID:       target.TargetID,
		Summary:        restSummary(pol.Op, r.Method, r.URL.Path, body),
	})
	if sErr != nil {
		httperr.Write(w, r, sErr)
		return
	}
	httperr.Write(w, r, fmt.Errorf(
		"staged as approval %s — once a human approves it, repeat this exact request with the %s: %s header: %w",
		approvalID, approvalTokenHeader, approvalID, apperrors.ErrRequiresApproval))
}

// stagedTarget answers what the approval binds to: the record type and the id
// the human's decision will be scoped and pinned by.
//
// Every operation this door can stage decodes into a typed command
// (agentcommand.go) and is answered by the SAME resolver the tool door asks, so
// the two cannot describe one operation differently — and the resolver's guards
// run here too, refusing a target the caller cannot see before a human is asked
// about it.
//
// Only the target is taken from the resolver. The line the human reads stays
// this door's own (restSummary), because a REST summary names the concrete
// method, path and body fields that this door's diff_hash actually binds.
// Adopting the resolver's line on this door as well is blocked on the six types
// the record seam does not serve: it can label those by id alone, so the change
// would put "Archive tag 0195c3…" in front of an approver where the operation
// and path stand today (gradionhq/margince-poc-v1#1021 is where those types get
// a name).
//
// It writes the refusal itself and reports ok=false, so a caller that cannot
// name a target stages nothing.
func stagedTarget(w http.ResponseWriter, r *http.Request, commands restCommandDeps, pol agentPolicy, body []byte) (agents.StageInfo, bool) {
	info, ok := resolveStagedTarget(w, r, commands, pol, body)
	if !ok {
		return agents.StageInfo{}, false
	}
	// A concrete target with no record type is unstageable authority: the
	// approvals surface scopes an inbox row by probing its target's own/team
	// visibility, and it cannot probe a type it was not told. Such a row
	// would show a record's summary and proposed change to everyone holding
	// the object grant, and let any of them decide a write against a row
	// their own scope hides. Refuse it here, the same fail-closed shape as
	// an undecidable kind, rather than mint an unscopable authority object.
	//
	// Applied to EVERY answer rather than inside one branch: a resolver names
	// its own target, so a decoder wired to an operation that declares no
	// record type would otherwise walk straight past the check that catches it.
	if !info.TargetID.IsZero() && info.TargetType == "" {
		httperr.Write(w, r, fmt.Errorf(
			"agent gate: %s stages against a concrete record but declares no record type: %w",
			pol.Op, apperrors.ErrPermissionDenied))
		return agents.StageInfo{}, false
	}
	return info, true
}

// resolveStagedTarget answers the staged target from the operation's own
// resolver, which every agent-reachable mutating operation now has
// (TestEveryAgentReachableMutatingRouteDecodesIntoACommand derives that from
// the policy table, so a route the contract adds fails there rather than
// arriving here undescribed).
//
// An operation with no entry is a defect this door refuses rather than guesses
// around. The guess it used to fall back to — the route's own {id} paired with
// the policy's declared record type — is what this seam replaced: it read the
// merge's routed id as the survivor, gave createOffer the deal's id as an
// offer's, and could not tell the two enrich depths apart, all while looking
// exactly as green as a real answer.
//
// body is the same buffered copy stageRefusal already hashed into
// canonicalRESTCall — passed through rather than re-read off r.Body, so a
// decoder that needs the request payload (create, patch) is a pure function
// of values the gate already proved readable, with no second reader of a
// stream nothing guarantees stays replayable.
func resolveStagedTarget(w http.ResponseWriter, r *http.Request, commands restCommandDeps, pol agentPolicy, body []byte) (agents.StageInfo, bool) {
	decode, described := restCommands[pol.Op]
	if !described {
		httperr.Write(w, r, fmt.Errorf(
			"agent gate: %s decodes into no governed call, so nothing can say what an approval of it would "+
				"bind to: %w", pol.Op, apperrors.ErrPermissionDenied))
		return agents.StageInfo{}, false
	}
	call, err := decode(pol, commands, r, body)
	if err != nil {
		httperr.Write(w, r, err)
		return agents.StageInfo{}, false
	}
	info, err := agents.StageSubject(r.Context(), call)
	if err != nil {
		httperr.Write(w, r, err)
		return agents.StageInfo{}, false
	}
	return info, true
}
