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
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
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
		"organization":          {Read: true, Update: true},
		"deal":                  {Read: true},
		"person":                {Read: true},
		"installation_settings": {Read: true},
	},
	RowScope: principal.RowScopeTeam,
}

// The library is fed by the UPLOAD path, not by a hand-written column. A test
// that seeds organization_id itself proves only that the read filters on it;
// it passes just as happily when nothing in the product ever writes it, which
// is exactly how a file uploaded through the UI can be invisible to the account
// view while every gate stays green.
func TestAnUploadedDocumentReachesTheAccountLibrary(t *testing.T) {
	e := Setup(t)
	store := activities.NewStore(e.Pool).WithBlobstore(blobstore.NewMemory())
	pipeline, stage, _ := DealFixture(t, e)
	org := e.SeedOrg(t, "Acme", &e.Rep1)
	deal := e.SeedDeal(t, "Renewal", pipeline, stage, &e.Rep1)
	e.WsExec(t, `UPDATE deal SET organization_id = $2 WHERE id = $1`, deal, org)
	// Filing a document is an update of the record it hangs off, so this
	// uploader holds update on both parents it files against.
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, docUploadPerms)

	onOrg, err := store.UploadAttachment(ctx, activities.AttachmentInput{
		EntityType: "organization", EntityID: org, Filename: "nda.pdf", Body: []byte("bytes"),
	})
	if err != nil {
		t.Fatalf("uploading against the organization: %v", err)
	}
	onDeal, err := store.UploadAttachment(ctx, activities.AttachmentInput{
		EntityType: "deal", EntityID: deal, Filename: "quote.pdf", Body: []byte("bytes"),
	})
	if err != nil {
		t.Fatalf("uploading against the deal: %v", err)
	}

	docs, _, err := store.ListOrganizationDocuments(
		e.As(e.Rep1, []ids.UUID{e.Team1}, AccountRepPerms), org, activities.DocumentFilters{})
	if err != nil {
		t.Fatalf("ListOrganizationDocuments: %v", err)
	}
	found := map[string]bool{}
	for _, d := range docs {
		found[d.Filename] = true
	}
	// The deal's file rolls up to the deal's account; the organization's own
	// file rolls up to itself.
	if !found["nda.pdf"] || !found["quote.pdf"] {
		t.Fatalf("the account library holds %v, want both uploaded files (org=%s deal=%s)",
			found, onOrg.Id, onDeal.Id)
	}
}

// docUploadPerms may file a document against either parent this case uses.
var docUploadPerms = principal.Permissions{
	RoleKeys: []string{"rep"},
	Objects: map[string]principal.ObjectGrant{
		"organization":          {Read: true, Update: true},
		"deal":                  {Read: true, Update: true},
		"person":                {Read: true},
		"installation_settings": {Read: true},
	},
	RowScope: principal.RowScopeTeam,
}

// Pointing at a document you cannot read is a read. Accepting the id would both
// confirm the target exists and build a relationship across a visibility
// boundary, which is exactly what the parent gate exists to stop.
func TestAttachmentMetadataRefusesASupersedesTargetTheCallerCannotSee(t *testing.T) {
	e := Setup(t)
	store := activities.NewStore(e.Pool)
	pipeline, stage, _ := DealFixture(t, e)
	org := e.SeedOrg(t, "Acme", &e.Rep1)
	theirs := e.SeedDeal(t, "Another team's deal", pipeline, stage, &e.Rep3)
	e.WsExec(t, `UPDATE deal SET organization_id = $2 WHERE id = $1`, theirs, org)

	mine := seedDocument(t, e, org, "organization", org, "v2.pdf", "contract", false)
	hidden := seedDocument(t, e, org, "deal", theirs, "their-v1.pdf", "contract", false)

	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, docWritePerms)
	_, err := store.UpdateAttachmentMetadata(ctx, mine,
		activities.DocumentMetadata{Supersedes: &hidden})
	if err == nil {
		t.Fatal("a document out of the caller's scope was accepted as a supersedes target")
	}
	// Existence-hiding: the refusal must not distinguish "not allowed" from
	// "no such document", or the id itself becomes the disclosure.
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("refusal = %v, want ErrNotFound so the target's existence stays hidden", err)
	}
}

// Nothing read a SECOND page, which is why the cursor could order on three
// columns and continue on two and still look right. A page boundary that lands
// inside the pinned group then excludes every newer unpinned document from
// every later page — the documents are not late, they are unreachable.
func TestTheAccountLibraryPagesPastThePinnedGroup(t *testing.T) {
	e := Setup(t)
	store := activities.NewStore(e.Pool)
	org := e.SeedOrg(t, "Acme", &e.Rep1)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, AccountRepPerms)

	// Two pinned and two unpinned. Pinned sort ahead of every unpinned row, so
	// a limit of 2 ends the first page exactly on the pinned/unpinned boundary.
	want := map[string]bool{}
	seed := func(name string, pinned bool) {
		seedDocument(t, e, org, "organization", org, name, "contract", pinned)
		want[name] = true
	}
	seed("pinned-a.pdf", true)
	seed("pinned-b.pdf", true)
	seed("loose-a.pdf", false)
	seed("loose-b.pdf", false)

	limit := 2
	got := map[string]bool{}
	var cursor *string
	for range 4 { // bounded: four documents at two per page cannot need more
		docs, page, err := store.ListOrganizationDocuments(ctx, org,
			activities.DocumentFilters{Limit: &limit, Cursor: cursor})
		if err != nil {
			t.Fatalf("listing a page: %v", err)
		}
		for _, d := range docs {
			if got[d.Filename] {
				t.Fatalf("%s came back on two different pages", d.Filename)
			}
			got[d.Filename] = true
		}
		if !page.HasMore {
			break
		}
		next := page.NextCursor
		cursor = &next
	}
	for name := range want {
		if !got[name] {
			t.Errorf("%s never appeared on any page — the cursor skipped it permanently", name)
		}
	}
}

