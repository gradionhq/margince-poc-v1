// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The §2.4 ladder from the tool surface's side: what an agent is told, and where
// the question goes, when it crosses a threshold a human can release.
//
// The 🟡 loop next door and this one look alike and are not. A confirm-first
// call asks a human to release ONE act, and the agent redeems that release by
// repeating the identical call with its approval_id. A step-up asks whether this
// agent may CONTINUE, and there is nothing to redeem: approving it widens the
// window, and the agent's next ordinary call succeeds. An agent told to retry
// with an approval_id here would present a token for a kind no tool can redeem.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/gradionhq/margince/backend/internal/platform/agentquota"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// QuotaReleaseRequest is what the surface asks the approvals engine to put in
// front of the human who lent this passport.
type QuotaReleaseRequest struct {
	// Proposal is the question itself, in the one spelling both modules read.
	Proposal agentquota.ReleaseProposal
	// Summary is the sentence the inbox shows.
	Summary string
}

// StepUpStagedError answers a call refused on a releasable quota once the
// question has been put to a human.
//
// It is its own type, and it does NOT unwrap to ErrRequiresApproval. That
// sentinel means "this act is confirm-first", and every transport that sees it
// tells the caller to retry with an approval_id — which for a step-up is advice
// that cannot work. It unwraps to the budget sentinel instead, which is what the
// refusal actually is: a volume threshold, now with a human looking at it.
type StepUpStagedError struct {
	ApprovalID ids.ApprovalID
	Counter    agentquota.Counter
}

func (e *StepUpStagedError) Error() string {
	return fmt.Sprintf(
		"this agent has spent its %s allowance for this window; the person who connected it has been asked whether it may continue (approval %s)",
		e.Counter, e.ApprovalID)
}

// Unwrap keeps every budget-aware caller — the REST gate, the transports' error
// mapping — seeing this for what it is.
func (e *StepUpStagedError) Unwrap() error { return apperrors.ErrBudgetExceeded }

// releasableQuotaRefusal answers the refusal a human can still say yes to, and
// nil for every other outcome — including a quota refusal on a hard stop, which
// no approval lifts and which must therefore never reach an inbox.
func releasableQuotaRefusal(err error) *auth.QuotaExceededError {
	var over *auth.QuotaExceededError
	if errors.As(err, &over) && over.Releasable() {
		return over
	}
	return nil
}

// askedOfAHuman reports whether this admission outcome ends with a question in
// somebody's inbox — the two shapes whose arguments are therefore worth
// validating first, so no human is asked to release a call that could not have
// run anyway.
func askedOfAHuman(err error) bool {
	return err == nil || errors.Is(err, apperrors.ErrRequiresApproval) || releasableQuotaRefusal(err) != nil
}

// willReachAHandler reports whether this admission outcome ends in a tool
// actually running: an admitted call, or a confirm-first refusal the caller
// answered with an approval it holds. A staged refusal and every other error
// end here, and a call that never runs is not a call to charge — counting one
// would let a caller exhaust its own ceiling with requests it was never
// allowed to make.
func willReachAHandler(err error, approvalID ids.ApprovalID, approvals Approvals) bool {
	if err == nil {
		return true
	}
	return errors.Is(err, apperrors.ErrRequiresApproval) && approvals != nil && !approvalID.IsZero()
}

// stageStepUp puts a releasable quota refusal in front of the human who lent
// this passport, and answers what the agent is told.
//
// The REFUSAL IS STILL THE ANSWER if the question cannot be asked. A surface
// with no approvals engine, a staging that fails, and a human who has already
// REJECTED this question all end the same way: the call is refused on the
// quota, exactly as it would have been. That is the conservative direction —
// the alternative is a refused call reported as an error about staging, which
// tells the agent to retry the staging rather than to stop.
//
// A human's NO is remembered, which is the whole reason this goes through the
// declined-aware staging path. An agent looping on a refusal would otherwise
// re-ask a question its human just answered, once per call, forever.
func (r *Registry) stageStepUp(ctx context.Context, refusal *auth.QuotaExceededError) error {
	if r.approvals == nil {
		return refusal
	}
	// The passport this call presented, which is the window the question is
	// about. Read from the principal rather than passed down, because it is the
	// same value the staging stamps the row with.
	actor, _ := principal.Actor(ctx)
	proposal := agentquota.NewReleaseProposal(refusal.Reading, actor.PassportID, refusal.Tool)
	id, staged, err := r.approvals.StageQuotaRelease(ctx, QuotaReleaseRequest{
		Proposal: proposal,
		Summary:  stepUpSummary(proposal),
	})
	if err != nil {
		// The refusal stands as the answer; the staging failure is ours and
		// belongs in the log, not in a message that would send the agent back to
		// retry it.
		slog.ErrorContext(ctx, "putting a step-up in front of the connecting human failed",
			"quota", string(refusal.Reading.Counter), "tool", refusal.Tool, "err", err)
		return refusal
	}
	if !staged {
		return refusal
	}
	return &StepUpStagedError{ApprovalID: id, Counter: refusal.Reading.Counter}
}

// stepUpSummary is the question, in the human's words rather than the counter's.
// It states the volume, the ceiling and the consequence of saying yes, because a
// human answering "continue?" with no numbers is answering nothing.
func stepUpSummary(p agentquota.ReleaseProposal) string {
	act := "been handed"
	unit := "records"
	if p.Counter != agentquota.Reads {
		act, unit = "made", "changes"
	}
	return fmt.Sprintf(
		"This agent has %s %d %s against a limit of %d for this window (most recently through %s). Approve to let it continue for another %d.",
		act, p.Observed, unit, p.Limit, p.Tool, p.Allowance)
}
