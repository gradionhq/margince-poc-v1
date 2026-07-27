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

// NewTaskCensus registers this build's AI invocation sites and validates them
// against the task contract. The error names every mismatch at once.
//
// The list is written out rather than derived from the contract on purpose: a
// loop over ai.SitesFor would make Validate compare the contract to itself and
// pass no matter what this build actually implements. Each line below is a
// claim that a site exists here, and Validate is what holds those claims to
// the contract. The trailing comment names where that site's prompt is built.
func NewTaskCensus() (*aitasks.Registry, error) {
	r := aitasks.NewRegistry()
	oneShot := func(task ai.Task, variant string) {
		r.Register(aitasks.Site{Task: task, Variant: variant, Kind: ai.SiteKindOneShot})
	}
	multiTurn := func(task ai.Task, variant string) {
		r.Register(aitasks.Site{Task: task, Variant: variant, Kind: ai.SiteKindMultiTurn})
	}
	agentLoop := func(task ai.Task, variant string) {
		r.Register(aitasks.Site{Task: task, Variant: variant, Kind: ai.SiteKindAgentLoop})
	}

	oneShot(ai.TaskCaptureClassify, "classify")           // captureclassify.go
	oneShot(ai.TaskCaptureCounterpartyVerdict, "verdict") // captureverdictask.go
	oneShot(ai.TaskEnrich, "signature")                   // captureenrich.go
	oneShot(ai.TaskDraftReply, "reply")                   // replydraft.go
	oneShot(ai.TaskBriefRanking, "rank")                  // briefs/briefl2.go
	oneShot(ai.TaskOfferDraft, "draft")                   // offerdraft.go
	oneShot(ai.TaskSiteExtract, "profile")                // siteprofile.go
	oneShot(ai.TaskSiteFactExtract, "page_facts")         // sitepagefacts.go
	oneShot(ai.TaskCertJudge, "judge")                    // aicert/judge.go
	oneShot(ai.TaskRateExtract, "pricing")                // modelraterefresh.go
	oneShot(ai.TaskRateExtract, "fx")                     // fxrefresh.go
	oneShot(ai.TaskVoiceBuild, "derive")                  // modules/ai/voicebuilder.go
	oneShot(ai.TaskVoiceBuild, "eval_draft")              // voicebuildeval.go
	oneShot(ai.TaskVoiceBuild, "eval_scores")             // voicebuildeval.go
	oneShot(ai.TaskColdStart, "field_extract")            // enrichextract.go
	multiTurn(ai.TaskColdStart, "company_message")        // onboardingcompanymessage.go
	multiTurn(ai.TaskColdStart, "sitereadmessage")        // onboardingsitereadmessage.go
	multiTurn(ai.TaskColdStart, "acts")                   // onboardingacts.go
	agentLoop(ai.TaskAgentLoop, "loop")                   // modules/agents/runner

	// The certification case each site is served by. Binding here rather than
	// inside the case's own file keeps one place to read what this build can
	// certify, and Validate refuses a case bound to a site nobody registered.
	r.BindCase(captureClassifyCases{})
	r.BindCase(counterpartyVerdictCases{})
	r.BindCase(fieldExtractCases{})
	r.BindCase(signatureEnrichCases{})
	r.BindCase(siteProfileCases{})
	r.BindCase(sitePageFactsCases{})
	r.BindCase(onboardingActCases{})

	return r, r.Validate()
}
