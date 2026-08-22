// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

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
			if in, asked := extension.RescheduleAfter(err); asked {
				return rescheduleFor(ctx, kind, registered.Class, in, err)
			}
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

// The bounds a requested postponement is held to before it reaches the queue.
//
// A delay is a REQUEST from a unit, exactly as a class is, and this is the half
// of the seam that makes the request safe to honour.
//
// WHAT THE CEILING PROTECTS, said precisely, because the imprecise version is
// checkably false: a postponed row is `scheduled`, and `scheduled` IS counted —
// health.go's waitingStates and the queue-depth gauge both include it, so a row
// postponed for a year still shows up as one piece of waiting work. What it
// escapes is the AGE reading. Every "how long has this waited" measurement in
// this package filters `scheduled_at <= now()`, correctly, because nothing
// scheduled for the future is late — so a far-future delay produces a count
// nobody can age, forever, and "waiting: 1" is what a healthy idle tick looks
// like too. The ceiling keeps the delay inside the window where a stuck
// postponement eventually becomes measurable instead of permanently plausible.
//
// The floor carries two jobs. River PANICS on a negative duration, so a unit
// that computed one from a clock — a deadline already past, a subtraction that
// went the wrong way — would take the worker process down rather than fail a
// tick, and a bound is the only place that can be caught once for everybody.
// Zero is not a panic but is River's "run me immediately", which for a failing
// tick is a spin against a provider that is already refusing; a second is long
// enough not to be one and short enough that no unit meaning "as soon as
// possible" is meaningfully denied.
//
// A CALLER STAYING UNDER THE CEILING IS NOT LEFT TO PROSE. It used to say the
// ceiling "sits well above any cadence a connector declares", which was a claim
// about one day's tree that nothing enforced — and it hid the inversion it was
// meant to reassure about, since a unit declaring a cadence above this bound
// reconciles its delay against that cadence perfectly and then gets clamped to
// less, polling a refusing provider harder during an outage than in health.
// backend/pollcadenceparity_test.go reads this bound out of this file and holds
// every postponing unit under it.
const (
	minRescheduleDelay = time.Second
	maxRescheduleDelay = 15 * time.Minute
)

// rescheduleFor turns a unit's postponement request into the queue's own control
// return.
//
// WARN, not ERROR, and the level is the message. A postponed tick is not a
// failure: nothing was lost, nothing is owed an operator, and logging it at the
// level a dead job uses would put a routine outage in the same lane as work that
// needs a human. It is logged at all because a connector that has been
// postponing itself for a day is a fact somebody eventually needs, and it is the
// one fact a snooze leaves nowhere else — River records no attempt error for a
// snooze, so this line and the unit's own row are the whole trail.
func rescheduleFor(ctx context.Context, kind, class string, in time.Duration, err error) error {
	delay := min(max(in, minRescheduleDelay), maxRescheduleDelay)
	attrs := append(faultLogAttrs(ctx, kind, class, err), "retry_in", delay.String())
	// A CLAMPED REQUEST SAYS SO. The bounds exist to catch a unit that computed a
	// delay wrong, and a clamp that logged only its own result made that mistake
	// invisible: a unit asking for 72 hours and a unit asking for fifteen minutes
	// produced byte-identical output, so the one thing the bound was written to
	// notice left no trace anywhere. The request is carried only when it differs,
	// because a field that repeats retry_in on every healthy line is noise the
	// clamped case then hides in.
	if delay != in {
		attrs = append(attrs, "requested", in.String())
	}
	slog.WarnContext(ctx, "jobs: a worker postponed its own tick rather than failing", attrs...)
	return river.JobSnooze(delay)
}

// faultLogAttrs is what every fault log line carries, spelled once so the three
// branches cannot describe the same failure three different ways.
//
// THE CORRELATION ID IS THE HANDLER'S, and it is not attached here. It used to
// be, because no process role installed its own handler as the default and a
// package-level slog call therefore reached a bare one that enriched nothing —
// so the id had to be attached by hand or it appeared on no fault line at all.
// Every serving role now installs a correlation-aware default
// (httpserver.InstallProcessLogger), which makes the hand-attachment not merely
// redundant but wrong: both halves would stamp the same key and a JSON line
// would carry correlation_id twice. One thing stamps it, and it is the handler
// that stamps it for every other package-level call in the tree.
//
// THE WORKSPACE STILL IS attached here, because nothing else knows it. The
// handler reads the correlation id off the context and only that; a job's
// workspace is this seam's to say.
//
// An absent value is OMITTED rather than logged empty. A dispatcher has no
// workspace and the two kindless entry points have no kind; an empty attr would
// assert a value the failure does not have, and a reader filtering on it would
// match every one of them.
func faultLogAttrs(ctx context.Context, kind, class string, err error) []any {
	attrs := make([]any, 0, 6)
	if kind != "" {
		attrs = append(attrs, "kind", kind)
	}
	if class != "" {
		attrs = append(attrs, "class", class)
	}
	if ws, ok := principal.WorkspaceID(ctx); ok {
		attrs = append(attrs, "workspace_id", ws.String())
	}
	return append(attrs, errorAttr(err))
}

// errorAttr renders the cause as a STRUCTURED GROUP rather than one formatted
// string, because this log line is the only place the cause is allowed to be
// detailed and a collector should not have to parse prose to use it.
//
// WHY THE DETAIL IS SAFE HERE and nowhere else: river_job.errors is unscoped and
// fleet-visible, which is the whole reason Fault substitutes a fixed sentence
// before the queue sees the error. The process log is the other side of that
// trade — its audience and its retention are the operator's own — so this is
// where the cause is supposed to be legible.
//
// THE TYPE CHAIN IS THE FIELD THAT EARNS ITS PLACE, and it is not in the
// message. The branch that most needs this line is the unclassified one, whose
// stored sentence says only that the diagnosis is in the process log — and what
// the reader of that line needs is which error a unit returned, so it can be
// given a class. err.Error() answers what the provider said; the chain answers
// which code said it. The same holds for a postponement, which River records no
// attempt error for at all, so this line is the entire trail.
//
// Outermost first, matching the order errors.As resolves in, and bounded by the
// wrapping depth an error actually has — a chain is walked with Unwrap and stops
// where the wrapping stops.
func errorAttr(err error) slog.Attr {
	return slog.Group("error",
		slog.String("message", err.Error()),
		slog.String("type", fmt.Sprintf("%T", err)),
		slog.Any("chain", causeTypes(err)),
	)
}

// causeTypes names the Go type of every layer of a wrapped error, outermost
// first, so an operator reading a fault line can find the code that produced it.
//
// It follows single-cause Unwrap only. An errors.Join tree has no single next
// layer, and a reader that flattened one would present siblings as a chain of
// causes — a claim about which failure produced which that the tree does not
// make. Such an error stops the walk at itself, where its own type and its
// message (which carries every joined cause's text) are already recorded.
func causeTypes(err error) []string {
	types := make([]string, 0, 4)
	for at := err; at != nil; at = errors.Unwrap(at) {
		types = append(types, fmt.Sprintf("%T", at))
	}
	return types
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
	{apperrors.ErrProviderUnusable, "provider_unusable", "an outside service gave no usable answer, so nothing was learned", "Check the provider is reachable, is not rate-limiting this installation, and answers in the expected shape, then re-queue. A request with no answer says nothing about its subject."},
	{apperrors.ErrRetentionHold, "retention_hold", "the record is held under a statutory retention obligation", "Nothing to do, and nothing to force: the hold outranks this job. The record becomes workable when the obligation lapses."},
}
