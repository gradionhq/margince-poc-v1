// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert

// How much of a task a record may claim. Two things decide it and neither is
// the scenario: the KIND of every site the run touched, which says the most a
// run of that site could cover, and the CASE bound to each, which says what
// this build's run does cover. A record carries the narrowest of them.

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

// TestCertifyTaskRecordsTheNarrowestScopeItsSitesCover: a task is one record
// but not always one site — cold_start ships a one-shot extraction beside
// three multi-turn conversations — and a scenario on a multi-turn site seeds
// the window and grades a single reply. A record pooling that with a one-shot
// site and still saying "full_invocation" would claim the conversation was
// exercised.
func TestCertifyTaskRecordsTheNarrowestScopeItsSitesCover(t *testing.T) {
	conversation := aitasks.Site{Task: ai.TaskSummarize, Variant: "widget_conversation", Kind: ai.SiteKindMultiTurn}
	cases := []struct {
		name  string
		sites []aitasks.Site
		want  string
	}{
		{"every site is one-shot", []aitasks.Site{widgetSite()}, aitasks.ScopeFullInvocation},
		{"one site is multi-turn", []aitasks.Site{widgetSite(), conversation}, aitasks.ScopeSingleTurn},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scenarios := make([]Scenario, 0, len(tc.sites))
			replies := make([]string, 0, len(tc.sites))
			scores := make([]string, 0, len(tc.sites))
			for _, s := range tc.sites {
				scenarios = append(scenarios, testScenarioOnSite(s.Variant, s.Variant, wideBands))
				replies = append(replies, "the widget is blue and durable")
				scores = append(scores, scoreJSON(90))
			}
			rec, err := certifyTask(wsContext(t), ai.TaskSummarize, scenarios, censusOfSites(t, tc.sites...),
				ai.FakeRoutingConfig(), "", 1, quietLogger(), &certifyHooks{
					candidateOpts: []ai.LocalOption{ai.WithFakeClient(ai.NewFakeClient().Script(replies...))},
					judgeOpts:     []ai.LocalOption{ai.WithFakeClient(ai.NewFakeClient().Script(scores...))},
				})
			if err != nil {
				t.Fatalf("certifyTask: %v", err)
			}
			if rec.CertifiedScope != tc.want {
				t.Fatalf("certified_scope = %q, want %q (record: %+v)", rec.CertifiedScope, tc.want, rec)
			}
		})
	}
}

// wsContext mints the fixed DB-less workspace principal every router
// call in this package's tests needs, mirroring ensureWorkspace's own
// production behavior so a direct certifyTask call (bypassing Run,
// which calls ensureWorkspace itself) still has one.
func wsContext(t *testing.T) context.Context {
	t.Helper()
	return ensureWorkspace(context.Background())
}

// A site's KIND says the most a run of it could cover; the CASE says what this
// build's run does cover. A one-shot site whose case makes one of the calls the
// site makes would otherwise put full_invocation on the record on the strength
// of its shape, and the number a reader trusts most would be the one nobody
// checked.
func TestCertifyTaskRecordsTheScopeTheCaseDeclares(t *testing.T) {
	site := widgetSite()
	census := aitasks.NewRegistry()
	census.Register(site)
	census.BindCase(site, narrowedCases{widgetCases{site: site}})

	rec, err := certifyTask(wsContext(t), ai.TaskSummarize,
		[]Scenario{testScenarioOnSite(site.Variant, site.Variant, wideBands)},
		census, ai.FakeRoutingConfig(), "", 1, quietLogger(), &certifyHooks{
			candidateOpts: []ai.LocalOption{ai.WithFakeClient(ai.NewFakeClient().Script("the widget is blue and durable"))},
			judgeOpts:     []ai.LocalOption{ai.WithFakeClient(ai.NewFakeClient().Script(scoreJSON(90)))},
		})
	if err != nil {
		t.Fatalf("certifyTask: %v", err)
	}
	if rec.CertifiedScope != aitasks.ScopeSingleCall {
		t.Fatalf("certified_scope = %q, want the case's own %q (record: %+v)",
			rec.CertifiedScope, aitasks.ScopeSingleCall, rec)
	}
}
