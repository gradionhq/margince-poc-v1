// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The queue set. Each entry's bound is a deliberate posture, not a tuning
// knob: a long, outbound-bound or model-bound pass gets its own pool so it
// cannot evict the short maintenance jobs from the default queue.

import "github.com/riverqueue/river"

func jobQueues() map[string]river.QueueConfig {
	return map[string]river.QueueConfig{
		river.QueueDefault: {MaxWorkers: 5},
		// Deep reads run on their own bounded pool so long crawls cannot
		// evict the short maintenance jobs from the default queue.
		deepReadQueue: {MaxWorkers: deepReadMaxWorkers},
		// Rate refreshes (FX fetch + pricing-page crawl+LLM extract) are
		// likewise long; their own bounded pool keeps a multi-workspace
		// burst from starving close-date, reconcile, and capture jobs.
		rateRefreshQueue: {MaxWorkers: rateRefreshMaxWorkers},
		// The AI-backed capture passes make serial model calls, so a
		// fanned-out fleet of them would occupy every default worker and
		// delay sends, Telegram polls, and capture syncs. Same species as
		// deep reads — long and model-bound — so the same posture.
		aiCaptureQueue: {MaxWorkers: aiCaptureMaxWorkers},
		// Overlay reconcile is SERIAL by design. overlaybudget.ConsumeSearch
		// counts but does not pace, and its keys are per workspace, so it
		// cannot bound a provider-level burst: a concurrent fan-out could
		// exceed the incumbent's per-second Search limit. Each workspace
		// still gets its own job row, which is the observability this phase
		// is after; per-workspace PARALLELISM is not.
		overlayReconcileQueue: {MaxWorkers: 1},
		// A full batch of sequential calls to endpoints this deployment
		// does not control: long and outbound-bound, so the same posture
		// deep reads take (webhookRetryQueue).
		webhookRetryQueue: {MaxWorkers: webhookRetryMaxWorkers},
		// A batch of agent runs, each entitled to the full RunWallClock —
		// the longest pass in the tree (agentSchedulerQueue).
		agentSchedulerQueue: {MaxWorkers: agentSchedulerMaxWorkers},
		// A full batch per policy, each record its own multi-statement
		// audited transaction — minutes of database-bound work per tenant
		// (privacyRetentionQueue).
		privacyRetentionQueue: {MaxWorkers: privacyRetentionMaxWorkers},
	}
}
