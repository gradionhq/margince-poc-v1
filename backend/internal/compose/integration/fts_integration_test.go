// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The 0052 search linguistics against the real migrated Postgres:
// accent folding (Muller finds Müller), the trigram quick-find (a name
// fragment finds the record without full-token match), and per-language
// stemming on activity free text (Vertrag finds Verträge on a row
// captured as German).

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestSearchFoldsAccentsAndStemsByLanguage(t *testing.T) {
	e := Setup(t)
	admin := e.Admin()

	mueller, err := e.People.CreatePerson(admin, people.CreatePersonInput{
		FullName: "Jürgen Müller", Source: "manual",
	})
	if err != nil {
		t.Fatalf("create person: %v", err)
	}

	// Accent folding: the unaccented spelling must find the umlaut row.
	searchStore := search.NewStore(e.DB())
	page, err := searchStore.Search(admin, search.Input{Query: "Muller", Types: []string{"person"}})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !hasHit(page, ids.UUID(mueller.Id)) {
		t.Error("search for 'Muller' did not find 'Müller' — accent folding is broken")
	}

	// Quick-find: a name fragment (no full token) must hit via trigram.
	persons, _, err := e.People.ListPeople(admin, people.ListPeopleInput{Query: strPtr("Müll")})
	if err != nil {
		t.Fatalf("list people: %v", err)
	}
	found := false
	for _, p := range persons {
		if p.Id == mueller.Id {
			found = true
		}
	}
	if !found {
		t.Error("list q='Müll' did not quick-find 'Jürgen Müller' — the trigram path is broken")
	}

	// German stemming: an activity captured as language=de matches the
	// singular query against its plural body.
	activityID := ids.NewV7()
	e.WsExec(t, `INSERT INTO activity (id, kind, subject, body, language, occurred_at, source, captured_by)
		VALUES ($1, 'note', 'Unterlagen', 'Bitte die Verträge prüfen', 'de', now(), 'manual', 'human:x')`,
		activityID)
	page, err = searchStore.Search(admin, search.Input{Query: "Vertrag", Types: []string{"activity"}})
	if err != nil {
		t.Fatalf("search activities: %v", err)
	}
	if !hasHit(page, activityID) {
		t.Error("search for 'Vertrag' did not find the German activity carrying 'Verträge' — language stemming is broken")
	}
}

func TestSearchFoldsApostrophesInNames(t *testing.T) {
	e := Setup(t)
	admin := e.Admin()

	// The typographic apostrophe (U+2019) — what pasted text actually
	// carries; f_unaccent folds it to ASCII ' before the strip.
	oreilly, err := e.People.CreatePerson(admin, people.CreatePersonInput{
		FullName: "Tim O’Reilly", Source: "manual",
	})
	if err != nil {
		t.Fatalf("create person: %v", err)
	}

	// Global search: the collapsed spelling, the apostrophe spelling,
	// and the bare surname must all find the row.
	searchStore := search.NewStore(e.DB())
	for _, q := range []string{"oreilly", "o'reilly", "o’reilly", "reilly"} {
		page, err := searchStore.Search(admin, search.Input{Query: q, Types: []string{"person"}})
		if err != nil {
			t.Fatalf("search %q: %v", q, err)
		}
		if !hasHit(page, ids.UUID(oreilly.Id)) {
			t.Errorf("search for %q did not find 'Tim O’Reilly' — apostrophe folding is broken", q)
		}
	}

	// List quick-find: the trigram contains-match must fold the same way.
	persons, _, err := e.People.ListPeople(admin, people.ListPeopleInput{Query: strPtr("oreil")})
	if err != nil {
		t.Fatalf("list people: %v", err)
	}
	found := false
	for _, p := range persons {
		if p.Id == oreilly.Id {
			found = true
		}
	}
	if !found {
		t.Error("list q='oreil' did not quick-find 'Tim O’Reilly' — the folded trigram path is broken")
	}
}

