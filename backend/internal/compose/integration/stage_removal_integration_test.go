// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// E2E-UC-ADMIN-04.remove-stage-guard (step 6, step 7, F1b): an empty
// stage is removed and the survivors stay contiguous; a stage still
// holding deals refuses and names them; the terminal pair is not
// removable at all; and once the deals move, the stage goes.

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
)

// configuredStage is one stage as the stage list answers it — a removal
// is about position, which the shared DiscoverSeededPipeline fixture
// deliberately does not carry.
type configuredStage struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Position int    `json:"position"`
}

func TestStageRemovalRefusesWhileDealsSitOnIt(t *testing.T) {
	e := apptest.SetupApp(t)
	e.Slug = "stage-rm"
	apptest.BootstrapWorkspaceSession(t, e, "Stage RM", "stage-rm@fable.test", "Admin")
	seeded := apptest.DiscoverSeededPipeline(t, e)
	dealID := apptest.CreateOpenDeal(t, e, seeded)

	stages := readStages(t, e, seeded.PipelineID)
	occupied := stages[0] // CreateOpenDeal lands the deal on the first open stage.
	empty := stages[2]
	if occupied.ID != seeded.Open {
		t.Fatalf("the deal sits on %q, not the first open stage this test removes from", occupied.Name)
	}

	// Step 7: the terminal pair is out of the add/remove surface.
	var refusal apptest.AnyMap
	if status := e.Call(t, "DELETE", "/v1/stages/"+seeded.Won, nil, nil, &refusal); status != http.StatusUnprocessableEntity ||
		refusal["code"] != "terminal_stage_not_removable" {
		t.Fatalf("removing the won stage → %d %v, want 422 terminal_stage_not_removable", status, refusal)
	}

	// F1b: the occupied stage refuses AND names what is in the way.
	assertOccupiedRefusal(t, e, occupied.ID, "Acme rollout")

	// Step 6: the empty stage goes, and the survivors close the gap.
	if status := e.Call(t, "DELETE", "/v1/stages/"+empty.ID, nil, nil, nil); status != http.StatusNoContent {
		t.Fatalf("removing the empty %q stage → %d, want 204", empty.Name, status)
	}
	assertContiguous(t, e, seeded.PipelineID, len(stages)-1)
	assertRemovalEvents(t, e, empty.ID, seeded.PipelineID)

	// The archived stage still reads, which is what keeps a historic
	// stage change renderable: its id resolves to a named row.
	if status := e.Call(t, "GET", "/v1/stages/"+empty.ID, nil, nil, nil); status != http.StatusOK {
		t.Fatalf("reading the removed stage → %d, want 200 — the stage-change history references it", status)
	}
	// And it is gone from the pipeline: a second removal has nothing live to remove.
	if status := e.Call(t, "DELETE", "/v1/stages/"+empty.ID, nil, nil, nil); status != http.StatusNotFound {
		t.Fatalf("removing an already-removed stage → %d, want 404", status)
	}

	// Once the deal moves, the guard lifts.
	if status := e.Call(t, "POST", "/v1/deals/"+dealID+"/advance",
		apptest.AnyMap{"to_stage_id": stages[1].ID}, nil, nil); status != http.StatusOK {
		t.Fatalf("advancing the deal off the occupied stage → %d, want 200", status)
	}
	if status := e.Call(t, "DELETE", "/v1/stages/"+occupied.ID, nil, nil, nil); status != http.StatusNoContent {
		t.Fatalf("removing the vacated stage → %d, want 204", status)
	}
	assertContiguous(t, e, seeded.PipelineID, len(stages)-2)

	// The history the archive existed to protect: the advance's own row
	// still points at the stage the deal left, which has since been removed.
	var fromRemoved int
	if err := e.Owner.QueryRow(t.Context(),
		`SELECT count(*) FROM deal_stage_history WHERE deal_id = $1 AND from_stage_id = $2`,
		dealID, occupied.ID).Scan(&fromRemoved); err != nil {
		t.Fatal(err)
	}
	if fromRemoved != 1 {
		t.Fatalf("%d history rows out of the removed stage, want 1 — archiving is what keeps them readable", fromRemoved)
	}
}

