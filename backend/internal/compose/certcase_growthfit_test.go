// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the two company-view cases owe the certification lane: they send the
// request production sends, they read the reply with production's own parser,
// and they refuse a scenario that could never measure anything.
//
// That last part carries most of the weight. A corpus entry naming a record the
// fixture never supplied, or accepting every band, reports as coverage forever
// while proving nothing — and unlike a wrong assertion it never goes red.

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// One company, labelled the way a corpus scenario labels one.
const growthFitFixtureJSON = `{
	"profile_fields": [
		{"label":"their_offer","field":"offer_summary","value":"Load-shifting software"},
		{"label":"their_icp","field":"icp","value":"Energy-intensive manufacturers"}
	],
	"facts": [
		{"label":"their_stack","field":"technology","value":"SAP S/4HANA"}
	]
}`

func prepareGrowthFit(t *testing.T, expected string) aitasks.PreparedCase {
	t.Helper()
	prepared, err := growthFitCases{}.Prepare(
		json.RawMessage(growthFitFixtureJSON), json.RawMessage(expected))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	return prepared
}

// citingLane answers with an id it reads out of the REQUEST, which is the only
// place the minted ids exist — Prepare hands them to nobody. A stub told the
// ids up front would prove less than one that finds them in the prompt.
type citingLane struct {
	band    string
	dossier bool
}

var promptFieldID = regexp.MustCompile(`"[iI]d":"([0-9a-fA-F-]{36})"`)

func (l citingLane) Complete(_ context.Context, req model.Request) (model.Response, error) {
	found := promptFieldID.FindStringSubmatch(req.Messages[0].Content)
	if found == nil {
		return model.Response{}, errors.New("the request carried no record id to cite")
	}
	cite := `"evidence":[{"entity_type":"profile_field","entity_id":"` + found[1] + `"}]`
	if l.dossier {
		return model.Response{Text: `{"sections":[{"kind":"summary","sentences":[
			{"text":"They sell load-shifting software.","nature":"fact",` + cite + `}]}]}`}, nil
	}
	return model.Response{Text: `{"band":"` + l.band + `","positive_factors":[
		{"text":"They sell load-shifting software.","nature":"fact",` + cite + `}]}`}, nil
}

// refusingLane answers with prose the parser cannot read.
type refusingLane struct{}

func (refusingLane) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{Text: "I'm afraid I can't do that."}, nil
}

// The site declarations must match what ai-tasks.yaml declares, or the registry
// refuses to build — this pins the pair rather than leaving it to that failure.
func TestTheCompanyViewCasesDeclareTheSitesTheContractDoes(t *testing.T) {
	if site := (growthFitCases{}).Site(); site.Task != ai.TaskGrowthFit || site.Variant != "growth_fit" {
		t.Errorf("growth-fit site = %+v, want growth_fit/growth_fit", site)
	}
	if site := (orgDossierCases{}).Site(); site.Task != ai.TaskSummarize || site.Variant != "org_dossier" {
		t.Errorf("dossier site = %+v, want summarize/org_dossier", site)
	}
}

// A scenario no reply could fail measures nothing, and reads as coverage for as
// long as it stays in the corpus. Each of these is refused at Prepare rather
// than passing silently at run time.
func TestAScenarioThatCouldNeverDisagreeWithAReplyIsRefused(t *testing.T) {
	for name, expected := range map[string]string{
		"no cited record expected":      `{"cites":[],"bands":["strong"]}`,
		"a record the fixture lacks":    `{"cites":["their_revenue"],"bands":["strong"]}`,
		"no band accepted":              `{"cites":["their_offer"],"bands":[]}`,
		"every judgeable band accepted": `{"cites":["their_offer"],"bands":["strong","moderate","weak"]}`,
		"the abstention accepted":       `{"cites":["their_offer"],"bands":["unknown"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := (growthFitCases{}).Prepare(
				json.RawMessage(growthFitFixtureJSON), json.RawMessage(expected)); err == nil {
				t.Error("a scenario that measures nothing was accepted into the corpus")
			}
		})
	}
}

// The ids a reply must cite are minted by Prepare, so an id in a reply is one
// the model was handed rather than one the corpus author could have written in.
func TestTheGrowthFitCaseGradesTheReplyAgainstIdsItMinted(t *testing.T) {
	prepared := prepareGrowthFit(t, `{"cites":["their_offer"],"bands":["strong","moderate"]}`)
	trace, err := prepared.Run(context.Background(), citingLane{band: "strong"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := prepared.Evaluate(trace); got.Result != aitasks.OutcomeAccepted {
		t.Errorf("outcome = %v (%s), want accepted", got.Result, got.Detail)
	}
}

// A band outside the scenario's range is the failure this case exists to catch:
// calling a clear fit weak is wrong at the one thing the surface is for.
func TestTheGrowthFitCaseRejectsABandTheScenarioDoesNotAccept(t *testing.T) {
	prepared := prepareGrowthFit(t, `{"cites":["their_offer"],"bands":["strong"]}`)
	trace, err := prepared.Run(context.Background(), citingLane{band: "weak"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	got := prepared.Evaluate(trace)
	if got.Result != aitasks.OutcomeWrongAnswer {
		t.Errorf("outcome = %v, want wrong answer for a band outside the range", got.Result)
	}
	if !strings.Contains(got.Detail, "weak") {
		t.Errorf("detail = %q, want it to name the band that was given", got.Detail)
	}
}

// A reply production would discard is an abstention here, not a wrong answer:
// the two mean different things about the model and must not be averaged.
func TestAReplyProductionWouldDiscardIsReportedAsAnAbstention(t *testing.T) {
	prepared := prepareGrowthFit(t, `{"cites":["their_offer"],"bands":["strong"]}`)
	trace, err := prepared.Run(context.Background(), refusingLane{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := prepared.Evaluate(trace); got.Result != aitasks.OutcomeAbstained {
		t.Errorf("outcome = %v, want abstained for a reply the parser refuses", got.Result)
	}
}

// The dossier case grades the records a description had to rest on, not its
// wording — the whole reason that lane exists is that the same facts read
// better as prose, and pinning sentences would fail a good dossier.
func TestTheDossierCaseGradesTheRecordsADescriptionRestsOn(t *testing.T) {
	prepared, err := (orgDossierCases{}).Prepare(
		json.RawMessage(growthFitFixtureJSON), json.RawMessage(`["their_offer"]`))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	trace, err := prepared.Run(context.Background(), citingLane{dossier: true})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := prepared.Evaluate(trace); got.Result != aitasks.OutcomeAccepted {
		t.Errorf("outcome = %v (%s), want accepted", got.Result, got.Detail)
	}
}

// A dossier that described something else cites nothing of this company, which
// production shows as the deterministic floor rather than as prose.
func TestADossierCitingNothingOfThisCompanyAbstains(t *testing.T) {
	prepared, err := (orgDossierCases{}).Prepare(
		json.RawMessage(growthFitFixtureJSON), json.RawMessage(`["their_offer"]`))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	trace, err := prepared.Run(context.Background(), emptyDossierLane{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := prepared.Evaluate(trace); got.Result != aitasks.OutcomeAbstained {
		t.Errorf("outcome = %v, want abstained", got.Result)
	}
}

// emptyDossierLane answers with a well-formed reply that describes nothing.
type emptyDossierLane struct{}

func (emptyDossierLane) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{Text: `{"sections":[]}`}, nil
}
