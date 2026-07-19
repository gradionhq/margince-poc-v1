// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// River adapter for durable personal voice builds. The ai module owns the
// builder and records; compose supplies the model lane and worker principal.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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

// WithVoiceBuild wires only the build-start enqueue seam into the otherwise
// always-live voice handlers.
func WithVoiceBuild(inserter *jobs.Runner) Option {
	return func(s *Server, _ *pgxpool.Pool) {
		s.voiceHandlers = s.WithVoiceBuildEnqueue(func(ctx context.Context, tx pgx.Tx, build ai.VoiceBuild) error {
			return inserter.EnqueueTx(ctx, tx, VoiceBuildArgs{
				WorkspaceID: storekit.MustWorkspace(ctx), ProfileID: build.ProfileID,
				BuildID: build.ID, RequestedBy: build.RequestedBy,
			}, voiceBuildInsertOpts())
		})
	}
}

// VoiceBuildArgs identifies one durable owner-bound build.
type VoiceBuildArgs struct {
	WorkspaceID ids.UUID `json:"workspace_id"`
	ProfileID   ids.UUID `json:"profile_id"`
	BuildID     ids.UUID `json:"build_id"`
	RequestedBy ids.UUID `json:"requested_by"`
}

// Kind is River's stable persisted job name.
func (VoiceBuildArgs) Kind() string { return "voice_build" }

func voiceBuildInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

type voiceBuildWorker struct {
	river.WorkerDefaults[VoiceBuildArgs]
	store *ai.VoiceStore
	brain completer
	log   *slog.Logger
}

func newVoiceBuildWorker(pool *pgxpool.Pool, brain completer, log *slog.Logger) *voiceBuildWorker {
	return &voiceBuildWorker{store: ai.NewVoiceStore(pool), brain: brain, log: log}
}

func (*voiceBuildWorker) Timeout(*river.Job[VoiceBuildArgs]) time.Duration { return 5 * time.Minute }

func (w *voiceBuildWorker) Work(ctx context.Context, job *river.Job[VoiceBuildArgs]) error {
	args := job.Args
	ctx = principal.WithWorkspaceID(ctx, args.WorkspaceID)
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalSystem, ID: "agent:voice-builder", UserID: args.RequestedBy, OnBehalfOf: args.RequestedBy,
	})
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	input, claimed, err := w.store.ClaimBuild(ctx, args.BuildID)
	if err != nil {
		return fmt.Errorf("voice build %s: claim: %w", args.BuildID, err)
	}
	if !claimed {
		return nil
	}
	artifact, err := ai.DeriveVoice(ctx, w.brain, input.Personality, input.Build.SourceHash, input.Samples)
	if err != nil {
		if failErr := w.store.FailBuild(ctx, args.BuildID, err); failErr != nil {
			return fmt.Errorf("voice build %s: record failure after %v: %w", args.BuildID, err, failErr)
		}
		w.log.WarnContext(ctx, "voice build failed", "build", args.BuildID.String(), "err", err)
		return nil
	}
	if _, err := w.store.CompleteBuild(ctx, args.BuildID, artifact, "voice_build"); err != nil {
		return fmt.Errorf("voice build %s: complete: %w", args.BuildID, err)
	}
	return nil
}

// AutomaticVoiceSweepArgs schedules the cheap daily eligibility scan. It
// enqueues the same durable build job as the manual API; no model work runs in
// this dispatcher.
type AutomaticVoiceSweepArgs struct{}

// Kind is River's stable persisted sweep name.
func (AutomaticVoiceSweepArgs) Kind() string { return "voice_automatic_sweep" }

type automaticVoiceSweepWorker struct {
	river.WorkerDefaults[AutomaticVoiceSweepArgs]
	store *ai.VoiceStore
	log   *slog.Logger
}

func (w *automaticVoiceSweepWorker) Work(ctx context.Context, _ *river.Job[AutomaticVoiceSweepArgs]) error {
	client := river.ClientFromContext[pgx.Tx](ctx)
	candidates, scanErr := w.store.DueAutomaticProfiles(ctx)
	buildErrors := scanErr
	for _, candidate := range candidates {
		candidateContext := principal.WithWorkspaceID(ctx, candidate.WorkspaceID)
		candidateContext = principal.WithActor(candidateContext, principal.Principal{
			Type: principal.PrincipalSystem, ID: "system:voice-learning", UserID: candidate.OwnerID, OnBehalfOf: candidate.OwnerID,
		})
		candidateContext = principal.WithCorrelationID(candidateContext, ids.NewV7())
		_, err := w.store.StartBuild(candidateContext, candidate.ProfileID, "automatic", func(ctx context.Context, tx pgx.Tx, build ai.VoiceBuild) error {
			_, err := client.InsertTx(ctx, tx, VoiceBuildArgs{
				WorkspaceID: candidate.WorkspaceID, ProfileID: build.ProfileID,
				BuildID: build.ID, RequestedBy: candidate.OwnerID,
			}, voiceBuildInsertOpts())
			return err
		})
		if err != nil {
			w.log.WarnContext(candidateContext, "automatic voice build enqueue failed", "profile", candidate.ProfileID.String(), "err", err)
			buildErrors = errors.Join(buildErrors, fmt.Errorf("profile %s: %w", candidate.ProfileID, err))
		}
	}
	return buildErrors
}