func TestStageRemovalTakesTheVersionGuard(t *testing.T) {
	e := apptest.SetupApp(t)
	e.Slug = "stage-rm-ver"
	apptest.BootstrapWorkspaceSession(t, e, "Stage RM Ver", "stage-rm-ver@fable.test", "Admin")
	seeded := apptest.DiscoverSeededPipeline(t, e)
	stages := readStages(t, e, seeded.PipelineID)

	var problem apptest.AnyMap
	if status := e.Call(t, "DELETE", "/v1/stages/"+stages[0].ID, nil,
		map[string]string{"If-Match": "999"}, &problem); status != http.StatusConflict ||
		problem["code"] != "version_skew" {
		t.Fatalf("removing a stage on a stale If-Match → %d %v, want 409 version_skew", status, problem)
	}
	// The refused removal left the stage where it was.
	if status := e.Call(t, "GET", "/v1/stages/"+stages[0].ID, nil, nil, nil); status != http.StatusOK {
		t.Fatalf("the stage after a refused removal → %d, want 200", status)
	}
	assertContiguous(t, e, seeded.PipelineID, len(stages))
}

// Contiguity is a postcondition of the removal, not an invariant the
// schema holds — createStage and updateStage both take the position they
// are handed — so a pipeline that was already gapped must come out of a
// removal renumbered, and a removal that moves nothing must not publish a
// reorder that did not happen.
func TestStageRemovalRenumbersWhateverLayoutItFinds(t *testing.T) {
	e := apptest.SetupApp(t)
	e.Slug = "stage-rm-gap"
	apptest.BootstrapWorkspaceSession(t, e, "Stage RM Gap", "stage-rm-gap@fable.test", "Admin")

	var pipeline struct {
		ID     string `json:"id"`
		Stages []struct {
			ID string `json:"id"`
		} `json:"stages"`
	}
	if status := e.Call(t, "POST", "/v1/pipelines", apptest.AnyMap{
		"name": "Gapped", "stages": []apptest.AnyMap{
			{"name": "Scout", "position": 2},
			{"name": "Pitch", "position": 5},
			{"name": "Close", "position": 9},
		},
	}, nil, &pipeline); status != http.StatusCreated {
		t.Fatalf("create a gapped pipeline → %d", status)
	}
	gapped := readStages(t, e, pipeline.ID)
	if gapped[0].Position != 2 || gapped[2].Position != 9 {
		t.Fatalf("the fixture is not gapped: %+v", gapped)
	}

	// Removing the LAST stage moves nothing above it — but the gaps below
	// are still the removal's to close.
	if status := e.Call(t, "DELETE", "/v1/stages/"+gapped[2].ID, nil, nil, nil); status != http.StatusNoContent {
		t.Fatalf("removing the last stage → %d, want 204", status)
	}
	assertContiguous(t, e, pipeline.ID, 2)

	// Now the list is 1..n already, so removing its last stage moves
	// nothing at all — and publishes no reorder.
	survivors := readStages(t, e, pipeline.ID)
	if status := e.Call(t, "DELETE", "/v1/stages/"+survivors[1].ID, nil, nil, nil); status != http.StatusNoContent {
		t.Fatalf("removing the trailing stage → %d, want 204", status)
	}
	assertContiguous(t, e, pipeline.ID, 1)

	var reorders int
	if err := e.Owner.QueryRow(t.Context(),
		`SELECT count(*) FROM event_outbox
		 WHERE envelope->>'type' = 'pipeline.updated'
		   AND envelope->'entity'->>'id' = $1::text
		   AND envelope->'payload'->'changed_fields'->'stage_positions' IS NOT NULL`,
		pipeline.ID).Scan(&reorders); err != nil {
		t.Fatal(err)
	}
	if reorders != 1 {
		t.Fatalf("%d reorder events for this pipeline, want 1 — only the removal that renumbered publishes one", reorders)
	}
}

