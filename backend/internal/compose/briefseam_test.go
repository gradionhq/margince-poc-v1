// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/briefs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The run reaches the tool whole.
//
// Every field this mapping drops is one an agent cannot ask for a second time:
// the queue state is what keeps it from re-raising an item a person already
// dismissed, and the evidence ids are what let it cite the ranking rather than
// restate it. A mapping that silently narrowed either would still serve a
// well-formed brief.
func TestTheBriefRunReachesTheToolWhole(t *testing.T) {
	snoozed := time.Date(2026, 8, 9, 7, 0, 0, 0, time.UTC)
	evidence := []ids.UUID{ids.NewV7(), ids.NewV7()}
	run := briefs.BriefRun{
		ID:             ids.NewV7(),
		GeneratedAt:    time.Date(2026, 8, 8, 6, 0, 0, 0, time.UTC),
		AsOf:           time.Date(2026, 8, 8, 5, 0, 0, 0, time.UTC),
		CandidateCount: 7,
		Items: []briefs.BriefRunItem{{
			ID: ids.NewV7(), DealID: ids.NewV7(), Rank: 1, Composite: 0.87,
			EvidenceIDs: evidence, State: "snoozed", SnoozedUntil: &snoozed,
		}},
	}

	served := briefRunToTool(run)

	if served.BriefID != run.ID || served.AsOf != run.AsOf || served.CandidateCount != 7 {
		t.Errorf("the run's own identity did not survive: %+v", served)
	}
	if len(served.Items) != 1 {
		t.Fatalf("got %d items, want 1", len(served.Items))
	}
	item := served.Items[0]
	if item.ItemID != run.Items[0].ID || item.DealID != run.Items[0].DealID || item.Rank != 1 {
		t.Errorf("item identity did not survive: %+v", item)
	}
	if item.State != "snoozed" || item.SnoozedUntil == nil || !item.SnoozedUntil.Equal(snoozed) {
		t.Errorf("the human's own queue state did not survive: %+v — an agent would re-raise what "+
			"a person has already put away", item)
	}
	if len(item.EvidenceIDs) != len(evidence) || item.EvidenceIDs[0] != evidence[0] {
		t.Errorf("the evidence behind the ranking did not survive: %+v", item.EvidenceIDs)
	}
}

// An item with no evidence still carries an empty list rather than null. The
// two are different facts on the wire, and an agent reading `null` has to guess
// which one it is looking at.
func TestABriefItemCarriesAnEmptyEvidenceListRatherThanNull(t *testing.T) {
	served := briefRunToTool(briefs.BriefRun{
		Items: []briefs.BriefRunItem{{ID: ids.NewV7(), DealID: ids.NewV7(), Rank: 1}},
	})

	raw, err := json.Marshal(served)
	if err != nil {
		t.Fatalf("marshalling the served run: %v", err)
	}
	if !strings.Contains(string(raw), `"evidence_ids":[]`) {
		t.Errorf("an item with no evidence serves null:\n%s", raw)
	}
}
