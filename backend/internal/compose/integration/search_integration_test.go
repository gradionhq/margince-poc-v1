// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Ranked cross-object search (B-EP05.15): relevance over the generated
// search_tsv columns, row-scope enforced per branch (a hit IS a read),
// archived rows invisible, stable ranked-keyset pagination, and the
// PERF-3 posture proven structurally — the plan must ride the GIN
// index, not a sequential scan.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// A role with NO person grant gets no person hits — search must not
// out-see the entity lists (object RBAC before row scope).
func TestSearchHonorsObjectRBAC(t *testing.T) {
	e := SetupSearch(t)
	e.Seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Rostock Person', 'manual', 'human:x')`)
	e.Seed(t, `INSERT INTO organization (id, workspace_id, display_name, source, captured_by) VALUES ($1, $2, 'Rostock Werft', 'manual', 'human:x')`)

	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	orgOnly := principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + e.Rep1.String(), UserID: e.Rep1,
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{"organization": {Read: true}},
			RowScope: principal.RowScopeAll,
		},
	})
	page, err := e.Store.Search(orgOnly, search.Input{Query: "rostock"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Hits) != 1 || page.Hits[0].Type != "organization" {
		t.Fatalf("object RBAC leaked into search: %+v", page.Hits)
	}
	// Explicitly requesting only the denied type answers an empty page,
	// not an error — nothing to disclose.
	page, err = e.Store.Search(orgOnly, search.Input{Query: "rostock", Types: []string{"person"}})
	if err != nil || len(page.Hits) != 0 {
		t.Fatalf("denied-type search → %v %+v, want an empty page", err, page.Hits)
	}
}

func TestSearchRanksAcrossObjectTypes(t *testing.T) {
	e := SetupSearch(t)
	e.Seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Heike Hamburg', 'manual', 'human:x')`)
	e.Seed(t, `INSERT INTO organization (id, workspace_id, display_name, source, captured_by) VALUES ($1, $2, 'Hamburg Logistics GmbH', 'manual', 'human:x')`)
	e.Seed(t, `INSERT INTO lead (id, workspace_id, company_name, email, source, captured_by) VALUES ($1, $2, 'Hamburg Freight', 'lead@hamburg.test', 'manual', 'human:x')`)
	e.Seed(t, `INSERT INTO activity (id, workspace_id, kind, subject, body, source, captured_by) VALUES ($1, $2, 'note', 'Hamburg visit', 'Met the Hamburg team at the Hamburg office in Hamburg', 'manual', 'human:x')`)
	e.Seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Unrelated Munich', 'manual', 'human:x')`)

	page, err := e.Store.Search(e.Admin(), search.Input{Query: "hamburg"})
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]bool{}
	for _, hit := range page.Hits {
		types[hit.Type] = true
		if hit.Score <= 0 {
			t.Fatalf("hit without a rank: %+v", hit)
		}
		if strings.Contains(hit.Title, "Munich") {
			t.Fatalf("non-matching row surfaced: %+v", hit)
		}
	}
	for _, want := range []string{"person", "organization", "lead", "activity"} {
		if !types[want] {
			t.Errorf("no %s hit in %+v", want, page.Hits)
		}
	}
	// The activity mentions the term four times — repetition ranks it
	// above single-mention rows.
	if page.Hits[0].Type != "activity" {
		t.Errorf("rank order ignores term frequency: top hit %+v", page.Hits[0])
	}
}

