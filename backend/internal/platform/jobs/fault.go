// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package jobs

import (
	"context"
	"errors"
	"log/slog"

	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/pkg/extension"
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
//
// It knows no River kind, so it can honour NO composed failure class: a class
// is verified against the vocabulary registered for the kind that is failing,
// and a caller who cannot say which kind that is cannot have a class verified
// on their behalf. A composed worker calls FaultForKind instead. A classified
// error arriving here is not lost — it falls through to the core sentinel
// underneath it, exactly as it did before classes existed.
func FaultContext(ctx context.Context, err error) error {
	return faultFor(ctx, "", err)
}

// FaultForKind is FaultContext for a worker that knows the River kind it is
// failing under, which is the only way a COMPOSED class can be honoured.
//
// THE KIND IS WHAT MAKES THE CHECK POSSIBLE, and the check is what makes the
// write path obey the boot validation. extension.Failure is a plain constructor:
// it accepts any FailureClass value a unit builds, including one whose Sentence
// was formatted from the cause — which is the accident this whole seam exists to
// prevent, since a provider's prose routinely names the address it refused. The
// declared set is validated and collision-checked at boot; a value handed over at
// tick time has been through none of that. So the sentence is persisted only when
// it is, exactly, one this installation registered for this kind.
//
// An unregistered class degrades to the same unclassified substitute a bypassed
// fault gets, and the cause goes to the log. That is a real loss of detail for
// the operator and it is the right trade: the alternative is a fleet-visible,
// unscoped column holding text nobody reviewed.
func FaultForKind(ctx context.Context, kind string, err error) error {
	return faultFor(ctx, kind, err)
}

// faultFor is the one body both entry points share, so the two cannot drift into
// two different orderings of the same decisions.
func faultFor(ctx context.Context, kind string, err error) error {
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
	// The UNIT'S OWN classification wins over the core vocabulary, and is checked
	// before it. A tick that wrapped a core sentinel in one of its declared classes
	// looked at the whole operation and said what it means for THIS unit — that a
	// provider is unreachable, say, rather than that some read found nothing — and
	// that is the more useful of two true statements. The sentinel stays reachable
	// through errors.Is underneath, so nothing that classifies on it downstream is
	// affected by the order.
	//
	// Only a class this installation REGISTERED for this kind is honoured — see
	// FaultForKind for why the write path checks rather than trusts.
	//
	// An unregistered one does not return here. It falls through to the core
	// vocabulary below, because a unit that wrapped a core sentinel in a class it
	// forgot to declare should still get the sentinel's own sentence rather than
	// nothing: an undeclared class must cost the failure the unit's detail, not a
	// classification it would have had anyway.
	if class, ok := extension.FailureClassOf(err); ok {
		if registered, found := registeredFailureClass(kind, class); found {
			// THE CAUSE STILL GOES TO THE LOG. Classifying a failure says what
			// KIND of thing went wrong; it does not say which host did not
			// resolve, and that detail is the diagnosis — the thing this seam
			// promises is reachable somewhere, just not in a fleet-visible
			// column. Returning here without logging would trade a vague screen
			// for a silent log, which is the same operator stuck one step later.
			slog.ErrorContext(ctx, "jobs: a worker failed", faultLogAttrs(ctx, kind, registered.Class, err)...)
			return &fault{sentence: registered.Sentence, cause: err}
		}
		slog.ErrorContext(ctx, "jobs: a worker returned a failure class this installation did not declare for this kind, so its sentence is not published",
			faultLogAttrs(ctx, kind, class.Class, err)...)
	}
	for _, known := range vocabulary {
		if errors.Is(err, known.sentinel) {
			return &fault{sentence: known.sentence, cause: err}
		}
	}
	// The SAME attributes as the two classified lines above. This is the branch
	// whose sentence tells an operator the diagnosis is in the process log, so it
	// is the one line that must be findable — and it was the one carrying neither
	// the kind nor anything else identifying the tick.
	slog.ErrorContext(ctx, "jobs: a worker failed with an unclassified cause", faultLogAttrs(ctx, kind, "", err)...)
	return &fault{sentence: unrecognised, cause: err}
}

// faultLogAttrs is what every fault log line carries, spelled once so the three
// branches cannot describe the same failure three different ways.
//
// The correlation id and the workspace are read off the context and attached
// EXPLICITLY rather than left to the handler. slog.ErrorContext only enriches a
// record if the DEFAULT handler happens to be correlation-aware, and no process
// role installs one: cmd/worker builds a correlation-aware logger and passes it
// around, while slog.Default() keeps the bare handler. So a line that relied on
// the handler carried neither value, whichever context it was given — which is
// the whole reason a caller is asked to pass the tick's own context.
//
// An absent value is OMITTED rather than logged empty. A dispatcher has no
// workspace and the two kindless entry points have no kind; an empty attr would
// assert a value the failure does not have, and a reader filtering on it would
// match every one of them.
func faultLogAttrs(ctx context.Context, kind, class string, err error) []any {
	attrs := make([]any, 0, 8)
	if kind != "" {
		attrs = append(attrs, "kind", kind)
	}
	if class != "" {
		attrs = append(attrs, "class", class)
	}
	if id, ok := principal.CorrelationID(ctx); ok {
		attrs = append(attrs, "correlation_id", id.String())
	}
	if ws, ok := principal.WorkspaceID(ctx); ok {
		attrs = append(attrs, "workspace_id", ws.String())
	}
	return append(attrs, "err", err)
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

// The three SUBSTITUTE sentences a reader falls back to when a stored failure
// does not classify. They live together, in this package, because they are one
// set with one property to keep: each says something no classified failure may
// ever claim to say, so a composed class declaring any of them is refused
// (refuseCoreCollision). Two of them used to live in the HTTP surface that
// renders them, where the refusal could not see them — and a rule enforced over
// part of a set is a rule with a hole in the shape of the rest.
//
// They are NOT vocabulary entries: no sentinel maps to them, no class token names
// them, and VettedFailure answers no class for them. That is the point. Each is
// what the product says when it has nothing to say.
const (
	// unrecognised is what an unclassified cause becomes on the wire. It says
	// where the diagnosis went, because an operator reading it in a job list
	// otherwise has no next step.
	unrecognised = "the job failed for a reason it could not classify; the diagnosis is in the process log"

	// UnvettedFailureReason is what an unrecognised STORED error becomes.
	//
	// It does NOT promise the diagnosis is in the process log. River writes its
	// own strings into that column too, and the rescuer's ("Stuck job rescued by
	// JobRescuer") means the worker's process died mid-job — so for that case,
	// one of the most common to reach here, a log pointer would be an instruction
	// to go read something that was never written. It says what is known and
	// where to look, and no more.
	UnvettedFailureReason = "the job failed for a reason this surface cannot vet; check the worker logs and the job row directly"

	// NoRecordedCause is what a row with no stored error at all becomes.
	//
	// It is NOT the unvetted substitute. A cancelled job that never ran records
	// no attempt error, and telling its operator the job "failed for a reason
	// this surface cannot vet" asserts a failure that did not happen and points
	// at a log line nobody wrote. Nothing recorded is a different fact from
	// something unreadable, and the two must not render alike.
	NoRecordedCause = "this job recorded no cause; a job cancelled before it ran records none"
)

// substitutes is the set no declared class may claim, spelled once so a fourth
// one added above is covered without anybody remembering to add it here.
var substitutes = []string{unrecognised, UnvettedFailureReason, NoRecordedCause}

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
//
// Every entry carries the same TRIPLE an extension unit declares (faultclass.go):
// a class token, the sentence, and the remedy. The two halves are one vocabulary
// rendered by one surface, so they hold the same shape — an operator should not be
// able to tell from a failure list which tier classified the failure, and a
// reader should not need two code paths to render it.
//
// TestEverySentinelIsClassifiedForTheJobSurface derives the coverage obligation
// from apperrors itself: a sentinel added there without an entry here fails the
// gate rather than silently reporting as unclassifiable the first time a job
// returns it.
var vocabulary = []struct {
	sentinel error
	class    string
	sentence string
	remedy   string
}{
	{apperrors.ErrNotFound, "record_gone", "the record this job names no longer exists", "Nothing to do: the work is moot. Re-queue only if the record was deleted in error and has been restored."},
	{apperrors.ErrConflict, "write_conflict", "another writer changed the record while this job ran", "Re-queue it. The job re-reads the record and the second attempt normally settles."},
	{apperrors.ErrVersionSkew, "version_skew", "the record changed under this job; it will re-read on retry", "Nothing to do: the retry re-reads. A job stuck here across many attempts means a writer is changing the record faster than the job can finish."},
	{apperrors.ErrPermissionDenied, "principal_not_permitted", "this job's principal is not permitted the action it attempted", "Check the seat this job runs as still holds the role the action needs; a demoted or archived seat produces exactly this."},
	{apperrors.ErrConsentNotGranted, "consent_missing", "consent for this purpose is not granted, so the job stopped before acting", "Nothing to fix in the job. The record's owner grants consent, or the work is not meant to happen."},
	{apperrors.ErrBudgetExceeded, "budget_spent", "the budget for this work is spent; the job will run once it refreshes", "Wait for the window to refresh, or raise the budget if the work matters more than the cap."},
	{apperrors.ErrIncumbentBudgetExhausted, "incumbent_budget_spent", "the incumbent CRM's API budget is spent; the poller will catch up", "Nothing to do: the poller resumes on the next window. Persistent exhaustion means the sync cadence is above what the incumbent's plan allows."},
	{apperrors.ErrRequiresApproval, "staged_for_approval", "this action needs human approval and was staged rather than executed", "Somebody approves or rejects the staged action; the job itself needs no re-queue."},
	{apperrors.ErrSeatTierInsufficient, "seat_tier_insufficient", "the granting seat's tier does not admit this action", "Raise the granting seat's tier, or stop asking this job for an action that tier is not meant to take."},
	{apperrors.ErrSeatLimitReached, "seat_limit_reached", "the installation's licensed full seats are all in use, so no seat was created", "Free a seat or license another, then re-queue."},
	{apperrors.ErrScopeExceeded, "scope_exceeded", "the passport's scope does not cover this action", "Re-issue the passport with the scope the action needs, or narrow what the job attempts."},
	{apperrors.ErrApprovalTokenInvalid, "approval_token_spent", "the approval token was invalid or already spent", "Ask for the approval again. A token is single-use, so a replayed one lands here."},
	{apperrors.ErrModeNotOverlay, "not_overlay_mode", "this workspace is no longer in overlay mode", "Nothing to do: the work belonged to overlay mode and the workspace has left it."},
	{apperrors.ErrUnsupportedBySoR, "unsupported_by_sor", "the system of record does not support this operation", "Nothing to fix here; the operation has to happen in the system of record itself."},
	{apperrors.ErrIncumbentAlreadyConnected, "incumbent_already_connected", "an incumbent connection already exists for this workspace", "Disconnect the existing incumbent first if this job was meant to replace it."},
	{apperrors.ErrOverlayFlipBlocked, "overlay_flip_blocked", "the overlay flip preflight is unsatisfied", "Read the flip preflight for what is outstanding, satisfy it, then re-queue."},
	{apperrors.ErrBaseCurrencyLocked, "base_currency_locked", "the base currency is locked by frozen conversion rates", "Nothing to do in the job: a base currency stops being changeable once rates are frozen against it."},
	{apperrors.ErrRetentionHold, "retention_hold", "the record is held under a statutory retention obligation", "Nothing to do, and nothing to force: the hold outranks this job. The record becomes workable when the obligation lapses."},
}
