// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import "testing"

// The generated accessors are the census's source of truth. These pin the
// shape every consumer reads, so a generator change that silently drops a
// table fails here rather than in the subsystem that depended on it.
func TestGeneratedDeclarationAccessors(t *testing.T) {
	if got := Status(TaskCaptureCounterpartyVerdict); got != StatusShipped {
		t.Errorf("Status(verdict) = %q, want %q", got, StatusShipped)
	}
	// deal_health stands in for the planned half of the table. summarize
	// used to: it shipped with the account brief, which is exactly the
	// transition this accessor exists to report.
	if got := Status(TaskDealHealth); got != StatusPlanned {
		t.Errorf("Status(deal_health) = %q, want %q", got, StatusPlanned)
	}
	if !NoPayload(TaskCaptureCounterpartyVerdict) {
		t.Error("NoPayload(verdict) = false; the contract pins it true")
	}
	if NoPayload(TaskDraftReply) {
		t.Error("NoPayload(draft_reply) = true; only the verdict task is pinned")
	}

	rate := SitesFor(TaskRateExtract)
	if len(rate) != 2 || rate[0].Name != "pricing" || rate[1].Name != "fx" {
		t.Fatalf("SitesFor(rate_extract) = %+v, want pricing then fx", rate)
	}
	if rate[0].Kind != SiteKindOneShot {
		t.Errorf("a bare site got kind %q, want %q", rate[0].Kind, SiteKindOneShot)
	}
	if loop := SitesFor(TaskAgentLoop); len(loop) != 1 || loop[0].Kind != SiteKindAgentLoop {
		t.Errorf("SitesFor(agent_loop) = %+v, want one agent_loop site", loop)
	}
	if got := SitesFor(TaskDealHealth); len(got) != 0 {
		t.Errorf("a planned task declares sites: %+v", got)
	}

	policy, ok := CompanyContextFor(TaskDraftReply)
	if !ok {
		t.Fatal("draft_reply has no company-context policy; every task must declare one")
	}
	if policy.TokenBudget != 1400 || len(policy.Scopes) != 4 {
		t.Errorf("draft_reply policy = %+v, want 4 scopes at 1400", policy)
	}
	if p, ok := CompanyContextFor(TaskCaptureCounterpartyVerdict); !ok || len(p.Scopes) != 0 {
		t.Errorf("verdict policy = %+v (declared %v), want a declared empty policy", p, ok)
	}
	if p, _ := CompanyContextFor(TaskSummarize); !p.Conditional {
		t.Error("summarize's policy must stay conditional")
	}

	if got := CostUnitFor(TaskCaptureClassify); got != "per_message" {
		t.Errorf("CostUnitFor(capture_classify) = %q, want per_message", got)
	}
	if got := CostUnitFor(TaskDraftReply); got != "" {
		t.Errorf("CostUnitFor(draft_reply) = %q, want no rule", got)
	}
	if got := EmbedCostUnit(); got != "per_entity" {
		t.Errorf("EmbedCostUnit() = %q, want per_entity", got)
	}
}

// Every task declares a company-context policy. Its absence used to be a
// RUNTIME error on the call (compose/companycontextprompt.go); the contract
// makes it a build-time fact.
func TestEveryTaskDeclaresACompanyContextPolicy(t *testing.T) {
	for _, task := range AllTasks() {
		if _, ok := CompanyContextFor(task); !ok {
			t.Errorf("task %q declares no company_context — declare `none` explicitly", task)
		}
	}
}
