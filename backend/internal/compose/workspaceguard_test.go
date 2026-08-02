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

import (
	"context"
	"testing"

	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

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
	}

	for kind, work := range refusals {
		t.Run(kind, func(t *testing.T) {
			if err := work(context.Background()); err == nil {
				t.Fatalf("%s accepted args naming no workspace — it would bind an empty GUC and read whatever the connection carries", kind)
			}
		})
	}
}

// A worker given a REAL workspace must get past the guard. Without this the
// suite above would still pass against a worker that refused everything.
func TestTheWorkspaceGuardAdmitsARealWorkspace(t *testing.T) {
	ctx, err := workspaceJobCtx(context.Background(), CloseDateWorkspaceArgs{Workspace: ids.NewV7()})
	if err != nil {
		t.Fatalf("the guard refused a workspace it was given: %v", err)
	}
	if ctx == nil {
		t.Fatal("the guard admitted the workspace but returned no context to work under")
	}
}
