// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// Task names one V1 AI workload. Routing is over capability tiers per
// task (ai-operational-spec §1.2); code never names a vendor.
type Task string

const (
	TaskCaptureClassify Task = "capture_classify"
	TaskEnrich          Task = "enrich"
	TaskSummarize       Task = "summarize"
	TaskDraftReply      Task = "draft_reply"
	TaskNLSearch        Task = "nl_search"
	TaskColdStart       Task = "cold_start"
	TaskTranscript      Task = "transcript"
	TaskDealHealth      Task = "deal_health"
	TaskBriefRanking    Task = "brief_ranking"
	// TaskAgentLoop is the Surface-B reason-act-observe runner: judgment
	// over tools and evidence, routed like the other reasoning tasks.
	TaskAgentLoop Task = "agent_loop"
	// TaskOfferDraft is the offer regenerate-from-signal drafting call: an
	// evidence-grounded structured-output pass, routed like the other
	// extraction tasks (cold-start, enrich).
	TaskOfferDraft Task = "offer_draft"
	// TaskSiteExtract is the deep site read's page extraction — its own
	// dial, separate from the interactive cold-start read-back, so an
	// installation can put the crawl's many background calls on a
	// different tier than onboarding without touching either ladder in
	// code.
	TaskSiteExtract Task = "site_extract"
)

// Tier is a capability tier (§1.1); ai-routing.yaml binds each to a
// provider+model per deployment.
type Tier string

const (
	TierLocalSmall Tier = "local_small" // L-S
	TierCheapCloud Tier = "cheap_cloud" // C-C
	TierPremium    Tier = "premium"     // P-F
	TierLocalLarge Tier = "local_large" // L-L
)

// taskLadders is the §1.2 routing table: primary tier first, then the
// fallback rungs fired on provider error or schema-validation failure.
var taskLadders = map[Task][]Tier{
	TaskCaptureClassify: {TierLocalSmall, TierCheapCloud},
	TaskEnrich:          {TierLocalSmall, TierCheapCloud},
	TaskSummarize:       {TierCheapCloud, TierPremium},
	TaskDraftReply:      {TierCheapCloud, TierPremium},
	TaskNLSearch:        {TierCheapCloud, TierPremium},
	TaskColdStart:       {TierCheapCloud, TierPremium},
	TaskTranscript:      {TierCheapCloud, TierPremium},
	TaskDealHealth:      {TierCheapCloud, TierPremium},
	// brief-ranking is the one task defaulting to Premium-frontier
	// (§1.2 RATIFY note): the genuinely multi-hop reasoning pass.
	TaskBriefRanking: {TierPremium, TierCheapCloud},
	TaskAgentLoop:    {TierCheapCloud, TierPremium},
	TaskOfferDraft:   {TierCheapCloud, TierPremium},
	TaskSiteExtract:  {TierCheapCloud, TierPremium},
}

// degradeTo is the one-tier-down move economy mode applies at 80–100%
// budget utilization (§1.3): premium demotes to cheap-cloud, cloud
// demotes to local-small, local-large to local-small.
var degradeTo = map[Tier]Tier{
	TierPremium:    TierCheapCloud,
	TierCheapCloud: TierLocalSmall,
	TierLocalLarge: TierLocalSmall,
	TierLocalSmall: TierLocalSmall,
}

// nonInteractive marks the tasks that queue rather than degrade when
// the budget is exhausted (§1.3 ≥100%): nothing is waiting on them
// interactively, so next-cycle budget beats reduced quality.
var nonInteractive = map[Task]bool{
	TaskCaptureClassify: true,
	TaskEnrich:          true,
	TaskBriefRanking:    true,
	TaskAgentLoop:       true,
	// The deep read is a queued job: at 100% budget it should wait for
	// next-cycle budget, not degrade to a model that extracts less.
	TaskSiteExtract: true,
}
