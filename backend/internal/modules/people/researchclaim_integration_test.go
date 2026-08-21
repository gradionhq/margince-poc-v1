// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// Saving the research claims a human accepted, over a real Postgres.
//
// The statement is EXECUTED here rather than read, because a claim's columns
// are only checkable against a database: the accept path's other tests filter
// input and return before the transaction opens, so they can say nothing about
// what a row ends up holding.
//
// Every column is asserted individually rather than "a row exists". A row
// exists for any argument order, so the weaker assertion admits a statement
// whose values are each their neighbour's — and the stored value is what a
// reader is later shown as a fact about a person.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// asArchiver is a COLLEAGUE who may retire a record, which the accepting rep
// may not: `as()` carries no delete grant. Two seats, so an acceptance refused
// on an archived person is refused because the record is retired and not
// because the caller lacks authority over it.
func (e *dedupeEnv) asArchiver() context.Context {
	other := e.otherRep
	ctx := principal.WithWorkspaceID(context.Background(), e.ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + other.String(), UserID: other,
		Permissions: principal.Permissions{
			RoleKeys: []string{"rep"},
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

func claimRowsFor(ctx context.Context, t *testing.T, e *dedupeEnv, personID ids.PersonID) int {
	t.Helper()
	var rows int
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM person_profile_field WHERE person_id = $1`, personID).Scan(&rows)
	}); err != nil {
		t.Fatalf("count the stored claims: %v", err)
	}
	return rows
}

// Every column holds what the caller supplied for it, and the row is found
// under the person and field a reader looks it up by — the two halves are
// separate assertions because a row stored under the wrong key is invisible to
// the lookup while still being a row.
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
	if rows := claimRowsFor(ctx, t, e, personID); rows != 1 {
		t.Errorf("rows under this person = %d, want 1 — the claim is stored under another key too", rows)
	}
}

// A later claim about the same field replaces the earlier one. This is the
// DO UPDATE arm, which the accept drawer reaches whenever a reader revisits a
// field they have already chosen.
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
	// The trigger owns the bump, so a version still at 1 means the row was
	// inserted afresh rather than updated — the replacement never happened and
	// a reader would be looking at a second row under a key they cannot see.
	if got.version != 2 {
		t.Errorf("version = %d, want 2 after the replacement", got.version)
	}
	if rows := claimRowsFor(ctx, t, e, personID); rows != 1 {
		t.Errorf("rows for the replaced field = %d, want 1", rows)
	}
}

// One acceptance of several claims writes them all, and carries the write
// shape: the domain rows, one audit_log row and one event_outbox row in ONE
// transaction. Without the ledger half a reader's decision changes a person's
// record with nothing recording who decided it, on a table whose whole purpose
// is to make a fact traceable.
func TestOneAcceptanceWritesEveryClaimAndTheWriteShape(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	personID, _ := e.seedEmployedPerson(ctx, t,
		"Ana Sol", "ana@many.test", "Sol SA", "many.test")

	accepted := map[string]string{
		"title":    "CFO",
		"role":     "Signatory",
		"linkedin": "https://linkedin.test/in/anasol",
	}
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
	if saved != len(accepted) {
		t.Fatalf("saved = %d, want %d", saved, len(accepted))
	}
	for field, value := range accepted {
		if got := readStoredClaim(ctx, t, e, personID, field); got.value != value {
			t.Errorf("%s = %q, want %q", field, got.value, value)
		}
	}

	audits, events, counted := acceptanceLedger(ctx, t, e, personID)
	if audits != 1 {
		t.Errorf("%d audit_log rows for the acceptance, want 1 — nothing records who accepted these claims", audits)
	}
	if events != 1 {
		t.Errorf("%d event_outbox rows for the acceptance, want 1 — no consumer is told the person changed", events)
	}
	// The audited count is read off the row rather than trusted from the
	// return value: the same number reaches the caller and the ledger, and a
	// ledger claiming more than landed is a false history of a person's record.
	if counted != len(accepted) {
		t.Errorf("audited research_claims_saved = %d, want %d", counted, len(accepted))
	}
}

// acceptanceLedger returns the person's audit_log and event_outbox rows for an
// acceptance, and the count the audit row recorded.
//
// Both sides key on the acceptance's own marker — the audit's
// research_claims_saved and the envelope's profile_fields — because the seed
// that creates the person, their employer and the edge between them emits
// events carrying this person's id too. Matching the id alone counts those and
// reads as "the acceptance emitted three".
func acceptanceLedger(ctx context.Context, t *testing.T, e *dedupeEnv, personID ids.PersonID) (audits, events, counted int) {
	t.Helper()
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM audit_log
			         WHERE entity_type = 'person' AND entity_id = $1
			           AND after -> 'research_claims_saved' IS NOT NULL),
			       (SELECT count(*) FROM event_outbox
			         WHERE envelope::text LIKE '%' || $1::text || '%'
			           AND envelope::text LIKE '%profile_fields%'),
			       coalesce((SELECT (after ->> 'research_claims_saved')::int FROM audit_log
			                  WHERE entity_type = 'person' AND entity_id = $1
			                    AND after -> 'research_claims_saved' IS NOT NULL
			                  LIMIT 1), 0)`,
			personID).Scan(&audits, &events, &counted)
	}); err != nil {
		t.Fatalf("counting the acceptance's ledger rows: %v", err)
	}
	return audits, events, counted
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
	if rows := claimRowsFor(ctx, t, e, personID); rows != 0 {
		t.Errorf("rows = %d, want 0 — a refused claim was stored anyway", rows)
	}
}

