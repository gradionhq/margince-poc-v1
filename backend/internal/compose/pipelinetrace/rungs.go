// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package pipelinetrace

// What each stage says about one message.
//
// Every branch here answers the same question — what can this rung HONESTLY
// claim — and the recurring wrong answer is silence. A stage with no row is not
// a stage that did not run.
//
// Three answers, and the boundaries between them are the whole file:
//
//   - it did not apply, which the rows present must support;
//   - it ran, with what it concluded;
//   - we cannot tell, which covers BOTH a swept window and rows that are not
//     the reader's. Those two are deliberately one sentence: distinguishing
//     them would tell a non-owner whether a row exists.

import (
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	trace "github.com/gradionhq/margince/backend/internal/shared/kernel/pipelinetrace"
)

// rung answers one registered stage.
func (a *Assembler) rung(reg trace.Registration, v view, facts *activities.PipelineFacts) Rung {
	stored, owned := v.stored, v.owned
	out := Rung{
		Stage:       reg.Stage,
		Order:       reg.Order,
		SubjectKind: reg.SubjectKind,
	}
	// A stage that reports nothing says so with its own reason. The four
	// absences are NOT interchangeable: "we will never show this" and "this step
	// does not exist yet" are different facts about the product, and collapsing
	// them would tell a member the wrong one.
	if reg.AbsentReason != "" {
		out.Status, out.Reason = trace.StatusNotReported, reg.AbsentReason
		return out
	}
	switch reg.Stage {
	case trace.StageInternalDrop:
		return storedRung(out, stored, owned, trace.StageInternalDrop)
	case trace.StageActivityWrite:
		return activityWriteRung(out, v)
	case trace.StageTierLadder:
		return storedRung(out, stored, owned, trace.StageTierLadder)
	case trace.StagePersonCreate:
		return personCreateRung(out, v, facts)
	case trace.StageVerdict:
		return verdictRung(out, v)
	case trace.StageAttentionLabel:
		return attentionLabelRung(out, v, facts)
	}
	// A registered stage with no branch here. TestEveryAnsweringStageHasABranch
	// walks the registry and fails on exactly this, so it is unreachable — but
	// an unknown rung beats a zero-value one that would read as a real state.
	out.Status = trace.StatusUnknown
	return out
}

// storedRung renders a stage whose answer is a capture_trace row.
func storedRung(out Rung, stored capture.TraceLadder, owned bool, stage trace.Stage) Rung {
	if !owned {
		// Unconditional, whether or not a row exists — the answer must not vary
		// with what is there, or a colleague comparing two shared messages
		// would learn which of them faulted on somebody else's mailbox.
		//
		// `unknown` rather than a "not yours to see" state, because the caller
		// here is frequently the message's OWN OWNER reading past the 24-hour
		// sweep: their rows were deleted, not hidden, and this is the only
		// wording true of both readers.
		return unavailable(out)
	}
	row, found := findRung(stored, stage)
	if !found {
		return notApplicableOrUnknown(out, stored)
	}
	out.Status = statusForOutcome(row.Outcome)
	out.Reason = trace.Reason(row.Reason)
	out.At = stamp(row.OccurredAt)
	out.Counterparty, out.Subject = row.Counterparty, row.Subject
	return out
}

// activityWriteRung is the one hybrid stage: its success is the activity's own
// existence, and its single failure mode leaves a row and no activity.
func activityWriteRung(out Rung, v view) Rung {
	stored, owned := v.stored, v.owned
	if v.activityHidden {
		// The activity exists and this reader may not open it. Neither `done`
		// (which would confirm it exists) nor `not_applicable` (which would say
		// the write never happened, contradicting the captured rung beside it)
		// is true here.
		return unavailable(out)
	}
	if row, found := findRung(stored, trace.StageActivityWrite); found && owned {
		out.Status = trace.StatusFailed
		out.Reason = trace.Reason(row.Reason)
		out.At = stamp(row.OccurredAt)
		return out
	}
	// Derived, so it answers for ANY caller who reached this activity: the row
	// existing is proof the write happened, and that is ordinary product state
	// rather than one member's diagnostic row.
	if stored.ActivityID != nil {
		out.Status = trace.StatusDone
		return out
	}
	if !owned {
		return unavailable(out)
	}
	// No activity and no fault row, with the rows visible: the message was
	// dropped before the write was ever attempted.
	return notApplicableOrUnknown(out, stored)
}

// personCreateRung is derived by ELIMINATION. There is no stored "the ladder
// decided to create a contact" — the ladder decides it in memory and explicitly
// refuses to re-derive it downstream — so this reads the person link, and falls
// back to what the ladder's own rung concluded.
func personCreateRung(out Rung, v view, facts *activities.PipelineFacts) Rung {
	stored, owned := v.stored, v.owned
	if v.activityHidden {
		// The activity exists and is not this reader's to open, so nothing
		// derived from it may be reported — including "no contact was made".
		return unavailable(out)
	}
	if facts == nil {
		// No activity at all: the message never reached the step.
		return notApplicableOrUnknown(out, stored)
	}
	if facts.HasPersonLink {
		out.Status = trace.StatusDone
		return out
	}
	ladder, found := findRung(stored, trace.StageTierLadder)
	if !owned || !found {
		// This rung is derived FROM a stored row, so it inherits that row's
		// availability: a caller who cannot see the ladder's decision must not
		// learn it through this one. Linked-or-not is all that can be said.
		return unavailable(out)
	}
	if noContactIntended(ladder.Outcome) {
		out.Status, out.Reason = trace.StatusNotApplicable, trace.ReasonNoContactIntended
		return out
	}
	// A contact was intended and none is linked. This promises nothing about
	// when: the nightly reconcile re-runs the resolver over link-less
	// activities, but a channel identity conflict stages a human review the
	// resolver will never clear, so "tonight" would be false indefinitely for
	// exactly those messages.
	out.Status, out.Reason = trace.StatusPending, trace.ReasonNotLinkedYet
	return out
}

