// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The lead list orders by the sort it was asked for, and pages under it.
// The list used to order every page by creation time whatever the request
// said, which made the score column a control that changed nothing: this
// walks a scored set highest-first, one row per page, so an ORDER BY that
// stopped reading the request would fail here rather than in a reader's
// hands.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/people"
)

// leadName reads the row's display name, which the contract carries as an
// optional string — a lead the list returned always has one.
func listedLeadName(name *string) string {
	if name == nil {
		return ""
	}
	return *name
}

func TestLeadListSortsByScoreAndPagesUnderIt(t *testing.T) {
	e := SetupSearch(t)
	ctx := e.AsFullUser()

	// Seeded out of score order, so a list that ignored the sort would
	// return them in the creation order below and fail the first check.
	e.Seed(t, `INSERT INTO lead (id, workspace_id, full_name, status, source, score, captured_by)
	           VALUES ($1, $2, 'Middle', 'working', 'inbound', 50, 'human:x')`)
	e.Seed(t, `INSERT INTO lead (id, workspace_id, full_name, status, source, score, captured_by)
	           VALUES ($1, $2, 'Warmest', 'working', 'inbound', 90, 'human:x')`)
	e.Seed(t, `INSERT INTO lead (id, workspace_id, full_name, status, source, score, captured_by)
	           VALUES ($1, $2, 'Coldest', 'working', 'inbound', 10, 'human:x')`)

	store := people.NewStore(e.Pool)
	sortField := "-score"
	one := 1

	page1, info1, err := store.ListLeads(ctx, people.ListLeadsInput{Sort: &sortField, Limit: &one})
	if err != nil {
		t.Fatalf("ListLeads sort=-score page 1: %v", err)
	}
	if len(page1) != 1 || listedLeadName(page1[0].FullName) != "Warmest" || !info1.HasMore {
		t.Fatalf("page 1 = %+v (more=%v), want [Warmest] with more", page1, info1.HasMore)
	}

	page2, info2, err := store.ListLeads(ctx, people.ListLeadsInput{
		Sort: &sortField, Limit: &one, Cursor: &info1.NextCursor,
	})
	if err != nil {
		t.Fatalf("ListLeads sort=-score page 2: %v", err)
	}
	if len(page2) != 1 || listedLeadName(page2[0].FullName) != "Middle" || !info2.HasMore {
		t.Fatalf("page 2 = %+v (more=%v), want [Middle] with more", page2, info2.HasMore)
	}

	page3, info3, err := store.ListLeads(ctx, people.ListLeadsInput{
		Sort: &sortField, Limit: &one, Cursor: &info2.NextCursor,
	})
	if err != nil {
		t.Fatalf("ListLeads sort=-score page 3: %v", err)
	}
	if len(page3) != 1 || listedLeadName(page3[0].FullName) != "Coldest" || info3.HasMore {
		t.Fatalf("page 3 = %+v (more=%v), want [Coldest] with no more", page3, info3.HasMore)
	}
}

// A sort field outside the lead vocabulary is refused rather than guessed,
// so a client cannot order by a column the list does not publish.
func TestLeadListRefusesAnUnknownSortField(t *testing.T) {
	e := SetupSearch(t)
	ctx := e.AsFullUser()

	store := people.NewStore(e.Pool)
	sortField := "not_a_column"
	if _, _, err := store.ListLeads(ctx, people.ListLeadsInput{Sort: &sortField}); err == nil {
		t.Fatal("ListLeads sort=not_a_column: want a refusal, got none")
	}
}
