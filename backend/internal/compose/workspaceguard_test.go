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
// WHICH workers it must drive is the contract's to say: the set is every
// workspace-role kind api/jobs.yaml declares, read from the declaration below
// rather than counted. A count is satisfied by any N entries, so a kind added
// to the file and forgotten here would leave the number right and its guard
// unproven — which is the same silence the suite exists to break.

import (
	"context"
	"log/slog"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// workspaceKindFloor guards against a vacuous pass. The contract declares 31
// workspace kinds today; the floor sits low enough that retiring a few passes
// does not drag it along, and high enough that a declaration answering nothing
// — which would make the derivation below demand nothing — is reported rather
// than read as full coverage.
const workspaceKindFloor = 25

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
		SignalScanWorkspaceArgs{}.Kind(): func(ctx context.Context) error {
			return (&signalScanWorkspaceWorker{}).Work(ctx, &river.Job[SignalScanWorkspaceArgs]{})
		},
		AgentSchedulerWorkspaceArgs{}.Kind(): func(ctx context.Context) error {
			return (&agentSchedulerWorkspaceWorker{}).Work(ctx, &river.Job[AgentSchedulerWorkspaceArgs]{})
		},
		PrivacyRetentionWorkspaceArgs{}.Kind(): func(ctx context.Context) error {
			return (&privacyRetentionWorkspaceWorker{}).Work(ctx, &river.Job[PrivacyRetentionWorkspaceArgs]{})
		},
		WebhookRetryWorkspaceArgs{}.Kind(): func(ctx context.Context) error {
			return (&webhookRetryWorkspaceWorker{}).Work(ctx, &river.Job[WebhookRetryWorkspaceArgs]{})
		},

		// The reindex worker takes its LAST attempt out of the pending set
		// before returning, which is a store write; the row here is given an
		// attempt below its cap so the refusal is what the call answers with,
		// rather than a nil store panicking on the way to it.
		EmbedReindexWorkspaceArgs{}.Kind(): func(ctx context.Context) error {
			return (&embedReindexWorkspaceWorker{}).Work(ctx, &river.Job[EmbedReindexWorkspaceArgs]{
				JobRow: &rivertype.JobRow{Attempt: 1, MaxAttempts: 2},
			})
		},
	}

	declared := 0
	for kind, spec := range jobs.Declared() {
		if spec.Role != jobs.Workspace {
			continue
		}
		declared++
		if _, driven := refusals[kind]; !driven {
			t.Errorf("%s carries one tenant's pass, but nothing here proves it refuses args naming no workspace — drive its worker above, or it can stop refusing and no test will notice", kind)
		}
	}
	if declared < workspaceKindFloor {
		t.Fatalf("the contract declares only %d workspace kinds, expected at least %d — the derivation resolved almost nothing and this suite would demand almost nothing",
			declared, workspaceKindFloor)
	}
	for kind := range refusals {
		spec, declaredKind := jobs.SpecFor(kind)
		if !declaredKind || spec.Role != jobs.Workspace {
			t.Errorf("%s is driven here but api/jobs.yaml declares no workspace kind by that name — the suite is pinning something the fleet does not run", kind)
		}
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
