// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The role declarations are load-bearing — jobs.WorkspaceScoped is what binds
// a job's workspace, and the G1 fitness gate reads both markers — so they are
// asserted at COMPILE time here as well as walked by the gate. A misspelled
// accessor or a marker on the wrong type is then a build error, which is where
// a contributor will actually see it, rather than a gate failure one layer out.

import "github.com/gradionhq/margince/backend/internal/platform/jobs"

var (
	_ jobs.WorkspaceScoped = AgentSchedulerWorkspaceArgs{}
	_ jobs.WorkspaceScoped = CaptureBackfillArgs{}
	_ jobs.WorkspaceScoped = CloseDateWorkspaceArgs{}
	_ jobs.WorkspaceScoped = FollowUpWorkspaceArgs{}
	_ jobs.WorkspaceScoped = TimeScanWorkspaceArgs{}
	_ jobs.WorkspaceScoped = IdempotencyRetentionWorkspaceArgs{}
	_ jobs.WorkspaceScoped = PrivacyRetentionWorkspaceArgs{}
	_ jobs.WorkspaceScoped = CaptureAutoEnrichWorkspaceArgs{}
	_ jobs.WorkspaceScoped = EmbedDriftWorkspaceArgs{}
	_ jobs.WorkspaceScoped = GmailWatchRenewArgs{}
	_ jobs.WorkspaceScoped = OverlayReconcileWorkspaceArgs{}
	_ jobs.WorkspaceScoped = GraphEdgeWorkspaceArgs{}
	_ jobs.WorkspaceScoped = ParticipantBackfillWorkspaceArgs{}
	_ jobs.WorkspaceScoped = LinkedInRematchWorkspaceArgs{}
	_ jobs.WorkspaceScoped = OrgNamePromotionWorkspaceArgs{}
	_ jobs.WorkspaceScoped = CaptureClassifyWorkspaceArgs{}
	_ jobs.WorkspaceScoped = CaptureEnrichWorkspaceArgs{}
	_ jobs.WorkspaceScoped = CaptureDigestWorkspaceArgs{}
	_ jobs.WorkspaceScoped = CounterpartyVerdictWorkspaceArgs{}
	_ jobs.WorkspaceScoped = CaptureSyncArgs{}
	_ jobs.WorkspaceScoped = FxRateRefreshArgs{}
	_ jobs.WorkspaceScoped = AiModelRateRefreshArgs{}
	_ jobs.WorkspaceScoped = OverlayRefetchArgs{}
	_ jobs.WorkspaceScoped = SendEmailArgs{}
	_ jobs.WorkspaceScoped = SiteDeepReadArgs{}
	_ jobs.WorkspaceScoped = TelegramIngestArgs{}
	_ jobs.WorkspaceScoped = TelegramPollArgs{}
	_ jobs.WorkspaceScoped = VoiceBuildArgs{}
	_ jobs.WorkspaceScoped = WebhookRetryWorkspaceArgs{}
)

var (
	_ jobs.FleetWide = AgentSchedulerArgs{}
	_ jobs.FleetWide = CaptureAutoEnrichSweepArgs{}
	_ jobs.FleetWide = CaptureClassifyArgs{}
	_ jobs.FleetWide = CaptureDigestArgs{}
	_ jobs.FleetWide = CaptureEnrichArgs{}
	_ jobs.FleetWide = CloseDateSweepArgs{}
	_ jobs.FleetWide = CounterpartyVerdictArgs{}
	_ jobs.FleetWide = EmbedDriftSweepArgs{}
	_ jobs.FleetWide = FollowUpReconcileArgs{}
	_ jobs.FleetWide = GmailSyncArgs{}
	_ jobs.FleetWide = GmailWatchArgs{}
	_ jobs.FleetWide = GraphEdgeReconcileArgs{}
	_ jobs.FleetWide = IdempotencyRetentionArgs{}
	_ jobs.FleetWide = LinkedInRematchArgs{}
	_ jobs.FleetWide = OrgNamePromotionArgs{}
	_ jobs.FleetWide = OverlayReconcileArgs{}
	_ jobs.FleetWide = ParticipantBackfillArgs{}
	_ jobs.FleetWide = PrivacyRetentionArgs{}
	_ jobs.FleetWide = TelegramPollSweepArgs{}
	_ jobs.FleetWide = TimeScanArgs{}
	_ jobs.FleetWide = VoiceBuildRetryArgs{}
	_ jobs.FleetWide = WebhookRetryArgs{}
	_ jobs.FleetWide = embedReindexArgs{}
)
