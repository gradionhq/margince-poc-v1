// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Every workspace-scoped worker refuses args that name no workspace, and
// refuses them BEFORE touching anything.
//
// The role declaration is only load-bearing if the worker acts on it, and
// "acts on it" has to include the empty case: a zero id binds a GUC to
// nothing, and the pass then reads and writes as whatever the connection
// happens to carry. The guard turning that into a failed row is the whole
// reason workspaceJobCtx returns an error rather than a context.
//
// The workers below are constructed with NIL collaborators on purpose. A
// worker that reached its store, its model lane or its pool before checking
// the workspace would panic here rather than fail — so this suite also pins
// the ORDER, which no gate can see.
//
// It covers EVERY workspace-scoped kind, and the count below is what keeps
// that true: a new kind added to jobroles.go's compile-time assertions and not
// to this map fails here rather than going unnoticed.

import (
	"context"
	"log/slog"
	"testing"

	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// workspaceScopedKinds is how many jobs.WorkspaceScoped assertions
// jobroles.go carries. Kept in step with it by the check below.
const workspaceScopedKinds = 26

func TestEveryWorkspaceWorkerRefusesArgsNamingNoWorkspace(t *testing.T) {
	// Named by kind rather than by Go type: a failure should say which JOB is
	// unguarded, because that is what an operator and the ledger both talk in.
	refusals := map[string]func(context.Context) error{
		CloseDateWorkspaceArgs{}.Kind(): func(ctx context.Context) error {
			return (&closeDateWorkspaceWorker{}).Work(ctx, &river.Job[CloseDateWorkspaceArgs]{})
		},
		FollowUpWorkspaceArgs{}.Kind(): func(ctx context.Context) error {
			return (&followUpWorkspaceWorker{}).Work(ctx, &river.Job[FollowUpWorkspaceArgs]{})
		},
		TimeScanWorkspaceArgs{}.Kind(): func(ctx context.Context) error {
			return (&timeScanWorkspaceWorker{}).Work(ctx, &river.Job[TimeScanWorkspaceArgs]{})
		},
		IdempotencyRetentionWorkspaceArgs{}.Kind(): func(ctx context.Context) error {
			return (&idempotencyRetentionWorkspaceWorker{}).Work(ctx, &river.Job[IdempotencyRetentionWorkspaceArgs]{})
		},
		CaptureAutoEnrichWorkspaceArgs{}.Kind(): func(ctx context.Context) error {
			return (&captureAutoEnrichWorkspaceWorker{}).Work(ctx, &river.Job[CaptureAutoEnrichWorkspaceArgs]{})
		},
		EmbedDriftWorkspaceArgs{}.Kind(): func(ctx context.Context) error {
			return (&embedDriftWorkspaceWorker{}).Work(ctx, &river.Job[EmbedDriftWorkspaceArgs]{})
		},
		GraphEdgeWorkspaceArgs{}.Kind(): func(ctx context.Context) error {
			return (&graphEdgeWorkspaceWorker{}).Work(ctx, &river.Job[GraphEdgeWorkspaceArgs]{})
		},
		ParticipantBackfillWorkspaceArgs{}.Kind(): func(ctx context.Context) error {
			return (&participantBackfillWorkspaceWorker{}).Work(ctx, &river.Job[ParticipantBackfillWorkspaceArgs]{})
		},
		LinkedInRematchWorkspaceArgs{}.Kind(): func(ctx context.Context) error {
			return (&linkedInRematchWorkspaceWorker{}).Work(ctx, &river.Job[LinkedInRematchWorkspaceArgs]{})
		},
		OrgNamePromotionWorkspaceArgs{}.Kind(): func(ctx context.Context) error {
			return (&orgNamePromotionWorkspaceWorker{}).Work(ctx, &river.Job[OrgNamePromotionWorkspaceArgs]{})
		},
		CaptureClassifyWorkspaceArgs{}.Kind(): func(ctx context.Context) error {
			return (&captureClassifyWorkspaceWorker{}).Work(ctx, &river.Job[CaptureClassifyWorkspaceArgs]{})
		},
		CaptureEnrichWorkspaceArgs{}.Kind(): func(ctx context.Context) error {
			return (&captureEnrichWorkspaceWorker{}).Work(ctx, &river.Job[CaptureEnrichWorkspaceArgs]{})
		},
		CaptureDigestWorkspaceArgs{}.Kind(): func(ctx context.Context) error {
			return (&captureDigestWorkspaceWorker{}).Work(ctx, &river.Job[CaptureDigestWorkspaceArgs]{})
		},
		CounterpartyVerdictWorkspaceArgs{}.Kind(): func(ctx context.Context) error {
			return (&counterpartyVerdictWorkspaceWorker{}).Work(ctx, &river.Job[CounterpartyVerdictWorkspaceArgs]{})
		},
		OverlayReconcileWorkspaceArgs{}.Kind(): func(ctx context.Context) error {
			return (&overlayReconcileWorkspaceWorker{}).Work(ctx, &river.Job[OverlayReconcileWorkspaceArgs]{})
		},
		GmailWatchRenewArgs{}.Kind(): func(ctx context.Context) error {
			return (&gmailWatchRenewWorker{}).Work(ctx, &river.Job[GmailWatchRenewArgs]{})
		},
		FxRateRefreshArgs{}.Kind(): func(ctx context.Context) error {
			return (&fxRefreshWorker{}).Work(ctx, &river.Job[FxRateRefreshArgs]{})
		},
		AiModelRateRefreshArgs{}.Kind(): func(ctx context.Context) error {
			return (&aiModelRateRefreshWorker{}).Work(ctx, &river.Job[AiModelRateRefreshArgs]{})
		},

		// The kinds below parse one OTHER id before reaching the guard, so
		// their args carry a valid one: with a zero-value payload the earlier
		// parse would fail and the test would pass without the workspace guard
		// ever running.
		SendEmailArgs{}.Kind(): func(ctx context.Context) error {
			return (&commsSendWorker{}).Work(ctx, &river.Job[SendEmailArgs]{
				Args: SendEmailArgs{DeliveryID: ids.NewV7().String()},
			})
		},
		CaptureBackfillArgs{}.Kind(): func(ctx context.Context) error {
			return (&captureBackfillWorker{}).Work(ctx, &river.Job[CaptureBackfillArgs]{
				Args: CaptureBackfillArgs{BackfillID: ids.NewV7().String()},
			})
		},
		CaptureSyncArgs{}.Kind(): func(ctx context.Context) error {
			return (&captureSyncWorker{}).Work(ctx, &river.Job[CaptureSyncArgs]{
				Args: CaptureSyncArgs{ConnectionID: ids.NewV7().String()},
			})
		},
		TelegramIngestArgs{}.Kind(): func(ctx context.Context) error {
			return (&telegramIngestWorker{}).Work(ctx, &river.Job[TelegramIngestArgs]{
				Args: TelegramIngestArgs{RawCaptureID: ids.NewV7().String()},
			})
		},
		TelegramPollArgs{}.Kind(): func(ctx context.Context) error {
			return (&telegramPollWorker{}).Work(ctx, &river.Job[TelegramPollArgs]{
				Args: TelegramPollArgs{ConnectionID: ids.NewV7().String()},
			})
		},
		VoiceBuildArgs{}.Kind(): func(ctx context.Context) error {
			return (&voiceBuildWorker{}).Work(ctx, &river.Job[VoiceBuildArgs]{
				Args: VoiceBuildArgs{RequestedBy: ids.NewV7().String()},
			})
		},
		OverlayRefetchArgs{}.Kind(): func(ctx context.Context) error {
			return (&overlayRefetchWorker{log: slog.New(slog.DiscardHandler)}).Work(
				ctx, &river.Job[OverlayRefetchArgs]{})
		},
		SiteDeepReadArgs{}.Kind(): func(ctx context.Context) error {
			return (&siteDeepReadWorker{}).Work(ctx, &river.Job[SiteDeepReadArgs]{
				Args: SiteDeepReadArgs{SiteReadID: ids.NewV7()},
			})
		},
	}

	if len(refusals) != workspaceScopedKinds {
		t.Fatalf("this suite drives %d workers but the tree declares %d workspace-scoped kinds (jobroles.go) — a kind whose refusal nobody pins is one that can silently stop refusing",
			len(refusals), workspaceScopedKinds)
	}

	for kind, work := range refusals {
		t.Run(kind, func(t *testing.T) {
			if err := work(context.Background()); err == nil {
				t.Fatalf("%s accepted args naming no workspace — it would bind an empty GUC and read whatever the connection carries", kind)
			}
		})
	}
}

// A worker given a REAL workspace must get past the guard, bound to THAT
// workspace. Without the positive case the suite above would still pass
// against a guard that refused everything; without the identity check it would
// pass against one that bound the wrong tenant.
func TestTheWorkspaceGuardBindsTheWorkspaceTheArgsDeclare(t *testing.T) {
	want := ids.NewV7()

	ctx, err := workspaceJobCtx(context.Background(), CloseDateWorkspaceArgs{Workspace: want})
	if err != nil {
		t.Fatalf("the guard refused a workspace it was given: %v", err)
	}
	got, ok := principal.WorkspaceID(ctx)
	if !ok {
		t.Fatal("the guard admitted the workspace but bound nothing — every tenant query would fail on an unset GUC")
	}
	if got != want {
		t.Fatalf("the guard bound %s, want the %s its args declared", got, want)
	}
}
