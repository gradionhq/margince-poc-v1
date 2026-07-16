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
	"testing"

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
	searchStore := search.NewStore(e.Pool)
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
	e.WsExec(t, `INSERT INTO activity (id, workspace_id, kind, subject, body, language, occurred_at, source, captured_by)
		VALUES ($1, $2, 'note', 'Unterlagen', 'Bitte die Verträge prüfen', 'de', now(), 'manual', 'human:x')`,
		activityID, e.WS)
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
	searchStore := search.NewStore(e.Pool)
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
