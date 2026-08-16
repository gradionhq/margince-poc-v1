// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package pipelinetrace

// What each stage says about one message.
//
// Every branch here answers the same question — what can this rung HONESTLY
// claim — and the recurring wrong answer is silence. A stage with no row is not
// a stage that did not run; it may be one whose row was swept, one the caller
// may not see, or one that never applied. Those are four different sentences and
// this file is where they are kept apart.

import (
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	trace "github.com/gradionhq/margince/backend/internal/shared/kernel/pipelinetrace"
)

// rung answers one registered stage.
func (a *Assembler) rung(reg trace.Registration, stored capture.TraceLadder,
	facts *activities.PipelineFacts, owned bool,
) Rung {
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
		return a.storedRung(out, stored, owned, trace.StageInternalDrop)
	case trace.StageActivityWrite:
		return a.activityWriteRung(out, stored, owned)
	case trace.StageTierLadder:
		return a.storedRung(out, stored, owned, trace.StageTierLadder)
	case trace.StagePersonCreate:
		return a.personCreateRung(out, stored, facts, owned)
	case trace.StageVerdict:
		return a.verdictRung(out, stored, owned)
	}
	if reg.Stage == trace.StageAttentionLabel {
		return attentionLabelRung(out, facts)
	}
	// A registered stage this assembler has no branch for. The gates make it
	// unreachable; saying so beats a zero-value rung that reads as a real state.
	out.Status = trace.StatusUnknown
	return out
}

// storedRung renders a stage whose answer is a capture_trace row.
func (a *Assembler) storedRung(out Rung, stored capture.TraceLadder, owned bool,
	stage trace.Stage,
) Rung {
	if !owned {
		// Unconditional, whether or not a row exists. Withholding only when
		// there IS one would be a row-existence oracle: a colleague comparing
		// two shared messages would learn which of them faulted on somebody
		// else's mailbox.
		out.Status = trace.StatusWithheld
		return out
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
func (a *Assembler) activityWriteRung(out Rung, stored capture.TraceLadder, owned bool) Rung {
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
		out.Status = trace.StatusWithheld
		return out
	}
	// No activity and no fault row, with the rows visible: the message was
	// dropped before the write was ever attempted.
	return notApplicableOrUnknown(out, stored)
}

// personCreateRung is derived by ELIMINATION. There is no stored "the ladder
// decided to create a contact" — the ladder decides it in memory and explicitly
// refuses to re-derive it downstream — so this reads the person link, and falls
// back to what the ladder's own rung concluded.
func (a *Assembler) personCreateRung(out Rung, stored capture.TraceLadder,
	facts *activities.PipelineFacts, owned bool,
) Rung {
	if facts == nil {
		// No activity: the message never reached the step.
		return notApplicableOrUnknown(out, stored)
	}
	if facts.HasPersonLink {
		out.Status = trace.StatusDone
		return out
	}
	ladder, found := findRung(stored, trace.StageTierLadder)
	if !owned || !found {
		// The ladder rung is a STORED row, so a caller who may not see it must
		// not learn its content through this one. Linked-or-not is all that can
		// be said, and it is said without a reason rather than with a guessed
		// one.
		out.Status = trace.StatusUnknown
		return out
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
func (a *Assembler) verdictRung(out Rung, stored capture.TraceLadder, owned bool) Rung {
	if !owned {
		out.Status = trace.StatusWithheld
		return out
	}
	resolution := findResolution(stored)
	if resolution == nil {
		out.Status, out.Reason = trace.StatusNotApplicable, trace.ReasonNoOpenQuestion
		return out
	}
	if resolution.Status == "pending" || resolution.Status == "unsure" {
		out.Status, out.Reason = trace.StatusPending, trace.ReasonAwaitingVerdict
	} else {
		out.Status, out.Reason = trace.StatusDone, trace.ReasonVerdictReached
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
func attentionLabelRung(out Rung, facts *activities.PipelineFacts) Rung {
	if facts == nil {
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
		out.Status = trace.StatusUnknown
		return out
	}
	out.Status = trace.StatusNotApplicable
	return out
}

// statusForOutcome maps capture's own vocabulary onto the reader's.
func statusForOutcome(outcome string) trace.Status {
	switch outcome {
	case "captured":
		return trace.StatusDone
	case "fault":
		return trace.StatusFailed
	case "internal", "suppressed", "deferred":
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
	return outcome == "suppressed" || outcome == "deferred" || outcome == "internal"
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
