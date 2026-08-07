// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The account document library (DOC-WIRE-1, documents-and-files).
//
// The claim under test is the one that cannot be checked by reading the SQL: a
// file whose own parent the caller cannot see contributes neither a row NOR a
// count. Filtering after the fact would leave the count right and the list
// short, which tells the viewer exactly what the gate exists to hide — that
// something is there.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// seedDocument files one attachment against a parent and rolls it up to an
// account. The roll-up column is a read path the writers maintain; here it is
// set directly because the subject is the READ.
func seedDocument(
	t *testing.T, e *Env, org ids.UUID, parentType string, parent ids.UUID, name, category string, pinned bool,
) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	// The storage key is bound rather than built from $1: Postgres deduces one
	// type per parameter, and using the id as both a uuid and a string leaves it
	// with two.
	e.WsExec(t, `
		INSERT INTO attachment (id, workspace_id, entity_type, entity_id, filename, storage_key,
		                        source, captured_by, category, organization_id, pinned)
		VALUES ($1, $2, $3, $4, $5, $6, 'upload', 'human:test', $7, $8, $9)`,
		id, e.WS, parentType, parent, name, "k/"+id.String(), category, org, pinned)
	return id
}

func TestOrganizationDocumentsHideAFileWhoseParentIsOutOfScope(t *testing.T) {
	e := Setup(t)
	store := activities.NewStore(e.Pool)
	pipeline, stage, _ := DealFixture(t, e)

	org := e.SeedOrg(t, "Acme", &e.Rep1)
	mine := e.SeedDeal(t, "My deal", pipeline, stage, &e.Rep1)
	theirs := e.SeedDeal(t, "Another team's deal", pipeline, stage, &e.Rep3)
	e.WsExec(t, `UPDATE deal SET organization_id = $2 WHERE id = $1`, mine, org)
	e.WsExec(t, `UPDATE deal SET organization_id = $2 WHERE id = $1`, theirs, org)

	seedDocument(t, e, org, "deal", mine, "our-contract.pdf", "contract", false)
	seedDocument(t, e, org, "deal", theirs, "their-contract.pdf", "contract", false)
	seedDocument(t, e, org, "organization", org, "nda.pdf", "legal", false)

	docs, _, err := store.ListOrganizationDocuments(
		e.As(e.Rep1, []ids.UUID{e.Team1}, AccountRepPerms), org, activities.DocumentFilters{})
	if err != nil {
		t.Fatalf("list documents: %v", err)
	}
	// Two, not three: the contract on the other team's deal is neither listed
	// nor counted. A total of three with two rows would say something exists.
	if len(docs) != 2 {
		names := make([]string, 0, len(docs))
		for _, d := range docs {
			names = append(names, d.Filename)
		}
		t.Fatalf("documents = %v, want only the two whose parents this caller can read", names)
	}
	for _, doc := range docs {
		if doc.Filename == "their-contract.pdf" {
			t.Error("a file on a deal outside the caller's row scope reached the account library")
		}
	}
}

func TestOrganizationDocumentsPutPinnedFirstAndFilterByCategory(t *testing.T) {
	e := Setup(t)
	store := activities.NewStore(e.Pool)
	org := e.SeedOrg(t, "Acme", &e.Rep1)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, AccountRepPerms)

	seedDocument(t, e, org, "organization", org, "old-offer.pdf", "offer", false)
	seedDocument(t, e, org, "organization", org, "signed-contract.pdf", "contract", true)
	seedDocument(t, e, org, "organization", org, "nda.pdf", "legal", false)

	docs, _, err := store.ListOrganizationDocuments(ctx, org, activities.DocumentFilters{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(docs) != 3 || docs[0].Filename != "signed-contract.pdf" {
		t.Fatalf("first document = %+v, want the pinned one — a pin is what a reader asked to keep at the top", docs)
	}

	contract := "contract"
	only, _, err := store.ListOrganizationDocuments(ctx, org,
		activities.DocumentFilters{Category: &contract})
	if err != nil {
		t.Fatalf("filtered list: %v", err)
	}
	if len(only) != 1 || only[0].Filename != "signed-contract.pdf" {
		t.Errorf("category filter returned %+v, want only the contract", only)
	}
}

// A cycle here is not cosmetic: every reader walking "what replaced this" would
// loop forever on it.
func TestAttachmentMetadataRefusesASupersedesCycle(t *testing.T) {
	e := Setup(t)
	store := activities.NewStore(e.Pool)
	org := e.SeedOrg(t, "Acme", &e.Rep1)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, docWritePerms)

	first := seedDocument(t, e, org, "organization", org, "v1.pdf", "contract", false)
	second := seedDocument(t, e, org, "organization", org, "v2.pdf", "contract", false)

	// v2 replaces v1 — fine.
	if _, err := store.UpdateAttachmentMetadata(ctx, second,
		activities.DocumentMetadata{Supersedes: &first}); err != nil {
		t.Fatalf("recording that v2 replaces v1: %v", err)
	}
	// v1 replacing v2 would close the loop.
	_, err := store.UpdateAttachmentMetadata(ctx, first,
		activities.DocumentMetadata{Supersedes: &second})
	if err == nil {
		t.Fatal("a supersedes cycle was accepted — every walk of the chain would now loop")
	}
}

// The write inherits the parent's authority, like every other attachment
// operation. A rep who may read a company but not change it may not retitle its
// contract either.
var docWritePerms = principal.Permissions{
	RoleKeys: []string{"rep"},
	Objects: map[string]principal.ObjectGrant{
		"organization": {Read: true, Update: true},
		"deal":         {Read: true},
		"person":       {Read: true},
	},
	RowScope: principal.RowScopeTeam,
}
