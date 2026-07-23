// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The voice-build job: the api enqueues one job per created build (inside
// the creating transaction, WithVoiceBuildEnqueue), the worker role claims
// the durable row and drives it snapshot → extract → evaluate → activate.
// The row owns its state: every model failure lands on the build as a
// deferred or failed status, never as a River retry loop.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// VoiceBuildArgs runs ONE durable voice build. Unique by args while
// incomplete: the api enqueue and the deferred-retry sweep converge on one
// job per build row.
type VoiceBuildArgs struct {
	Workspace   string `json:"workspace"`
	ProfileID   string `json:"profile_id"`
	BuildID     string `json:"build_id"`
	RequestedBy string `json:"requested_by"`
}

// Kind is the stable job identifier River persists in river_job.
func (VoiceBuildArgs) Kind() string { return "voice_build" }

// VoiceBuildRetryArgs schedules one deferred-build sweep: re-enqueue every
// budget-deferred build whose next attempt is due.
type VoiceBuildRetryArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (VoiceBuildRetryArgs) Kind() string { return "voice_build_retry" }

// voiceBuildRetryInterval paces the deferred sweep; deferrals are hours
// long, so a 15-minute scan is prompt without being busywork.
const voiceBuildRetryInterval = 15 * time.Minute

// voiceBuildDeferral is how long a budget-deferred build waits before the
// sweep re-offers it to the (possibly refreshed) budget window.
const voiceBuildDeferral = 6 * time.Hour

// voiceBuildTimeout bounds one run: one builder call plus 15 evaluation
// drafts and 5 judge calls, each small, with validator retry headroom.
const voiceBuildTimeout = 10 * time.Minute

// The live voice_build row states the worker distinguishes when a claim is
// refused; a terminal row needs no further work.
const (
	voiceRowQueued   = "queued"
	voiceRowDeferred = "deferred"
	voiceRowRunning  = "running"
)

func voiceBuildInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: activeSweepStates}}
}

// WithVoiceBuildEnqueue makes createVoiceBuild queue the job through the
// insert-only runner inside the creating transaction (the api role never
// builds in-request — the worker role does).
func WithVoiceBuildEnqueue(inserter *jobs.Runner) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		s.voiceHandlers = s.WithVoiceBuildEnqueue(func(ctx context.Context, tx pgx.Tx, build ai.VoiceBuild) error {
			actor, ok := principal.Actor(ctx)
			if !ok {
				return fmt.Errorf("compose: voice build enqueue without an acting principal")
			}
			return inserter.EnqueueTx(ctx, tx, VoiceBuildArgs{
				Workspace:   storekit.MustWorkspace(ctx).String(),
				ProfileID:   build.ProfileID.String(),
				BuildID:     build.ID.String(),
				RequestedBy: actor.UserID.String(),
			}, voiceBuildInsertOpts())
		})
	}
}

// voiceBuildWorker drives one claimed build to a terminal state.
type voiceBuildWorker struct {
	river.WorkerDefaults[VoiceBuildArgs]
	store *ai.VoiceStore
	brain completer
	log   *slog.Logger
	now   func() time.Time
}

func newVoiceBuildWorker(pool *pgxpool.Pool, brain completer, log *slog.Logger) *voiceBuildWorker {
	return &voiceBuildWorker{store: ai.NewVoiceStore(pool), brain: brain, log: log, now: time.Now}
}

// Timeout overrides River's 1-minute default for the multi-call run.
func (w *voiceBuildWorker) Timeout(*river.Job[VoiceBuildArgs]) time.Duration {
	return voiceBuildTimeout
}

// reclaimAfter leaves a grace beyond the work timeout before a replacement
// worker may steal a running row from a dead one.
func (w *voiceBuildWorker) reclaimAfter() time.Duration { return voiceBuildTimeout + time.Minute }

// voiceBuildWorkerCtx binds the build's workspace and the owner-scoped
// system principal: visibleProfile admits only the profile owner, so the
// runner acts as the requesting human's system delegate.
func voiceBuildWorkerCtx(ctx context.Context, args VoiceBuildArgs) (context.Context, error) {
	ws, err := ids.Parse(args.Workspace)
	if err != nil {
		return nil, fmt.Errorf("voice_build: workspace id: %w", err)
	}
	requester, err := ids.Parse(args.RequestedBy)
	if err != nil {
		return nil, fmt.Errorf("voice_build: requester id: %w", err)
	}
	ctx = principal.WithWorkspaceID(ctx, ws)
	ctx = principal.WithActor(ctx, principal.Principal{
		Type:       principal.PrincipalSystem,
		ID:         "agent:voice-builder",
		UserID:     requester,
		OnBehalfOf: requester,
	})
	return principal.WithCorrelationID(ctx, ids.NewV7()), nil
}

