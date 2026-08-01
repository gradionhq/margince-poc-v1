// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// Deciding the matcher's suggestions, and what a decision does (ADR-0078
// §2.1b).
//
// The unit tests cannot reach any of this: every rule here is a property of
// SQL — that a confirmation stamps the connection's own URL onto the contact,
// that it never overwrites one already there, that a rejection is durable, and
// that the reach read counts what it says it counts.

import (
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// suggestedGhost is the export's one name-and-employer match — the row every
// test here decides. Named rather than parameterized because the fixture has
// exactly one suggestion by construction: Dana carries an address and
// auto-confirms, and Nobody Atall works somewhere unknown.
const suggestedGhost = "Andreas Müller"

func (e *dedupeEnv) ghostID(t *testing.T) ids.UUID {
	t.Helper()
	ctx := e.as()
	var id ids.UUID
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT id FROM linkedin_connection WHERE full_name = $1`, suggestedGhost).Scan(&id)
	}); err != nil {
		t.Fatalf("reading the id of ghost %q: %v", suggestedGhost, err)
	}
	return id
}

func (e *dedupeEnv) linkedInHandle(t *testing.T, person ids.PersonID) (string, bool) {
	t.Helper()
	ctx := e.as()
	var handle string
	found := true
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx,
			`SELECT handle FROM person_social WHERE person_id = $1 AND platform = 'linkedin'`,
			person.UUID).Scan(&handle)
		if err != nil && err.Error() == pgx.ErrNoRows.Error() {
			found = false
			return nil
		}
		return err
	}); err != nil {
		t.Fatalf("reading %s's LinkedIn handle: %v", person, err)
	}
	return handle, found
}

// importAndMatch is the two steps every test here starts from: the export
// lands, the matcher runs, and the suggestions are waiting.
func (e *dedupeEnv) importAndMatch(t *testing.T) {
	t.Helper()
	e.importExport(t)
	if _, err := e.store.MatchLinkedInConnections(e.as(), e.rep); err != nil {
		t.Fatalf("matching: %v", err)
	}
}

func TestConfirmingASuggestionPutsTheLinkedInURLOnTheContact(t *testing.T) {
	e := setupDedupe(t)
	org := e.seedOrgNamed(t, "Acme GmbH")
	andreas := e.seedContact(t, "Andreas Müller")
	e.employ(t, andreas, org)
	e.importAndMatch(t)

	ghost := e.ghostID(t)
	decision, err := e.store.ConfirmLinkedInMatch(e.as(), ghost, ids.Nil)
	if err != nil {
		t.Fatalf("confirming: %v", err)
	}
	if decision.Connection.MatchStatus != "confirmed" {
		t.Errorf("the connection is %q after confirming, want confirmed", decision.Connection.MatchStatus)
	}
	if !decision.ProfileURLWritten {
		t.Error("the confirmation reported writing no profile URL, but the export carried one")
	}
	// The whole point of confirming: the contact now carries the connection.
	handle, found := e.linkedInHandle(t, andreas)
	if !found {
		t.Fatal("the contact gained no LinkedIn handle — confirming a match that changes nothing is not worth doing")
	}
	if handle != "https://www.linkedin.com/in/amueller" {
		t.Errorf("the contact carries %q, want the CONNECTION's own profile URL", handle)
	}
}

func TestConfirmingNeverOverwritesAHandleTheContactAlreadyHad(t *testing.T) {
	e := setupDedupe(t)
	org := e.seedOrgNamed(t, "Acme GmbH")
	andreas := e.seedContact(t, "Andreas Müller")
	e.employ(t, andreas, org)

	// Somebody already recorded a LinkedIn address for this contact. That is a
	// human's statement, and a match confirmation is not grounds to replace it.
	existing := "https://www.linkedin.com/in/the-one-we-already-had"
	ctx := e.as()
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO person_social (workspace_id, person_id, platform, handle)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, 'linkedin', $2)`,
			andreas.UUID, existing)
		return err
	}); err != nil {
		t.Fatalf("seeding the existing handle: %v", err)
	}

	e.importAndMatch(t)
	decision, err := e.store.ConfirmLinkedInMatch(e.as(), e.ghostID(t), ids.Nil)
	if err != nil {
		t.Fatalf("confirming: %v", err)
	}
	// The match still stands — only the copy did not happen.
	if decision.Connection.MatchStatus != "confirmed" {
		t.Errorf("the connection is %q, want confirmed even though nothing was copied", decision.Connection.MatchStatus)
	}
	if decision.ProfileURLWritten {
		t.Error("the decision claims it wrote a profile URL over one that was already there")
	}
	if handle, _ := e.linkedInHandle(t, andreas); handle != existing {
		t.Errorf("the contact's handle became %q, want the one already on the record %q", handle, existing)
	}
}

