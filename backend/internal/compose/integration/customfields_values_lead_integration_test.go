// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The lead half of the custom-field VALUES coverage: the fieldcatalog seam
// wired into the segregated lead store — active cf_* columns ride create /
// update writes and get / list / replay / disqualify reads like core fields,
// same drop-on-mismatch and workspace-isolation posture as the
// person/organization/deal suites. Reuses the shared people cfvFixture
// (setupCFV) since leads live in the people store.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/customfields"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestCustomFieldValues_LeadRoundTrip(t *testing.T) {
	f := setupCFV(t)
	col := f.defineField(t, customfields.FieldSpec{Object: "lead", Label: "Is Cool", Type: customfields.TypeBoolean, Source: "ui"})

	created, _, err := f.store.CreateLead(f.ctx, people.CreateLeadInput{
		FullName: strp("Grace Hopper"), Source: "ui",
		CustomFields: map[string]any{col: true},
	})
	if err != nil {
		t.Fatalf("CreateLead: %v", err)
	}
	assertCF(t, created.AdditionalProperties, col, true)

	got, err := f.store.GetLead(f.ctx, leadIDOf(ids.UUID(created.Id)), storekit.LiveOnly)
	if err != nil {
		t.Fatalf("GetLead: %v", err)
	}
	assertCF(t, got.AdditionalProperties, col, true)

	updated, err := f.store.UpdateLead(f.ctx, leadIDOf(ids.UUID(created.Id)), people.UpdateLeadInput{
		CustomFields: map[string]any{col: false},
	})
	if err != nil {
		t.Fatalf("UpdateLead: %v", err)
	}
	assertCF(t, updated.AdditionalProperties, col, false)

	list, _, err := f.store.ListLeads(f.ctx, people.ListLeadsInput{})
	if err != nil {
		t.Fatalf("ListLeads: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListLeads returned %d rows, want 1", len(list))
	}
	assertCF(t, list[0].AdditionalProperties, col, false)
}

// Source-key replay returns the existing row carrying its custom fields, and
// the replay's own cf values are ignored (the original write is authoritative).
func TestCustomFieldValues_LeadSourceReplayCarriesCustomFields(t *testing.T) {
	f := setupCFV(t)
	col := f.defineField(t, customfields.FieldSpec{Object: "lead", Label: "Tier", Type: customfields.TypeText, Source: "ui"})
	system, id := "crm", "ext-42"

	created, wasCreated, err := f.store.CreateLead(f.ctx, people.CreateLeadInput{
		FullName: strp("Ada"), Source: "import", SourceSystem: &system, SourceID: &id,
		CustomFields: map[string]any{col: "gold"},
	})
	if err != nil || !wasCreated {
		t.Fatalf("CreateLead: err=%v created=%v", err, wasCreated)
	}
	assertCF(t, created.AdditionalProperties, col, "gold")

	replay, wasCreated, err := f.store.CreateLead(f.ctx, people.CreateLeadInput{
		FullName: strp("Ada"), Source: "import", SourceSystem: &system, SourceID: &id,
		CustomFields: map[string]any{col: "silver"},
	})
	if err != nil {
		t.Fatalf("CreateLead replay: %v", err)
	}
	if wasCreated {
		t.Fatalf("same source key must replay the existing lead, not create a new one")
	}
	// The original value is authoritative — replay is a read, not a write.
	assertCF(t, replay.AdditionalProperties, col, "gold")
}

// Disqualify (the DELETE path) returns the archived lead with its custom
// fields intact — a retire is a status flip, never a value drop.
func TestCustomFieldValues_LeadDisqualifyPreservesCustomFields(t *testing.T) {
	f := setupCFV(t)
	col := f.defineField(t, customfields.FieldSpec{Object: "lead", Label: "Is Cool", Type: customfields.TypeBoolean, Source: "ui"})

	created, _, err := f.store.CreateLead(f.ctx, people.CreateLeadInput{
		FullName: strp("Otto"), Source: "ui",
		CustomFields: map[string]any{col: true},
	})
	if err != nil {
		t.Fatalf("CreateLead: %v", err)
	}

	disqualified, err := f.store.DisqualifyLead(f.ctx, leadIDOf(ids.UUID(created.Id)))
	if err != nil {
		t.Fatalf("DisqualifyLead: %v", err)
	}
	if disqualified.Status != "disqualified" {
		t.Fatalf("status = %q, want disqualified", disqualified.Status)
	}
	assertCF(t, disqualified.AdditionalProperties, col, true)
}
