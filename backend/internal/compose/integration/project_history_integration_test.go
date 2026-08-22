// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// A project's history is readable like every other record's: the phase move
// the deals store writes (action advance_phase, with the phase before/after
// images) comes back from both history views — the whole-mutation record
// history and the per-field diff — naming who moved it and when.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestProjectHistoryListsThePhaseTransitionAndWhoMadeIt(t *testing.T) {
	e := Setup(t)
	org := e.SeedOrg(t, "BAER Pharma", nil)
	p := seedProject(e.Admin(), t, e, "ERP replacement", strPtr("ERP-27"), org, &e.Rep1)

	mover := e.As(e.Rep1, []ids.UUID{e.Team1}, principal.Permissions{
		RoleKeys: []string{"rep"},
		Objects:  map[string]principal.ObjectGrant{"project": {Read: true, Update: true}},
		RowScope: principal.RowScopeOwn,
	})
	if _, err := e.Deals.AdvanceProjectPhase(mover, p.ID, deals.AdvanceProjectPhaseInput{ToPhase: deals.PhasePursuing}); err != nil {
		t.Fatalf("advance project: %v", err)
	}

	// The record history carries the creation row and the move. Removing
	// "project" from privacy's fieldHistoryEntityTypes makes this read
	// answer not-found.
	record, err := privacy.ListRecordHistory(e.Admin(), e.DB(), privacy.RecordHistoryFilter{
		EntityType: "project", EntityID: p.ID.UUID,
	})
	if err != nil {
		t.Fatalf("record history of a project: %v", err)
	}
	if len(record.Entries) != 2 {
		t.Fatalf("record history has %d entries, want 2 (create, advance_phase): %+v", len(record.Entries), record.Entries)
	}
	var move *privacy.RecordHistoryEntry
	for i := range record.Entries {
		if record.Entries[i].Action == "advance_phase" {
			move = &record.Entries[i]
		}
	}
	if move == nil {
		t.Fatalf("no advance_phase line in %+v", record.Entries)
	}
	if move.ActorID != "human:"+e.Rep1.String() {
		t.Errorf("the move is attributed to %q, want the rep who made it (human:%s)", move.ActorID, e.Rep1)
	}
	if move.Before["phase"] != deals.PhaseInitiative || move.After["phase"] != deals.PhasePursuing {
		t.Errorf("phase images = %v → %v, want initiative → pursuing", move.Before["phase"], move.After["phase"])
	}
	if move.OccurredAt.IsZero() {
		t.Error("the move carries no occurred_at")
	}

	// The per-field diff: advance_phase is a projected verb, so the phase
	// change reads as one field entry. Removing "advance_phase" from
	// privacy's fieldHistoryProjectedActions leaves this page empty.
	fields, err := privacy.ListFieldHistory(e.Admin(), e.DB(), privacy.FieldHistoryFilter{
		EntityType: "project", EntityID: p.ID.UUID, Field: strPtr("phase"),
	})
	if err != nil {
		t.Fatalf("field history of a project: %v", err)
	}
	if len(fields.Entries) != 1 {
		t.Fatalf("phase field history has %d entries, want exactly the one move: %+v", len(fields.Entries), fields.Entries)
	}
	if got := fields.Entries[0]; got.OldValue == nil || got.NewValue == nil ||
		*got.OldValue != deals.PhaseInitiative || *got.NewValue != deals.PhasePursuing {
		t.Errorf("phase diff = %v → %v, want initiative → pursuing", got.OldValue, got.NewValue)
	}
}