// assertOccupiedRefusal checks the F1b guard: a 422 whose machine code a
// surface can branch on, and whose sentence names the deal standing in
// the way so the admin knows what to move.
func assertOccupiedRefusal(t *testing.T, e *apptest.AppEnv, stageID, dealName string) {
	t.Helper()
	var refusal struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	status := e.Call(t, "DELETE", "/v1/stages/"+stageID, nil, nil, &refusal)
	if status != http.StatusUnprocessableEntity || refusal.Code != "stage_occupied" {
		t.Fatalf("removing an occupied stage → %d %q, want 422 stage_occupied", status, refusal.Code)
	}
	if !strings.Contains(refusal.Detail, dealName) {
		t.Fatalf("the refusal reads %q and never names the deal in the way", refusal.Detail)
	}
	// The stage and its deal are untouched by the refusal.
	var stage struct {
		ArchivedAt *string `json:"archived_at"`
	}
	if status := e.Call(t, "GET", "/v1/stages/"+stageID, nil, nil, &stage); status != http.StatusOK || stage.ArchivedAt != nil {
		t.Fatalf("the refused stage reads %d archived_at=%v, want 200 and live", status, stage.ArchivedAt)
	}
}

// assertContiguous holds UC-ADMIN-04's ordering invariant: the live
// stages of the pipeline are exactly positions 1..n after a removal.
func assertContiguous(t *testing.T, e *apptest.AppEnv, pipelineID string, want int) {
	t.Helper()
	stages := readStages(t, e, pipelineID)
	if len(stages) != want {
		t.Fatalf("%d live stages, want %d", len(stages), want)
	}
	for i, s := range stages {
		if s.Position != i+1 {
			t.Fatalf("stage %q sits at position %d, want %d — a removal must leave 1..n contiguous",
				s.Name, s.Position, i+1)
		}
	}
}

// assertRemovalEvents holds the events.md §5.3b split: the stage's own
// archival is a stage fact carrying its pipeline, and the shift that
// closed the gap is ONE pipeline.updated, never N stage.updated.
func assertRemovalEvents(t *testing.T, e *apptest.AppEnv, stageID, pipelineID string) {
	t.Helper()
	var archived, reorders int
	if err := e.Owner.QueryRow(t.Context(),
		`SELECT count(*) FILTER (WHERE envelope->>'type' = 'stage.archived'
		                           AND envelope->'entity'->>'id' = $1::text
		                           AND envelope->'payload'->>'pipeline_id' = $2::text),
		        count(*) FILTER (WHERE envelope->>'type' = 'pipeline.updated'
		                           AND envelope->'payload'->'changed_fields'->'stage_positions' IS NOT NULL)
		 FROM event_outbox`, stageID, pipelineID).Scan(&archived, &reorders); err != nil {
		t.Fatal(err)
	}
	if archived != 1 {
		t.Fatalf("%d stage.archived events naming the pipeline, want 1", archived)
	}
	if reorders != 1 {
		t.Fatalf("%d pipeline.updated reorder events, want exactly 1 — a reorder is one pipeline fact", reorders)
	}
}

// readStages answers the pipeline's live stages ordered by position.
func readStages(t *testing.T, e *apptest.AppEnv, pipelineID string) []configuredStage {
	t.Helper()
	var listed struct {
		Data []configuredStage `json:"data"`
	}
	if status := e.Call(t, "GET", "/v1/stages?pipeline_id="+pipelineID, nil, nil, &listed); status != http.StatusOK {
		t.Fatalf("listing stages → %d", status)
	}
	// The list answers live rows only unless include_archived asks
	// otherwise, so a removed stage is already out of this slice.
	sort.Slice(listed.Data, func(i, j int) bool { return listed.Data[i].Position < listed.Data[j].Position })
	return listed.Data
}
