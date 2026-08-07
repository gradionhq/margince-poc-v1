// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The relationship-graph reads over HTTP (ADR-0078). These go through the real
// router, the real gates and the real payload shapes — the layer where a
// contract promise either holds or does not.

import (
	"net/http"
	"testing"
)

type networkColleagueDTO struct {
	UserID          string `json:"user_id"`
	DisplayName     string `json:"display_name"`
	Strength        *int   `json:"strength"`
	StrengthBucket  string `json:"strength_bucket"`
	Interactions90d int    `json:"interactions_90d"`
}

type personNetworkDTO struct {
	PersonID   string                `json:"person_id"`
	Colleagues []networkColleagueDTO `json:"colleagues"`
}

type coverageRiskDTO struct {
	Kind      string   `json:"kind"`
	Summary   string   `json:"summary"`
	PersonIDs []string `json:"person_ids"`
	UserIDs   []string `json:"user_ids"`
	// A pointer, so a test can tell "no day count sent" from "zero days" —
	// which is the whole distinction the field exists to keep.
	DaysSinceTouch *int `json:"days_since_touch"`
}

type dealCoverageDTO struct {
	DealID       string            `json:"deal_id"`
	Stakeholders []anyMap          `json:"stakeholders"`
	OurSide      []anyMap          `json:"our_side"`
	Risks        []coverageRiskDTO `json:"risks"`
}

func TestPersonNetworkAnswersHonestlyWhenNobodyKnowsThem(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	var person anyMap
	if status := e.call(t, "POST", "/v1/people",
		anyMap{"full_name": "Unknown Contact"}, nil, &person); status != http.StatusCreated {
		t.Fatalf("creating the contact: %d", status)
	}
	id, _ := person["id"].(string)

	// A contact nobody has spoken to answers 200 with an EMPTY list, not 404
	// and not an error. "The account is cold" is the useful answer here, and
	// the surface has to be able to say it.
	var got personNetworkDTO
	if status := e.call(t, "GET", "/v1/people/"+id+"/network", nil, nil, &got); status != http.StatusOK {
		t.Fatalf("network status = %d, want 200", status)
	}
	if got.PersonID != id {
		t.Errorf("payload names person %s, want %s", got.PersonID, id)
	}
	if len(got.Colleagues) != 0 {
		t.Errorf("a contact with no interactions has %d colleagues", len(got.Colleagues))
	}
}

func TestPersonNetworkHidesAContactTheCallerCannotRead(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	// A person id that does not exist is indistinguishable from one this
	// caller may not see — existence is not disclosed, here as everywhere.
	var body anyMap
	if status := e.call(t, "GET",
		"/v1/people/019fb000-0000-7000-8000-00000000dead/network", nil, nil, &body); status != http.StatusNotFound {
		t.Errorf("an unknown contact answered %d, want 404 — a network read must not "+
			"confirm that a record exists when the person read would not", status)
	}
}

func TestDealCoverageFlagsAThreadlessDealAndExplainsWhy(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	var pipelines anyMap
	if status := e.call(t, "GET", "/v1/pipelines", nil, nil, &pipelines); status != http.StatusOK {
		t.Fatalf("listing pipelines: %d", status)
	}
	pipeline, stage := firstPipelineAndStage(t, pipelines)

	var deal anyMap
	if status := e.call(t, "POST", "/v1/deals", anyMap{
		"name": "Threadless", "pipeline_id": pipeline, "stage_id": stage, "source": "ui",
	}, nil, &deal); status != http.StatusCreated {
		t.Fatalf("creating the deal: %d", status)
	}
	dealID, _ := deal["id"].(string)

	var got dealCoverageDTO
	if status := e.call(t, "GET", "/v1/deals/"+dealID+"/coverage", nil, nil, &got); status != http.StatusOK {
		t.Fatalf("coverage status = %d, want 200", status)
	}
	if got.DealID != dealID {
		t.Errorf("payload names deal %s, want %s", got.DealID, dealID)
	}
	// REPORT-PARAM-1 verbatim: zero engaged contacts is below the floor of
	// two, so a deal with nobody on it reads as single-threaded — the same
	// answer the reporting surface gives, which is the point of reusing the
	// rule rather than inventing one.
	var found *coverageRiskDTO
	for i := range got.Risks {
		if got.Risks[i].Kind == "single_threaded_theirs" {
			found = &got.Risks[i]
		}
	}
	if found == nil {
		t.Fatalf("a deal with no engaged contacts raised no single-threaded risk: %+v", got.Risks)
	}
	if found.Summary == "" {
		t.Error("the risk carries no reason — a flag a human cannot read is a red dot")
	}
}

func TestDealCoverageHidesADealTheCallerCannotRead(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	// The coverage payload names the deal's people, so a caller who cannot
	// read the deal must not learn who sits on it.
	var body anyMap
	if status := e.call(t, "GET",
		"/v1/deals/019fb000-0000-7000-8000-00000000beef/coverage", nil, nil, &body); status != http.StatusNotFound {
		t.Errorf("an unknown deal answered %d, want 404", status)
	}
}