func TestConfirmingCanNameADifferentContactThanTheMatcherGuessed(t *testing.T) {
	e := setupDedupe(t)
	org := e.seedOrgNamed(t, "Acme GmbH")
	guessed := e.seedContact(t, "Andreas Müller")
	e.employ(t, guessed, org)
	// The person the member knows it actually is — a different record entirely.
	actual := e.seedContact(t, "A. Mueller (private)")
	e.employ(t, actual, org)
	e.importAndMatch(t)

	decision, err := e.store.ConfirmLinkedInMatch(e.as(), e.ghostID(t), actual.UUID)
	if err != nil {
		t.Fatalf("confirming against a corrected contact: %v", err)
	}
	if decision.Connection.MatchedPerson == nil || *decision.Connection.MatchedPerson != actual.UUID {
		t.Fatalf("the connection links to %v, want the contact the human named %s",
			decision.Connection.MatchedPerson, actual)
	}
	// The URL follows the correction, not the guess.
	if _, found := e.linkedInHandle(t, guessed); found {
		t.Error("the wrongly guessed contact was stamped with the handle anyway")
	}
	if _, found := e.linkedInHandle(t, actual); !found {
		t.Error("the corrected contact gained no handle")
	}
}

func TestARejectionSurvivesTheNextImportAndTheSweep(t *testing.T) {
	e := setupDedupe(t)
	org := e.seedOrgNamed(t, "Acme GmbH")
	andreas := e.seedContact(t, "Andreas Müller")
	e.employ(t, andreas, org)
	e.importAndMatch(t)

	ghost := e.ghostID(t)
	if _, err := e.store.RejectLinkedInMatch(e.as(), ghost); err != nil {
		t.Fatalf("rejecting: %v", err)
	}
	if status, person := e.ghostStatus(t, "Andreas Müller"); status != "rejected" || person != nil {
		t.Fatalf("after rejecting the ghost is %q → %v, want rejected → nil", status, person)
	}

	// Re-import the same file and run the matcher again. A rejection that did
	// not survive would put the same wrong guess in front of the same person
	// every time they refresh their export.
	e.importAndMatch(t)
	if status, person := e.ghostStatus(t, "Andreas Müller"); status != "rejected" || person != nil {
		t.Errorf("a re-import resurrected a rejected match: %q → %v", status, person)
	}
}

func TestTheReviewListShowsOnlyTheCallersOwnConnections(t *testing.T) {
	e := setupDedupe(t)
	org := e.seedOrgNamed(t, "Acme GmbH")
	andreas := e.seedContact(t, "Andreas Müller")
	e.employ(t, andreas, org)
	e.importAndMatch(t)

	suggested := "suggested"
	rows, _, err := e.store.ListMyLinkedInConnections(e.as(), ListMyLinkedInConnectionsInput{
		MatchStatus: &suggested,
	})
	if err != nil {
		t.Fatalf("listing suggestions: %v", err)
	}
	if len(rows) != 1 || rows[0].FullName != "Andreas Müller" {
		t.Fatalf("the suggestion queue holds %d rows (%+v), want the one name+employer match", len(rows), rows)
	}
	// The ORIGINAL company string, not the folded form the matcher compares on:
	// nobody can judge "acme" where the export said "Acme GmbH".
	if rows[0].CompanyName == nil || *rows[0].CompanyName != "Acme GmbH" {
		t.Errorf("the row shows company %v, want the export's own spelling", rows[0].CompanyName)
	}
	// And the contact it is guessed to be, resolved for display.
	if rows[0].MatchedPersonName == nil || *rows[0].MatchedPersonName != "Andreas Müller" {
		t.Errorf("the row names contact %v, want the suggested contact", rows[0].MatchedPersonName)
	}
}

func TestReachCountsConnectionsPerAccountAndSaysWhatItCannotShow(t *testing.T) {
	e := setupDedupe(t)
	org := e.seedOrgNamed(t, "Acme GmbH")
	dana := e.seedContact(t, "Dana Buyer")
	e.seedEmail(t, dana, "dana@acme.test")
	e.employ(t, dana, org)
	andreas := e.seedContact(t, "Andreas Müller")
	e.employ(t, andreas, org)
	e.importAndMatch(t)

	reach, err := e.store.MyLinkedInReach(e.as(), nil)
	if err != nil {
		t.Fatalf("reading reach: %v", err)
	}
	if len(reach.Accounts) != 1 {
		t.Fatalf("reach lists %d accounts (%+v), want the one on file", len(reach.Accounts), reach.Accounts)
	}
	acme := reach.Accounts[0]
	if acme.OrganizationID != org.UUID {
		t.Errorf("reach names account %s, want %s", acme.OrganizationID, org)
	}
	// Two of the three exported connections work at Acme.
	if acme.Connections != 2 {
		t.Errorf("reach counts %d connections at the account, want 2", acme.Connections)
	}
	// Only Dana's address match auto-confirmed, so only she counts as on file.
	// The gap — one person known there who is not yet a confirmed contact — is
	// the whole finding.
	if acme.ContactsOnFile != 1 {
		t.Errorf("reach counts %d contacts on file, want 1 (the address match)", acme.ContactsOnFile)
	}
	// The connection at a company nobody here knows is reported as unresolved
	// rather than dropped, because that number is what shrinks as accounts are
	// created.
	if reach.UnresolvedConnections != 1 {
		t.Errorf("reach reports %d unresolved connections, want 1", reach.UnresolvedConnections)
	}
	if reach.AccountsTotal != 1 {
		t.Errorf("reach reports %d accounts in total, want 1", reach.AccountsTotal)
	}
}