func hasHit(page search.Page, id ids.UUID) bool {
	for _, hit := range page.Hits {
		if hit.ID == id {
			return true
		}
	}
	return false
}

// A project hit carries `key · company` as its excerpt, so a reader can tell
// two accounts' "Phase 2" apart without opening each. A project with no key
// shows the company alone rather than a dangling separator.
func TestProjectSearchHitsCarryTheKeyAndTheCompanyAsTheSnippet(t *testing.T) {
	e := Setup(t)
	admin := e.Admin()
	org := e.SeedOrg(t, "Acme Tooling", &e.Rep1)
	key := "ERP-27"
	keyed, err := e.Deals.CreateProject(admin, deals.CreateProjectInput{
		Name: "Cutover rehearsal", Key: &key, OrganizationID: orgIDOf(org), Source: "manual",
	})
	if err != nil {
		t.Fatalf("create keyed project: %v", err)
	}
	keyless, err := e.Deals.CreateProject(admin, deals.CreateProjectInput{
		Name: "Cutover planning", OrganizationID: orgIDOf(org), Source: "manual",
	})
	if err != nil {
		t.Fatalf("create keyless project: %v", err)
	}

	page, err := search.NewStore(e.DB()).Search(admin, search.Input{Query: "Cutover", Types: []string{"project"}})
	if err != nil {
		t.Fatalf("search projects: %v", err)
	}
	snippets := map[ids.UUID]string{}
	for _, hit := range page.Hits {
		snippets[hit.ID] = hit.Snippet
	}
	if got := snippets[ids.UUID(keyed.Id)]; got != "ERP-27 · Acme Tooling" {
		t.Errorf("keyed project snippet = %q, want \"ERP-27 · Acme Tooling\"", got)
	}
	if got := snippets[ids.UUID(keyless.Id)]; got != "Acme Tooling" {
		t.Errorf("keyless project snippet = %q, want the company alone", got)
	}
}

// Naming the company behind a project is a read of the organization row. A
// searcher outside that row's scope — here a capture-private company another
// rep owns — is shown the key alone, and a searcher with no organization grant
// at all likewise; the project hit itself still answers.
func TestProjectSearchSnippetNamesTheCompanyOnlyToACallerWhoMayReadIt(t *testing.T) {
	e := Setup(t)
	org := e.SeedOrg(t, "Acme Tooling", &e.Rep1)
	key := "ERP-27"
	project, err := e.Deals.CreateProject(e.Admin(), deals.CreateProjectInput{
		Name: "Cutover rehearsal", Key: &key, OrganizationID: orgIDOf(org), Source: "manual",
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	// Owner-private AFTER the project exists: creating one is a read of the
	// company, and a private company is out of even the admin's scope.
	e.MakeCapturePrivate(t, "organization", org, e.Rep1)
	snippetFor := func(ctx context.Context) string {
		t.Helper()
		page, err := search.NewStore(e.DB()).Search(ctx, search.Input{Query: "Cutover", Types: []string{"project"}})
		if err != nil {
			t.Fatalf("search projects: %v", err)
		}
		for _, hit := range page.Hits {
			if hit.ID == ids.UUID(project.Id) {
				return hit.Snippet
			}
		}
		t.Fatal("the project hit is missing, so the snippet assertion proves nothing")
		return ""
	}

	if got := snippetFor(e.As(e.Rep1, []ids.UUID{e.Team1}, roomPerms)); got != "ERP-27 · Acme Tooling" {
		t.Errorf("owner's snippet = %q, want the company named", got)
	}
	if got := snippetFor(e.As(e.Rep3, []ids.UUID{e.Team2}, roomPerms)); got != "ERP-27" {
		t.Errorf("another rep's snippet = %q, want the key alone — the company is capture-private to Rep1", got)
	}
	if got := snippetFor(e.As(e.Rep1, []ids.UUID{e.Team1}, withoutGrant(roomPerms, "organization"))); got != "ERP-27" {
		t.Errorf("snippet without the organization grant = %q, want the key alone", got)
	}
}