func TestDealCoverageDistinguishesADepartedChampionFromADepartedStakeholder(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	org, deal := e.dealAtAnAccount(t, "Bär Pharma", "Renewal")

	// Two people who used to work there and one who still does. The rule
	// demands EVIDENCE of a departure — an ended employment plus no live one —
	// so the third person proves the flag is about leaving and not about
	// having no employment row.
	gone := e.contactAt(t, org, "Departed Champion", "2026-01-31")
	alsoGone := e.contactAt(t, org, "Departed Legal", "2026-02-28")
	stayed := e.contactAt(t, org, "Still There", "")

	e.stakeholder(t, deal, gone, "champion")
	e.stakeholder(t, deal, alsoGone, "legal")
	e.stakeholder(t, deal, stayed, "user")

	risks := e.coverageRisks(t, deal)
	champion := findRisk(risks, "champion_left")
	if champion == nil {
		t.Fatalf("the champion left the account and no champion_left risk fired: %+v", risks)
	}
	if len(champion.PersonIDs) != 1 || champion.PersonIDs[0] != gone {
		t.Errorf("champion_left names %v, want only the departed champion %s", champion.PersonIDs, gone)
	}

	stakeholder := findRisk(risks, "stakeholder_left")
	if stakeholder == nil {
		t.Fatalf("a non-champion seat left the account and no stakeholder_left risk fired: %+v", risks)
	}
	// The colleague who still works there must NOT appear. A departure list
	// that swept in every seat would put a resignation on a person who is at
	// their desk.
	for _, id := range stakeholder.PersonIDs {
		if id == stayed {
			t.Errorf("a stakeholder who still works at the account was reported as having left")
		}
		if id == gone {
			t.Errorf("the champion was reported twice — once as champion_left and once as stakeholder_left")
		}
	}
}

func TestDealCoverageDoesNotCallARehiredStakeholderDeparted(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	org, deal := e.dealAtAnAccount(t, "Voss Systems", "Expansion")

	// A role change recorded as end-then-start, which is how most CRMs record
	// a promotion. The closed row is real and so is the live one, and only the
	// live one decides: flagging this would announce a resignation every time
	// somebody changed job title.
	person := e.contactAt(t, org, "Promoted Person", "2026-01-31")
	e.Employ(t, person, org, "2026-02-01", "")
	e.stakeholder(t, deal, person, "champion")

	risks := e.coverageRisks(t, deal)
	if findRisk(risks, "champion_left") != nil {
		t.Errorf("a stakeholder with a live employment was reported as departed: %+v", risks)
	}
}

func TestGoingColdFiresOnTheReportingWindowAndCarriesTheDayCount(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	_, deal := e.dealAtAnAccount(t, "Kessler GmbH", "Silent")

	// A brand-new deal has been silent since it was created, which is zero
	// days — not cold. The flag must not fire on the day a rep writes a deal
	// down.
	if findRisk(e.coverageRisks(t, deal), "going_cold") != nil {
		t.Error("a deal created moments ago was flagged as going cold")
	}

	// Age the last touch past REPORT-PARAM-2's window. Written through the
	// owner connection because no API sets this column — it is maintained by
	// the capture path, and the point of the test is the read, not the write.
	e.ageLastTouch(t, deal, 41)

	risks := e.coverageRisks(t, deal)
	cold := findRisk(risks, "going_cold")
	if cold == nil {
		t.Fatalf("a deal untouched for 41 days raised no going_cold risk: %+v", risks)
	}
	if cold.DaysSinceTouch == nil {
		t.Fatal("going_cold carries no day count — the 30/60-day views filter on it")
	}
	if *cold.DaysSinceTouch != 41 {
		t.Errorf("going_cold reports %d days, want 41", *cold.DaysSinceTouch)
	}
	// Every other finding leaves the count ABSENT. A zero would read as
	// "touched today" on a rule that says nothing about recency.
	for _, r := range risks {
		if r.Kind != "going_cold" && r.DaysSinceTouch != nil {
			t.Errorf("%s carries days_since_touch=%d; only going_cold speaks about recency",
				r.Kind, *r.DaysSinceTouch)
		}
	}
}

func TestAWonDealIsNotGoingCold(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	_, deal := e.dealAtAnAccount(t, "Brandt Automotive", "Delivered")
	e.ageLastTouch(t, deal, 400)
	e.closeWon(t, deal)

	// Silence after the close is delivery, not decay. Flagging it would train
	// a rep to ignore the flag on the deals where it means something.
	if findRisk(e.coverageRisks(t, deal), "going_cold") != nil {
		t.Error("a won deal silent for 400 days was flagged as going cold")
	}
}

// --- fixture helpers ---