func TestSearchHitsCarryTheCallersRowScope(t *testing.T) {
	e := SetupSearch(t)
	e.Seed(t, `INSERT INTO person (id, workspace_id, full_name, owner_id, source, captured_by) VALUES ($1, $2, 'Scoped Bremen', $3, 'manual', 'human:x')`, e.Rep3)
	e.Seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Shared Bremen', 'manual', 'human:x')`)

	// rep1 (team1, row_scope=team) must not see rep3's (team2) record —
	// but the ownerless row is workspace-shared.
	page, err := e.Store.Search(e.AsTeamRep(e.Rep1, e.Team1), search.Input{Query: "bremen"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Hits) != 1 || page.Hits[0].Title != "Shared Bremen" {
		t.Fatalf("row scope leaked into search: %+v", page.Hits)
	}
	// row_scope=all sees both.
	page, err = e.Store.Search(e.Admin(), search.Input{Query: "bremen"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Hits) != 2 {
		t.Fatalf("unbounded scope sees %d, want 2", len(page.Hits))
	}
}

func TestSearchExcludesArchivedRows(t *testing.T) {
	e := SetupSearch(t)
	e.Seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by, archived_at) VALUES ($1, $2, 'Archived Kiel', 'manual', 'human:x', now())`)
	page, err := e.Store.Search(e.Admin(), search.Input{Query: "kiel"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Hits) != 0 {
		t.Fatalf("archived row surfaced: %+v", page.Hits)
	}
}

// Search is how people find ACCOUNTS, and the company running the CRM is not
// one to find (ADR-0082/A127). It stays an ordinary row, readable by id — what
// narrows is discovery.
func TestSearchExcludesTheOwnCompany(t *testing.T) {
	e := SetupSearch(t)
	e.Seed(t, `INSERT INTO organization (id, workspace_id, display_name, is_anchor, source, captured_by) VALUES ($1, $2, 'Rostock Consulting GmbH', true, 'manual', 'human:x')`)
	customer := e.Seed(t, `INSERT INTO organization (id, workspace_id, display_name, source, captured_by) VALUES ($1, $2, 'Rostock Freight AG', 'manual', 'human:x')`)

	page, err := e.Store.Search(e.Admin(), search.Input{Query: "rostock"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Hits) != 1 || page.Hits[0].ID != customer {
		t.Fatalf("search returned %+v, want only the customer %s — the installation's own company is not an account to find", page.Hits, customer)
	}
}

func TestSearchRankedCursorWalksAllHitsOnce(t *testing.T) {
	e := SetupSearch(t)
	want := map[string]bool{}
	for i := 0; i < 5; i++ {
		id := e.Seed(t, fmt.Sprintf(`INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Dresden Contact %d', 'manual', 'human:x')`, i))
		want[id.String()] = false
	}
	got := 0
	cursor := ""
	for pages := 0; pages < 5; pages++ {
		page, err := e.Store.Search(e.Admin(), search.Input{Query: "dresden", Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, hit := range page.Hits {
			if seen, ok := want[hit.ID.String()]; !ok || seen {
				t.Fatalf("hit %s unknown or repeated", hit.ID)
			}
			want[hit.ID.String()] = true
			got++
		}
		if !page.HasMore {
			break
		}
		cursor = page.NextCursor
	}
	if got != 5 {
		t.Fatalf("cursor walk yielded %d of 5 hits", got)
	}
}

func TestSearchEmptyQueryIsAValidationError(t *testing.T) {
	e := SetupSearch(t)
	_, err := e.Store.Search(e.Admin(), search.Input{Query: "   "})
	var bad *search.BadQueryError
	if err == nil || !errors.As(err, &bad) || !strings.Contains(bad.Reason, "required") {
		t.Fatalf("empty query → %v, want BadQueryError", err)
	}
}

// The PERF-3 posture, proven structurally instead of by a flaky
// wall-clock or planner assertion: every table the search union reads
// must define a GIN index over search_tsv, so the FTS predicate CAN
// scale past a sequential scan. (Which plan the optimizer picks at a
// given cardinality is its business; the index existing is ours.)
func TestSearchEveryBranchHasAGinIndex(t *testing.T) {
	e := SetupSearch(t)
	for _, table := range []string{"person", "organization", "deal", "lead", "activity"} {
		var exists bool
		err := e.Owner.QueryRow(context.Background(), `
			SELECT EXISTS (
			  SELECT 1 FROM pg_index i
			  JOIN pg_class idx ON idx.oid = i.indexrelid
			  JOIN pg_class tbl ON tbl.oid = i.indrelid
			  JOIN pg_am am ON am.oid = idx.relam
			  JOIN pg_attribute a ON a.attrelid = tbl.oid AND a.attnum = ANY(i.indkey)
			  WHERE tbl.relname = $1 AND am.amname = 'gin' AND a.attname = 'search_tsv')`,
			table).Scan(&exists)
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Errorf("table %s is searched but has no GIN index over search_tsv", table)
		}
	}
}