// An emailed contract hangs off the ACTIVITY that carried it. Leaving activity
// out of the parent kinds dropped every such file from the account library
// while the endpoint documented the opposite — the reader sees an empty
// contracts shelf and has no way to know the file is there.
//
// Its visibility is the link walk, not a row-scope clause over its own columns,
// so the arm has to use auth.ActivityScopeClause. This asserts both halves: the
// file appears for a reader who can open the activity, and does not for one who
// cannot.
func TestAnActivityBorneFileReachesTheLibraryAndStaysGated(t *testing.T) {
	e := Setup(t)
	store := activities.NewStore(e.Pool)
	pipeline, stage, _ := DealFixture(t, e)
	org := e.SeedOrg(t, "Acme", &e.Rep1)
	mine := e.SeedDeal(t, "My deal", pipeline, stage, &e.Rep1)
	theirs := e.SeedDeal(t, "Another team's deal", pipeline, stage, &e.Rep3)
	e.WsExec(t, `UPDATE deal SET organization_id = $2 WHERE id = $1`, mine, org)
	e.WsExec(t, `UPDATE deal SET organization_id = $2 WHERE id = $1`, theirs, org)

	// Two emails on this account: one linked to a deal the rep covers, one to a
	// deal they do not.
	visibleEmail := seedActivityWithDeal(t, e, mine)
	hiddenEmail := seedActivityWithDeal(t, e, theirs)
	seedDocument(t, e, org, "activity", visibleEmail, "signed-msa.pdf", "contract", false)
	seedDocument(t, e, org, "activity", hiddenEmail, "their-msa.pdf", "contract", false)

	docs, _, err := store.ListOrganizationDocuments(
		e.As(e.Rep1, []ids.UUID{e.Team1}, AccountRepPerms), org, activities.DocumentFilters{})
	if err != nil {
		t.Fatalf("ListOrganizationDocuments: %v", err)
	}
	found := map[string]bool{}
	for _, d := range docs {
		found[d.Filename] = true
	}
	if !found["signed-msa.pdf"] {
		t.Error("the emailed contract never reached the account library")
	}
	if found["their-msa.pdf"] {
		t.Error("a file on an activity outside the caller's scope appeared in the library")
	}
}

// seedActivityWithDeal files one email against a deal, which is what gives the
// activity its scope through the link walk.
func seedActivityWithDeal(t *testing.T, e *Env, deal ids.UUID) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	e.WsExec(t, `
		INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, $2, 'email', 'Contract', $3::timestamptz, 'manual', 'human:test')`,
		id, e.WS, activityOccurredAt)
	e.WsExec(t, `
		INSERT INTO activity_link (workspace_id, activity_id, entity_type, deal_id)
		VALUES ($1, $2, 'deal', $3)`, e.WS, id, deal)
	return id
}

// When two companies turn out to be one, the survivor's library has to hold
// both sides' files. organization_id is a denormalized read path, so nothing
// moves it on its own — a file left pointing at the dissolved company is filed
// under a record that no longer exists, and to a user the contract has simply
// vanished.
func TestAnOrganizationMergeCarriesTheDocumentsAcross(t *testing.T) {
	e := Setup(t)
	files := activities.NewStore(e.Pool)
	orgs := people.NewStore(e.Pool)
	survivor := e.SeedOrg(t, "Acme", &e.Rep1)
	dissolved := e.SeedOrg(t, "Acme Holdings", &e.Rep1)

	seedDocument(t, e, dissolved, "organization", dissolved, "old-msa.pdf", "contract", false)
	seedDocument(t, e, survivor, "organization", survivor, "new-msa.pdf", "contract", false)

	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, docUploadPerms)
	if _, err := orgs.MergeOrganization(ctx,
		ids.From[ids.OrganizationKind](dissolved), ids.From[ids.OrganizationKind](survivor)); err != nil {
		t.Fatalf("merging the duplicate company: %v", err)
	}

	docs, _, err := files.ListOrganizationDocuments(
		e.As(e.Rep1, []ids.UUID{e.Team1}, AccountRepPerms), survivor, activities.DocumentFilters{})
	if err != nil {
		t.Fatalf("ListOrganizationDocuments: %v", err)
	}
	found := map[string]bool{}
	for _, d := range docs {
		found[d.Filename] = true
	}
	if !found["old-msa.pdf"] {
		t.Error("the dissolved company's contract did not follow the merge — it is filed under a record that no longer exists")
	}
	if !found["new-msa.pdf"] {
		t.Error("the survivor's own contract went missing across the merge")
	}
}

// activityOccurredAt is a fixed instant: nothing here turns on when the email
// arrived, and a real clock in a fixture is a flake waiting for the boundary
// it happens to sit on.
const activityOccurredAt = "2026-08-01T09:00:00Z"
