// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The composition's AI invocation-site census. Every cross-module edge is
// injected in this layer, and a model-invocation site is one: the task
// contract names it, this package builds it. A process role that wires no
// model path never calls this.

import (
	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

// NewTaskCensus registers this build's AI invocation sites with the
// certification case that serves each, and validates them against the task
// contract. The error names every mismatch at once.
//
// The list is written out rather than derived from the contract on purpose: a
// loop over ai.SitesFor would make Validate compare the contract to itself and
// pass no matter what this build actually implements. Each line below is a
// claim that a site exists here and that this case certifies it, and Validate
// is what holds those claims to the contract — including that the case agrees
// with the line it sits on. The case type is the navigable link to the code:
// it is the site's only compiler-checked address.
func NewTaskCensus() (*aitasks.Registry, error) {
	r := aitasks.NewRegistry()
	register := func(kind string, task ai.Task, variant string, c aitasks.CaseFactory) {
		site := aitasks.Site{Task: task, Variant: variant, Kind: kind}
		r.Register(site)
		r.BindCase(site, c)
	}
	oneShot := func(task ai.Task, variant string, c aitasks.CaseFactory) {
		register(ai.SiteKindOneShot, task, variant, c)
	}
	multiTurn := func(task ai.Task, variant string, c aitasks.CaseFactory) {
		register(ai.SiteKindMultiTurn, task, variant, c)
	}
	agentLoop := func(task ai.Task, variant string, c aitasks.CaseFactory) {
		register(ai.SiteKindAgentLoop, task, variant, c)
	}

	oneShot(ai.TaskCaptureClassify, "classify", captureClassifyCases{})
	oneShot(ai.TaskCaptureCounterpartyVerdict, "verdict", counterpartyVerdictCases{})
	oneShot(ai.TaskEnrich, "signature", signatureEnrichCases{})
	oneShot(ai.TaskDraftReply, "reply", replyDraftCases{})
	oneShot(ai.TaskBriefRanking, "rank", briefRankingCases{})
	oneShot(ai.TaskOfferDraft, "draft", offerDraftCases{})
	oneShot(ai.TaskSignalExtract, "thread_events", signalExtractCases{})
	oneShot(ai.TaskSiteExtract, "profile", siteProfileCases{})
	oneShot(ai.TaskSiteFactExtract, "page_facts", sitePageFactsCases{})
	oneShot(ai.TaskSiteTriage, "triage", siteTriageCases{})
	oneShot(ai.TaskSummarize, "org_brief", orgBriefCases{})
	oneShot(ai.TaskSummarize, "org_ask", orgAskCases{})
	oneShot(ai.TaskSummarize, "org_dossier", orgDossierCases{})
	oneShot(ai.TaskGrowthFit, "growth_fit", growthFitCases{})
	oneShot(ai.TaskCertJudge, "judge", certJudgeCases{})
	oneShot(ai.TaskRateExtract, "pricing", ratePricingCases{})
	oneShot(ai.TaskRateExtract, "fx", rateFxCases{})
	oneShot(ai.TaskVoiceBuild, "derive", voiceDeriveCases{})
	oneShot(ai.TaskVoiceBuild, "eval_draft", voiceEvalDraftCases{})
	oneShot(ai.TaskVoiceBuild, "eval_scores", voiceEvalScoresCases{})
	oneShot(ai.TaskColdStart, "field_extract", fieldExtractCases{})
	multiTurn(ai.TaskColdStart, "company_message", onboardingCompanyMessageCases{})
	multiTurn(ai.TaskColdStart, "sitereadmessage", companyReadMessageCases{})
	multiTurn(ai.TaskColdStart, "acts", onboardingActCases{})
	agentLoop(ai.TaskAgentLoop, "loop", agentLoopCases{})

	return r, r.Validate()
}