// coverageRisks reads one deal's findings through the real endpoint.
func (e *env) coverageRisks(t *testing.T, dealID string) []coverageRiskDTO {
	t.Helper()
	var got dealCoverageDTO
	if status := e.call(t, "GET", "/v1/deals/"+dealID+"/coverage", nil, nil, &got); status != http.StatusOK {
		t.Fatalf("coverage status = %d, want 200", status)
	}
	return got.Risks
}

func findRisk(risks []coverageRiskDTO, kind string) *coverageRiskDTO {
	for i := range risks {
		if risks[i].Kind == kind {
			return &risks[i]
		}
	}
	return nil
}

// dealAtAnAccount creates an organization and an open deal owned by it — the
// shape every departure rule needs, since a deal with no account has nobody to
// have left.
func (e *env) dealAtAnAccount(t *testing.T, orgName, dealName string) (org, deal string) {
	t.Helper()
	var created anyMap
	if status := e.call(t, "POST", "/v1/organizations",
		anyMap{"display_name": orgName, "source": "ui"}, nil, &created); status != http.StatusCreated {
		t.Fatalf("creating the account: %d", status)
	}
	org, _ = created["id"].(string)

	var pipelines anyMap
	if status := e.call(t, "GET", "/v1/pipelines", nil, nil, &pipelines); status != http.StatusOK {
		t.Fatalf("listing pipelines: %d", status)
	}
	pipeline, stage := firstPipelineAndStage(t, pipelines)

	var made anyMap
	if status := e.call(t, "POST", "/v1/deals", anyMap{
		"name": dealName, "pipeline_id": pipeline, "stage_id": stage,
		"organization_id": org, "source": "ui",
	}, nil, &made); status != http.StatusCreated {
		t.Fatalf("creating the deal: %d", status)
	}
	deal, _ = made["id"].(string)
	return org, deal
}

// hiredOn is when every fixture employee started. The start date is not what
// any of these rules read — only whether an employment has ENDED — so one
// shared value keeps the fixtures about the thing under test.
const hiredOn = "2020-01-01"

// contactAt creates a person and their employment at the account. An empty
// endedAt means they still work there.
func (e *env) contactAt(t *testing.T, org, name, endedAt string) string {
	t.Helper()
	var person anyMap
	if status := e.call(t, "POST", "/v1/people",
		anyMap{"full_name": name}, nil, &person); status != http.StatusCreated {
		t.Fatalf("creating %s: %d", name, status)
	}
	id, _ := person["id"].(string)
	e.Employ(t, id, org, hiredOn, endedAt)
	return id
}

func (e *env) Employ(t *testing.T, person, org, startedAt, endedAt string) {
	t.Helper()
	body := anyMap{
		"kind": "employment", "person_id": person, "organization_id": org,
		"started_at": startedAt, "source": "ui",
	}
	if endedAt != "" {
		body["ended_at"] = endedAt
	}
	var out anyMap
	if status := e.call(t, "POST", "/v1/relationships", body, nil, &out); status != http.StatusCreated {
		t.Fatalf("recording employment: %d (%v)", status, out)
	}
}

func (e *env) stakeholder(t *testing.T, deal, person, role string) {
	t.Helper()
	var out anyMap
	if status := e.call(t, "POST", "/v1/relationships", anyMap{
		"kind": "deal_stakeholder", "deal_id": deal, "person_id": person,
		"role": role, "source": "ui",
	}, nil, &out); status != http.StatusCreated {
		t.Fatalf("seating the %s stakeholder: %d (%v)", role, status, out)
	}
}

// ageLastTouch backdates the deal's last captured touch. Written as owner
// because no API writes this column — capture maintains it — and the behaviour
// under test is how the coverage read interprets it.
func (e *env) ageLastTouch(t *testing.T, deal string, days int) {
	t.Helper()
	if _, err := e.owner.Exec(t.Context(),
		`UPDATE deal SET last_activity_at = now() - make_interval(days => $2) WHERE id = $1`,
		deal, days); err != nil {
		t.Fatalf("backdating the deal's last touch: %v", err)
	}
}

func (e *env) closeWon(t *testing.T, deal string) {
	t.Helper()
	if _, err := e.owner.Exec(t.Context(),
		`UPDATE deal SET status = 'won', closed_at = now() WHERE id = $1`, deal); err != nil {
		t.Fatalf("closing the deal as won: %v", err)
	}
}

// firstPipelineAndStage picks the default pipeline and its first stage.
func firstPipelineAndStage(t *testing.T, listed anyMap) (pipeline, stage string) {
	t.Helper()
	data, _ := listed["data"].([]any)
	if len(data) == 0 {
		t.Fatal("the workspace seed produced no pipeline")
	}
	first, _ := data[0].(map[string]any)
	pipeline, _ = first["id"].(string)
	stages, _ := first["stages"].([]any)
	if len(stages) == 0 {
		t.Fatal("the default pipeline has no stages")
	}
	firstStage, _ := stages[0].(map[string]any)
	stage, _ = firstStage["id"].(string)
	return pipeline, stage
}
