// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// Saving an accepted research claim, over a real Postgres.
//
// The statement is exercised rather than read, because reading it is what
// failed: it shipped with its columns bound to the wrong placeholders and its
// argument count disagreeing with its highest placeholder, so every call
// errored — and the two tests this package already had over the accept path
// both filter input and never reach the database. A writer nothing executes is
// a writer nothing checks.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// asArchiver is the accepting rep's context plus the delete grant archiving
// needs. Separate from as() so the accept path is always exercised under a
// seat that may update and nothing more.
func (e *dedupeEnv) asArchiver() context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + e.rep.String(), UserID: e.rep,
		Permissions: principal.Permissions{
			RoleKeys: []string{"admin"},
			Objects: map[string]principal.ObjectGrant{
				"person": {Read: true, Update: true, Delete: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})
}

// storedClaim is one person_profile_field row as the reader sees it.
type storedClaim struct {
	value      string
	snippet    string
	sourceRef  string
	source     string
	capturedBy string
	version    int64
}

func readStoredClaim(ctx context.Context, t *testing.T, e *dedupeEnv, personID ids.PersonID, field string) storedClaim {
	t.Helper()
	var got storedClaim
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT value, evidence_snippet, source_ref, source, captured_by, version
			FROM person_profile_field WHERE person_id = $1 AND field = $2`,
			personID, field).Scan(&got.value, &got.snippet, &got.sourceRef,
			&got.source, &got.capturedBy, &got.version)
	}); err != nil {
		t.Fatalf("read back the %s claim: %v", field, err)
	}
	return got
}

// Every column holds what the caller supplied for it. Asserted column by
// column rather than "a row exists": the defect this covers wrote a row on
// every field, and each value was the neighbouring argument.
func TestSavingAResearchClaimWritesEachColumnTheValueItWasGiven(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	personID, _ := e.seedEmployedPerson(ctx, t,
		"Nadia Farrow", "nadia@research.test", "Farrow Systems", "research.test")

	saved, err := e.store.SaveResearchClaims(ctx, personID, []ResearchClaimInput{{
		Field:     "title",
		Value:     "Head of Procurement",
		Quote:     "Nadia Farrow leads procurement across the group.",
		SourceURL: "https://research.test/team",
	}})
	if err != nil {
		t.Fatalf("SaveResearchClaims: %v", err)
	}
	if saved != 1 {
		t.Fatalf("saved = %d, want 1", saved)
	}

	got := readStoredClaim(ctx, t, e, personID, "title")
	want := storedClaim{
		value:      "Head of Procurement",
		snippet:    "Nadia Farrow leads procurement across the group.",
		sourceRef:  "https://research.test/team",
		source:     researchSource,
		capturedBy: "human:" + e.rep.String(),
		version:    1,
	}
	if got != want {
		t.Errorf("stored claim = %+v, want %+v", got, want)
	}
}

// The person id is the person's, and the field is the field's. Its own case
// because the shipped defect put the WORKSPACE id in both, which a
// value-only assertion cannot see: the row was unreachable by the id a reader
// looks it up under, so "no row" and "wrong row" had to be told apart.
func TestASavedResearchClaimIsFoundUnderThePersonAndFieldItNames(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	personID, _ := e.seedEmployedPerson(ctx, t,
		"Tomas Reit", "tomas@lookup.test", "Reit GmbH", "lookup.test")

	if _, err := e.store.SaveResearchClaims(ctx, personID, []ResearchClaimInput{{
		Field:     "role",
		Value:     "Board member",
		Quote:     "Tomas Reit sits on the supervisory board.",
		SourceURL: "https://lookup.test/board",
	}}); err != nil {
		t.Fatalf("SaveResearchClaims: %v", err)
	}

	var rows int
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM person_profile_field
			WHERE person_id = $1 AND field = 'role'`, personID).Scan(&rows)
	}); err != nil {
		t.Fatalf("count the role claim: %v", err)
	}
	if rows != 1 {
		t.Errorf("rows under (person, 'role') = %d, want 1 — the claim is stored under something else", rows)
	}
}

// A later claim about the same field replaces the earlier one, and the
// trigger moves the version with it. This is the DO UPDATE arm, which the
// accept drawer reaches whenever a reader revisits a field they already chose.
func TestAcceptingASecondClaimAboutOneFieldReplacesTheFirst(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	personID, _ := e.seedEmployedPerson(ctx, t,
		"Iris Lund", "iris@replace.test", "Lund AS", "replace.test")

	first := ResearchClaimInput{
		Field:     "title",
		Value:     "Engineer",
		Quote:     "Iris Lund, engineer, joined in 2019.",
		SourceURL: "https://replace.test/old",
	}
	if _, err := e.store.SaveResearchClaims(ctx, personID, []ResearchClaimInput{first}); err != nil {
		t.Fatalf("first save: %v", err)
	}

	second := ResearchClaimInput{
		Field:     "title",
		Value:     "Chief Engineer",
		Quote:     "Iris Lund was promoted to chief engineer.",
		SourceURL: "https://replace.test/new",
	}
	if _, err := e.store.SaveResearchClaims(ctx, personID, []ResearchClaimInput{second}); err != nil {
		t.Fatalf("second save: %v", err)
	}

	got := readStoredClaim(ctx, t, e, personID, "title")
	if got.value != second.Value || got.snippet != second.Quote || got.sourceRef != second.SourceURL {
		t.Errorf("stored claim = %+v, want the second claim's value/quote/source", got)
	}
	// The trigger owns this, so a fill that stopped bumping it would mean the
	// UPDATE arm stopped firing and the row was being inserted afresh.
	if got.version != 2 {
		t.Errorf("version = %d, want 2 after the replacement", got.version)
	}
}

