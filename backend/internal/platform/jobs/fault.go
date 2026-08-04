// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package jobs

import (
	"context"
	"errors"
	"log/slog"

	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// Fault renders a worker's failure as a fixed operator sentence and keeps
// the cause reachable through errors.Is.
//
// River persists err.Error() into river_job.errors verbatim. That column
// has no workspace, no RLS, and a retention River chooses — so whatever a
// worker returns is stored, fleet-visible, for as long as the ladder runs.
// A provider refusing a message routinely names the address it refused, so
// the raw cause is the one thing that may not travel this way. It goes to
// the log, where the audience and the retention are the operator's own.
//
// This is the comms faultReason posture (a fixed sentence chosen by what
// the cause IS, never the cause's own text) applied at the seam every
// worker shares rather than in one module.
func Fault(err error) error { return FaultContext(context.Background(), err) }

// FaultContext is Fault with the caller's context on the log line, so an
// unclassified failure carries the correlation id the rest of the trace uses.
func FaultContext(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	// River's own control returns pass through UNTOUCHED. A snooze
	// reschedules and a cancel deliberately stops; neither is a failure and
	// neither carries a cause to publish. They reach a worker's return
	// through helpers as often as directly (telegrampoll.go's
	// answerPollFailure returns river.JobSnooze on a provider throttle), so
	// this check cannot live at the call sites — it has to be here, or every
	// routine throttle logs as an unclassified failure and River's own
	// errors.As classification reads a substituted message.
	//
	// It is also checked BEFORE the vocabulary, so a cancel carrying a known
	// sentinel stays a cancel: stopping deliberately is not failing.
	var snooze *river.JobSnoozeError
	var cancel *river.JobCancelError
	if errors.As(err, &snooze) || errors.As(err, &cancel) {
		return err
	}
	for _, known := range vocabulary {
		if errors.Is(err, known.sentinel) {
			return &fault{sentence: known.sentence, cause: err}
		}
	}
	slog.ErrorContext(ctx, "jobs: a worker failed with an unclassified cause", "err", err)
	return &fault{sentence: unrecognised, cause: err}
}

// VettedSentence reports whether s is a sentence Fault itself would have
// written — one of the vocabulary's, or the unclassified fallback.
//
// It exists because river_job.errors is fleet-visible with no RLS and no
// redaction path (see Fault), so a surface that shows a failure to a human
// asks this rather than trusting the column. A worker that bypassed Fault
// and returned its raw cause stored that cause here; the answer for it is
// false, and the reader substitutes its own fixed text. River writes into
// that column too — its rescuer's "Stuck job rescued by JobRescuer" is not
// a Fault sentence and is correctly refused.
//
// The comparison is EXACT, never a prefix or a contains: a worker whose raw
// cause merely embeds a vetted sentence would otherwise carry the rest of
// its text through on the strength of the part that matched.
//
// The vocabulary stays unexported: a caller asks whether one string is
// vetted, it does not get the list to render or to match against by hand.
func VettedSentence(s string) bool {
	if s == unrecognised {
		return true
	}
	for _, known := range vocabulary {
		if s == known.sentence {
			return true
		}
	}
	return false
}

// unrecognised is what an unclassified cause becomes on the wire. It says
// where the diagnosis went, because an operator reading it in a job list
// otherwise has no next step.
const unrecognised = "the job failed for a reason it could not classify; the diagnosis is in the process log"

// fault carries the vetted sentence on the wire and the real cause
// underneath, so errors.Is still classifies while Error() stays fixed.
type fault struct {
	sentence string
	cause    error
}

func (f *fault) Error() string { return f.sentence }
func (f *fault) Unwrap() error { return f.cause }

// vocabulary maps the shared sentinel registry to operator sentences. Each
// says what went wrong AND what it means for the job — an operator reading
// a failure list needs to know whether to retry, wait, or fix something.
var vocabulary = []struct {
	sentinel error
	sentence string
}{
	{apperrors.ErrNotFound, "the record this job names no longer exists"},
	{apperrors.ErrConflict, "another writer changed the record while this job ran"},
	{apperrors.ErrVersionSkew, "the record changed under this job; it will re-read on retry"},
	{apperrors.ErrPermissionDenied, "this job's principal is not permitted the action it attempted"},
	{apperrors.ErrConsentNotGranted, "consent for this purpose is not granted, so the job stopped before acting"},
	{apperrors.ErrBudgetExceeded, "the budget for this work is spent; the job will run once it refreshes"},
	{apperrors.ErrIncumbentBudgetExhausted, "the incumbent CRM's API budget is spent; the poller will catch up"},
	{apperrors.ErrRequiresApproval, "this action needs human approval and was staged rather than executed"},
	{apperrors.ErrSeatTierInsufficient, "the granting seat's tier does not admit this action"},
	{apperrors.ErrScopeExceeded, "the passport's scope does not cover this action"},
	{apperrors.ErrApprovalTokenInvalid, "the approval token was invalid or already spent"},
	{apperrors.ErrModeNotOverlay, "this workspace is no longer in overlay mode"},
	{apperrors.ErrUnsupportedBySoR, "the system of record does not support this operation"},
	{apperrors.ErrIncumbentAlreadyConnected, "an incumbent connection already exists for this workspace"},
	{apperrors.ErrOverlayFlipBlocked, "the overlay flip preflight is unsatisfied"},
}