func (w *voiceBuildWorker) Work(ctx context.Context, job *river.Job[VoiceBuildArgs]) error {
	ctx, err := voiceBuildWorkerCtx(ctx, job.Args)
	if err != nil {
		return err
	}
	profileID, err := ids.Parse(job.Args.ProfileID)
	if err != nil {
		return fmt.Errorf("voice_build: profile id: %w", err)
	}
	buildID, err := ids.Parse(job.Args.BuildID)
	if err != nil {
		return fmt.Errorf("voice_build: build id: %w", err)
	}
	input, claimed, err := w.store.ClaimBuild(ctx, profileID, buildID, w.reclaimAfter())
	if err != nil {
		return fmt.Errorf("voice_build %s: claim: %w", job.Args.BuildID, err)
	}
	if !claimed {
		switch input.Build.Status {
		case voiceRowQueued, voiceRowDeferred, voiceRowRunning:
			// The row is still live but not claimable right now: a rival's
			// fresh claim, a deferred window that has not opened, or a dead
			// worker's row inside its reclaim grace. Succeeding here would
			// strand the build with no job left to work it — come back.
			return river.JobSnooze(2 * time.Minute)
		default:
			// Terminal — the row already owns its outcome.
			return nil
		}
	}
	claimedAt := claimTime(input)
	if w.brain == nil {
		return w.fail(ctx, buildID, claimedAt, "model_unavailable",
			"Voice building is unavailable until an AI provider is configured on the worker role.")
	}
	if err := w.run(ctx, buildID, input); err != nil {
		if errors.Is(err, ai.ErrBudgetDeferred) {
			terminal, cancel := terminalCtx(ctx)
			defer cancel()
			if deferErr := w.store.DeferBuild(terminal, buildID, claimedAt,
				"The monthly AI budget is exhausted; the build resumes in the next window.",
				w.deferralDeadline(err)); deferErr != nil {
				return fmt.Errorf("voice_build %s: defer: %w", job.Args.BuildID, deferErr)
			}
			return nil
		}
		// The row carries only the safe detail; the OPERATOR needs the real
		// cause or a repeated invalid_output is undiagnosable from any log.
		w.log.WarnContext(ctx, "voice build error", "build", buildID.String(), "err", err)
		return w.fail(ctx, buildID, claimedAt, failureStatusCode(err), ai.SafeVoiceBuildFailure(err))
	}
	return nil
}

// claimTime is the claim generation every terminal write fences on.
func claimTime(input ai.VoiceBuildInput) time.Time {
	if input.Build.StartedAt == nil {
		// ClaimBuild always stamps started_at; a nil here would only mean a
		// hand-edited row, and a zero fence simply never matches.
		return time.Time{}
	}
	return *input.Build.StartedAt
}

// predecessorWordCount reads the previous version's corpus size so the
// change timeline can record what a rebuild ADDED, not the whole corpus. A
// missing or malformed stats payload reads as zero — the delta then simply
// reports the full corpus, which is the pre-existing honest fallback.
func predecessorWordCount(predecessor *ai.VoiceProfileVersion) int {
	if predecessor == nil {
		return 0
	}
	words, ok := predecessor.StatsJSON["word_count"].(float64)
	if !ok {
		return 0
	}
	return int(words)
}

// evaluatedPredecessorVersion names the version the evaluation compared
// against, so activation can refuse a comparison that went stale mid-run.
func evaluatedPredecessorVersion(predecessor *ai.VoiceProfileVersion) int {
	if predecessor == nil {
		return 0
	}
	return predecessor.ProfileVersion
}

// deferralDeadline honors the router's exact budget-window boundary when the
// error carries one; the fixed fallback serves only a bare sentinel.
func (w *voiceBuildWorker) deferralDeadline(err error) time.Time {
	var deferral *ai.BudgetDeferralError
	if errors.As(err, &deferral) && deferral.NextAttemptAt.After(w.now()) {
		return deferral.NextAttemptAt
	}
	return w.now().Add(voiceBuildDeferral)
}