// Several claims in one call all land. The loop commits inside one
// transaction, so a statement that failed on the second claim would leave the
// reader's first choice saved and their second silently gone.
func TestEveryClaimInOneAcceptanceLands(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	personID, _ := e.seedEmployedPerson(ctx, t,
		"Ana Sol", "ana@many.test", "Sol SA", "many.test")

	saved, err := e.store.SaveResearchClaims(ctx, personID, []ResearchClaimInput{
		{Field: "title", Value: "CFO", Quote: "Ana Sol, CFO.", SourceURL: "https://many.test/a"},
		{Field: "role", Value: "Signatory", Quote: "Ana Sol may sign alone.", SourceURL: "https://many.test/b"},
		{
			Field: "linkedin", Value: "https://linkedin.test/in/anasol",
			Quote: "Ana Sol's profile is linked from the team page.", SourceURL: "https://many.test/c",
		},
	})
	if err != nil {
		t.Fatalf("SaveResearchClaims: %v", err)
	}
	if saved != 3 {
		t.Fatalf("saved = %d, want 3", saved)
	}
	for field, value := range map[string]string{
		"title":    "CFO",
		"role":     "Signatory",
		"linkedin": "https://linkedin.test/in/anasol",
	} {
		if got := readStoredClaim(ctx, t, e, personID, field); got.value != value {
			t.Errorf("%s = %q, want %q", field, got.value, value)
		}
	}
}

// A claim missing any of its three evidence parts is refused, and the table is
// left empty — a partial claim is not stored in a weaker form.
func TestAResearchClaimMissingItsEvidenceIsRefusedAndStoresNothing(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	personID, _ := e.seedEmployedPerson(ctx, t,
		"Petra Vogel", "petra@refuse.test", "Vogel KG", "refuse.test")

	for name, claim := range map[string]ResearchClaimInput{
		"no value":  {Field: "title", Quote: "Petra Vogel, director.", SourceURL: "https://refuse.test/a"},
		"no quote":  {Field: "title", Value: "Director", SourceURL: "https://refuse.test/a"},
		"no source": {Field: "title", Value: "Director", Quote: "Petra Vogel, director."},
	} {
		if _, err := e.store.SaveResearchClaims(ctx, personID, []ResearchClaimInput{claim}); err == nil {
			t.Errorf("%s: SaveResearchClaims = nil error, want a refusal", name)
		}
	}

	var rows int
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM person_profile_field WHERE person_id = $1`, personID).Scan(&rows)
	}); err != nil {
		t.Fatalf("count the claims: %v", err)
	}
	if rows != 0 {
		t.Errorf("rows = %d, want 0 — a refused claim was stored anyway", rows)
	}
}

// A source that is not a document a reader can open is refused at the WRITE,
// not only where the run produced it. A client is not obliged to send back
// what the run returned, so a read path that refuses a script URL and a write
// path that accepts it is how untrusted input reaches the record.
func TestAResearchClaimWhoseSourceIsNotAWebURLIsRefusedAtTheWrite(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	personID, _ := e.seedEmployedPerson(ctx, t,
		"Ravi Menon", "ravi@scheme.test", "Menon Ltd", "scheme.test")

	for _, source := range []string{
		"javascript:alert(1)",
		"data:text/html,<script>alert(1)</script>",
		"file:///etc/passwd",
		"https://",
	} {
		_, err := e.store.SaveResearchClaims(ctx, personID, []ResearchClaimInput{{
			Field: "title", Value: "CTO",
			Quote: "Ravi Menon is the CTO.", SourceURL: source,
		}})
		if err == nil {
			t.Errorf("source %q was accepted, want a refusal", source)
		}
	}
}

// An archived person takes no new claims. The accept drawer can be open when
// somebody else archives the record, so the guard is the write's, not the
// screen's.
func TestAnArchivedPersonTakesNoResearchClaim(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	personID, _ := e.seedEmployedPerson(ctx, t,
		"Elke Braun", "elke@archived.test", "Braun GmbH", "archived.test")
	// Archived by a seat that may delete, which the accepting rep is not: the
	// point is that a live grant to UPDATE does not survive the record being
	// retired by somebody else.
	if _, err := e.store.ArchivePerson(e.asArchiver(), personID, nil); err != nil {
		t.Fatalf("archive the person: %v", err)
	}

	if _, err := e.store.SaveResearchClaims(ctx, personID, []ResearchClaimInput{{
		Field: "title", Value: "Owner",
		Quote: "Elke Braun owns the company.", SourceURL: "https://archived.test/about",
	}}); err == nil {
		t.Fatal("SaveResearchClaims = nil error on an archived person, want a refusal")
	}
}