// A field outside the closed set takes the WHOLE acceptance down with it. The
// set is the database's (person_profile_field's field check), so this claim is
// refused mid-transaction rather than by the loop above it — which is what
// makes it the case that proves the acceptance is all-or-nothing. A reader
// whose second choice was rejected must not find their first one saved, since
// they would have no way to tell which half of their decision took effect.
func TestAnUnknownFieldTakesTheWholeAcceptanceDown(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	personID, _ := e.seedEmployedPerson(ctx, t,
		"Milan Prit", "milan@atomic.test", "Prit doo", "atomic.test")

	if _, err := e.store.SaveResearchClaims(ctx, personID, []ResearchClaimInput{
		{Field: "title", Value: "COO", Quote: "Milan Prit, COO.", SourceURL: "https://atomic.test/a"},
		{Field: "email", Value: "milan@atomic.test", Quote: "Contact Milan at this address.", SourceURL: "https://atomic.test/b"},
	}); err == nil {
		t.Fatal("SaveResearchClaims = nil error for a field outside the closed set, want a refusal")
	}
	if rows := claimRowsFor(ctx, t, e, personID); rows != 0 {
		t.Errorf("rows = %d, want 0 — the first claim survived a refused acceptance", rows)
	}
}

// An archived person takes no new claims. The accept drawer can be open when
// somebody else archives the record, so the guard is the write's and not the
// screen's.
func TestAnArchivedPersonTakesNoResearchClaim(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	personID, _ := e.seedEmployedPerson(ctx, t,
		"Elke Braun", "elke@archived.test", "Braun GmbH", "archived.test")
	if _, err := e.store.ArchivePerson(e.asArchiver(), personID, nil); err != nil {
		t.Fatalf("archive the person: %v", err)
	}

	if _, err := e.store.SaveResearchClaims(ctx, personID, []ResearchClaimInput{{
		Field: "title", Value: "Owner",
		Quote: "Elke Braun owns the company.", SourceURL: "https://archived.test/about",
	}}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("SaveResearchClaims on an archived person = %v, want ErrNotFound", err)
	}
}

// A rep bounded to their own rows cannot accept claims about a person they may
// only READ. The seat matters more here than anywhere else in this file: every
// other case runs unbounded, where the write-authority arm short-circuits, so
// without this one the narrower half of the gate is never exercised.
//
// ErrPermissionDenied and not ErrNotFound, which is the write gate's own rule
// (auth/writescope.go): the visibility probe runs first and keeps its 404, and
// this caller PASSES it — they can open the record — so the write arm refuses
// in the open rather than hiding a row already shown to them.
func TestARepWhoMayOnlyReadAPersonCannotAcceptClaimsOnThem(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	personID, _ := e.seedEmployedPerson(ctx, t,
		"Sofia Reyes", "sofia@bounded.test", "Reyes SL", "bounded.test")

	reader := e.asRowScope(principal.RowScopeOwn)
	if _, err := e.store.SaveResearchClaims(reader, personID, []ResearchClaimInput{{
		Field: "title", Value: "Head of Legal",
		Quote: "Sofia Reyes heads the legal team.", SourceURL: "https://bounded.test/team",
	}}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("SaveResearchClaims without write authority = %v, want ErrPermissionDenied", err)
	}
	if rows := claimRowsFor(ctx, t, e, personID); rows != 0 {
		t.Errorf("rows = %d, want 0 — a refused acceptance wrote anyway", rows)
	}
}