// run drives extract → evaluate → activate on a claimed build. Every
// terminal write below mints its detached context AT write time (deep-read
// precedent): a context minted before the model calls would carry a deadline
// the run itself already spent, and the outcome could then never be
// recorded — the build would sit "running" until reclaim, forever retrying.
func (w *voiceBuildWorker) run(ctx context.Context, buildID ids.UUID, input ai.VoiceBuildInput) error {
	heldOut, buildSamples := splitVoiceHeldOut(input.Samples, input.Build.SourceHash)
	if err := w.store.SetBuildStage(ctx, buildID, "extract"); err != nil {
		w.log.WarnContext(ctx, "voice build stage update failed", "build", buildID.String(), "err", err)
	}
	artifact, err := ai.DeriveVoice(ctx, w.brain, input.Personality, input.Build.SourceHash, buildSamples)
	if err != nil {
		return err
	}
	if err := w.store.SetBuildStage(ctx, buildID, "evaluate"); err != nil {
		w.log.WarnContext(ctx, "voice build stage update failed", "build", buildID.String(), "err", err)
	}
	var predecessor *ai.VoiceProfileVersion
	if active, ok, err := w.store.ActiveVersion(ctx, input.Profile.ID); err != nil {
		return err
	} else if ok {
		predecessor = &active
	}
	evaluated, err := evaluateVoiceCandidate(ctx, w.brain, artifact, input.Personality, heldOut, predecessor)
	if err != nil {
		return err
	}
	corpusStats := ai.AnalyzeVoice(input.Samples)
	if err := w.store.SetBuildStage(ctx, buildID, "activate"); err != nil {
		w.log.WarnContext(ctx, "voice build stage update failed", "build", buildID.String(), "err", err)
	}
	// The completing write must outlive a job timeout that fires mid-run: a
	// canceled work context must never strand a finished artifact unrecorded.
	terminal, cancel := terminalCtx(ctx)
	defer cancel()
	_, err = w.store.CompleteBuild(terminal, buildID, claimTime(input), ai.VoiceBuildOutcome{
		Artifact:     artifact,
		Evaluation:   evaluated.Evaluation,
		SampleDrafts: evaluated.SampleDrafts,
		// Guidance and the stored stats read the WHOLE corpus, held-out
		// included — the nudge must never ask for a register the user
		// already supplied, and the profile screen counts everything.
		Guidance:             voiceGuidance(corpusStats),
		CorpusStats:          corpusStats,
		PredecessorWords:     predecessorWordCount(predecessor),
		EvaluatedPredecessor: evaluatedPredecessorVersion(predecessor),
		Classification:       evaluated.Classification,
		Action:               evaluated.Action,
		StatusCode:           evaluated.StatusCode,
		ReviewReasons:        evaluated.ReviewReasons,
		ModelProvider:        "routed",
		ModelName:            modelNameOrUnrecorded(artifact.ModelName),
	})
	return err
}

// fail records the terminal failure on the row; the job itself succeeds —
// the row owns retry policy, not River. The write runs on its own detached
// context so it lands even when the work context is already dead.
func (w *voiceBuildWorker) fail(ctx context.Context, buildID ids.UUID, claimedAt time.Time, statusCode, detail string) error {
	terminal, cancel := terminalCtx(ctx)
	defer cancel()
	if err := w.store.FailBuild(terminal, buildID, claimedAt, statusCode, detail); err != nil {
		return fmt.Errorf("voice_build %s: record failure: %w", buildID.String(), err)
	}
	w.log.WarnContext(ctx, "voice build failed", "build", buildID.String(), "status_code", statusCode, "detail", detail)
	return nil
}

// failureStatusCode maps a build error class onto the row vocabulary: a
// missing or unbound model lane is model_unavailable, malformed or
// unverifiable model output is invalid_output, everything else is internal.
func failureStatusCode(err error) string {
	text := err.Error()
	for _, marker := range []string{"no model path", "no bound", "not bound", "unbound"} {
		if strings.Contains(text, marker) {
			return "model_unavailable"
		}
	}
	for _, marker := range []string{"invalid JSON", "cited unknown sample", "is not verbatim", "output", "is empty"} {
		if strings.Contains(text, marker) {
			return "invalid_output"
		}
	}
	return "internal"
}

func modelNameOrUnrecorded(name string) string {
	if name == "" {
		return "unrecorded"
	}
	return name
}

// voiceBuildRetryWorker re-enqueues every due deferred build; the per-build
// job's uniqueness makes a double offer harmless.
type voiceBuildRetryWorker struct {
	river.WorkerDefaults[VoiceBuildRetryArgs]
	store *ai.VoiceStore
	log   *slog.Logger
}

func (w *voiceBuildRetryWorker) Work(ctx context.Context, _ *river.Job[VoiceBuildRetryArgs]) error {
	due, enumErr := w.store.DueDeferredBuilds(ctx)
	client := river.ClientFromContext[pgx.Tx](ctx)
	for _, ref := range due {
		if ref.RequestedBy == nil {
			// The requester was deleted (ON DELETE SET NULL): nobody can own
			// this run anymore — the row stays deferred and visible rather
			// than running under an unattributable principal.
			w.log.WarnContext(ctx, "voice build skipped: requester deleted", "build", ref.BuildID.String())
			continue
		}
		if _, err := client.Insert(ctx, VoiceBuildArgs{
			Workspace:   ref.Workspace.String(),
			ProfileID:   ref.ProfileID.String(),
			BuildID:     ref.BuildID.String(),
			RequestedBy: ref.RequestedBy.String(),
		}, voiceBuildInsertOpts()); err != nil {
			w.log.WarnContext(ctx, "voice build retry enqueue failed", "build", ref.BuildID.String(), "err", err)
		}
	}
	return enumErr
}