// verdictRung reports the SENDER's disposition, which the ledger owns. It is
// read through the stored row's join rather than copied, because one sender's
// answer covers every message they sent and a copy would collide with itself the
// moment they were re-judged.
func verdictRung(out Rung, v view) Rung {
	stored, owned := v.stored, v.owned
	if !owned {
		return unavailable(out)
	}
	resolution := findResolution(stored)
	if resolution == nil {
		out.Status, out.Reason = trace.StatusNotApplicable, trace.ReasonNoOpenQuestion
		return out
	}
	// The open set is the kernel's, not a third literal copy of the ledger's
	// vocabulary. And an UNRECOGNISED status is not a reached verdict: a state
	// this build has never seen would otherwise render as "a verdict has been
	// reached" for a sender still being judged.
	switch {
	case trace.IsOpenDisposition(resolution.Status):
		out.Status, out.Reason = trace.StatusPending, trace.ReasonAwaitingVerdict
	case isSettledDisposition(resolution.Status):
		out.Status, out.Reason = trace.StatusDone, trace.ReasonVerdictReached
	default:
		return unavailable(out)
	}
	if resolution.ResolvedAt != nil {
		out.At = stamp(*resolution.ResolvedAt)
	}
	return out
}

// attentionLabelRung is the stage whose silence motivated this surface.
//
// Its eligibility is not decided here: activities owns the backlog predicate and
// answers with the class that excluded this message, so the sentence a member
// reads changes when the rule does.
func attentionLabelRung(out Rung, v view, facts *activities.PipelineFacts) Rung {
	if v.activityHidden {
		return unavailable(out)
	}
	if facts == nil {
		// No activity: there was nothing for the classifier to read.
		out.Status = trace.StatusNotApplicable
		return out
	}
	if facts.CaptureLabel != "" {
		out.Status, out.Reason = trace.StatusDone, trace.ReasonLabelled
		return out
	}
	out.Reason = facts.ClassifyReason
	if facts.ClassifyEligible {
		out.Status = trace.StatusPending
		return out
	}
	out.Status = trace.StatusSkipped
	return out
}

// notApplicableOrUnknown is the distinction the retention window forces.
//
// With rows present for this message, an absent rung means the stage did not
// run. With NO rows at all the window has swept them, and "did not run" is a
// claim the data cannot support — absence and never-happened are
// indistinguishable, so the honest answer is that we no longer know.
func notApplicableOrUnknown(out Rung, stored capture.TraceLadder) Rung {
	if len(stored.Rungs) == 0 {
		return unavailable(out)
	}
	out.Status = trace.StatusNotApplicable
	return out
}

// statusForOutcome maps capture's own vocabulary onto the reader's.
func statusForOutcome(outcome string) trace.Status {
	switch outcome {
	case string(capture.TraceCaptured):
		return trace.StatusDone
	case string(capture.TraceFault):
		return trace.StatusFailed
	case string(capture.TraceInternal), string(capture.TraceSuppressed),
		string(capture.TraceDeferred):
		// Each of these is the pipeline DECLINING to go further, which is what
		// skipped means. The reason beside it says which decline it was.
		return trace.StatusSkipped
	default:
		return trace.StatusUnknown
	}
}

// noContactIntended reports whether the ladder concluded that no contact was to
// be made, which is what turns an unlinked message from a pending repair into a
// finished decision.
func noContactIntended(outcome string) bool {
	return outcome == string(capture.TraceSuppressed) ||
		outcome == string(capture.TraceDeferred) ||
		outcome == string(capture.TraceInternal)
}

// unavailable is the one answer for every reason this surface cannot say what
// happened — a swept window, or rows that are not the reader's. Spelled once so
// the two can never diverge into answers a reader could tell apart.
func unavailable(out Rung) Rung {
	out.Status, out.Reason = trace.StatusUnknown, trace.ReasonRecordNotAvailable
	return out
}

// isSettledDisposition names the ledger states that ARE an answer.
//
// It reads capture's own constants rather than restating them: the ledger owns
// this vocabulary, and a literal copy here is the drift the trace exists to
// expose, reproduced inside the trace.
//
// Listed rather than derived as "not open", so a status added to the ledger
// reaches the default branch and reports that we cannot tell, instead of being
// read as a verdict nobody reached.
func isSettledDisposition(status string) bool {
	switch status {
	case capture.PendingStatusReal, capture.PendingStatusNoise,
		capture.PendingStatusRejected, capture.PendingStatusSuppressed:
		return true
	default:
		return false
	}
}

func findRung(stored capture.TraceLadder, stage trace.Stage) (capture.TraceRow, bool) {
	for _, r := range stored.Rungs {
		if r.Stage == string(stage) {
			return r, true
		}
	}
	return capture.TraceRow{}, false
}

func findResolution(stored capture.TraceLadder) *capture.TraceResolution {
	for _, r := range stored.Rungs {
		if r.Resolution != nil {
			return r.Resolution
		}
	}
	return nil
}

func stamp(t time.Time) *time.Time {
	utc := t.UTC()
	return &utc
}
