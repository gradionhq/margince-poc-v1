// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// Importing a LinkedIn Connections.csv and matching the ghosts it creates
// (CG-DDL-2 / ADR-0078 §2.1b).
//
// What has to hold:
//
//   - the real export parses — preamble, locale headers, ragged rows and all;
//   - re-importing updates rather than duplicating, because people re-export
//     regularly and a doubled network makes every reach count a lie;
//   - an EXACT ADDRESS match auto-confirms, the same rule capture's dedupe uses;
//   - a NAME + EMPLOYER match only SUGGESTS, and an ambiguous one does not even
//     do that — two Andreas Müllers at one firm is the case that must not be
//     resolved by a coin flip;
//   - nothing ever becomes a person.

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// realExport is shaped like the file LinkedIn actually hands a member: three
// preamble lines, a blank, then the header. A parser that assumed line 1 was
// the header would read the notes as columns.
const realExport = `Notes:
"When exporting your connection data, you may notice that some of the email addresses are missing."

First Name,Last Name,URL,Email Address,Company,Position,Connected On
Dana,Buyer,https://www.linkedin.com/in/danabuyer,dana@acme.test,Acme GmbH,CTO,15 Mar 2024
Andreas,Müller,https://www.linkedin.com/in/amueller,,Acme GmbH,Head of IT,02 Feb 2023
Nobody,Atall,https://www.linkedin.com/in/nobody,,Unknown Ltd,Founder,01 Jan 2020
`

func (e *dedupeEnv) importExport(t *testing.T) LinkedInImportResult {
	t.Helper()
	res, err := e.store.ImportLinkedInConnections(e.as(), strings.NewReader(realExport))
	if err != nil {
		t.Fatalf("importing the export: %v", err)
	}
	return res
}

func (e *dedupeEnv) ghostStatus(t *testing.T, name string) (status string, person *ids.UUID) {
	t.Helper()
	ctx := e.as()
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT match_status, matched_person_id FROM linkedin_connection WHERE full_name = $1`,
			name).Scan(&status, &person)
	}); err != nil {
		t.Fatalf("reading ghost %q: %v", name, err)
	}
	return status, person
}

func TestTheRealLinkedInExportParses(t *testing.T) {
	e := setupDedupe(t)
	res := e.importExport(t)

	if res.Imported != 3 {
		t.Fatalf("imported %d of 3 connections (skipped %d) — the preamble or the header threw the parser", res.Imported, res.Skipped)
	}
	// Re-importing a refreshed export must not double the network.
	again := e.importExport(t)
	if again.Imported != 3 {
		t.Errorf("re-import stored %d rows", again.Imported)
	}
	var total int
	ctx := e.as()
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM linkedin_connection`).Scan(&total)
	}); err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Errorf("after two imports the network holds %d connections, want 3 — every reach count would be doubled", total)
	}
}

func TestAnAddressMatchConfirmsAndANameMatchOnlySuggests(t *testing.T) {
	e := setupDedupe(t)
	org := e.seedOrgNamed(t, "Acme GmbH")

	// Dana is a known contact WITH the address the export carries.
	dana := e.seedContact(t, "Dana Buyer")
	e.seedEmail(t, dana, "dana@acme.test")
	e.employ(t, dana, org)
	// Andreas is a known contact at the same employer, but the export has no
	// address for him — name and employer are all there is.
	andreas := e.seedContact(t, "Andreas Müller")
	e.employ(t, andreas, org)

	e.importExport(t)
	if _, err := e.store.MatchLinkedInConnections(e.as()); err != nil {
		t.Fatalf("matching: %v", err)
	}

	// An address is identity, here as everywhere else in this module.
	if status, person := e.ghostStatus(t, "Dana Buyer"); status != "confirmed" || person == nil || *person != dana.UUID {
		t.Errorf("the address match is %q → %v, want confirmed → %s", status, person, dana)
	}
	// A name plus an employer is plausible and no more. Auto-confirming it
	// would quietly attach a stranger to a customer record.
	if status, person := e.ghostStatus(t, "Andreas Müller"); status != "suggested" || person == nil || *person != andreas.UUID {
		t.Errorf("the name+employer match is %q → %v, want suggested → %s", status, person, andreas)
	}
	// A connection at a company nobody here knows stays a ghost.
	if status, _ := e.ghostStatus(t, "Nobody Atall"); status != "unmatched" {
		t.Errorf("an unknown connection is %q, want unmatched", status)
	}
}

func TestTwoContactsOfTheSameNameAtOneEmployerAreNotGuessedBetween(t *testing.T) {
	e := setupDedupe(t)
	org := e.seedOrgNamed(t, "Acme GmbH")
	// The case the whole suggest/confirm split exists for.
	first := e.seedContact(t, "Andreas Müller")
	second := e.seedContact(t, "Andreas Müller")
	e.employ(t, first, org)
	e.employ(t, second, org)

	e.importExport(t)
	if _, err := e.store.MatchLinkedInConnections(e.as()); err != nil {
		t.Fatalf("matching: %v", err)
	}
	if status, person := e.ghostStatus(t, "Andreas Müller"); status != "unmatched" || person != nil {
		t.Errorf("an ambiguous name was resolved to %q → %v; picking one is a guess wearing a confirmation's clothes", status, person)
	}
}

func TestImportingConnectionsCreatesNoPeople(t *testing.T) {
	e := setupDedupe(t)
	var before, after int
	ctx := e.as()
	count := func(into *int) {
		if err := e.store.tx(ctx, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM person`).Scan(into)
		}); err != nil {
			t.Fatal(err)
		}
	}
	count(&before)
	e.importExport(t)
	if _, err := e.store.MatchLinkedInConnections(e.as()); err != nil {
		t.Fatalf("matching: %v", err)
	}
	count(&after)
	if after != before {
		t.Errorf("the import created %d people; a LinkedIn export is a list of third parties who never agreed to be in this CRM", after-before)
	}
}

// seedOrgNamed writes one account under the given display name.
func (e *dedupeEnv) seedOrgNamed(t *testing.T, name string) ids.OrganizationID {
	t.Helper()
	id := ids.New[ids.OrganizationKind]()
	ctx := e.as()
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO organization (id, workspace_id, display_name, name_source, owner_id, source, captured_by, visibility)
			VALUES ($1, $2, $3, 'human', $4, 'manual', 'human:test', 'workspace')`,
			id, e.ws, name, e.rep)
		return err
	}); err != nil {
		t.Fatalf("seeding org %s: %v", name, err)
	}
	return id
}

// seedEmail gives a contact one address.
func (e *dedupeEnv) seedEmail(t *testing.T, person ids.PersonID, email string) {
	t.Helper()
	ctx := e.as()
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO person_email (workspace_id, person_id, email, is_primary, source, captured_by)
			VALUES ($1, $2, $3, true, 'manual', 'human:test')`, e.ws, person, email)
		return err
	}); err != nil {
		t.Fatalf("seeding email for %s: %v", person, err)
	}
}

// employ puts a contact on an account's payroll, live.
func (e *dedupeEnv) employ(t *testing.T, person ids.PersonID, org ids.OrganizationID) {
	t.Helper()
	ctx := e.as()
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO relationship (workspace_id, kind, person_id, organization_id, source, captured_by)
			VALUES ($1, 'employment', $2, $3, 'manual', 'human:test')`, e.ws, person, org)
		return err
	}); err != nil {
		t.Fatalf("employing %s: %v", person, err)
	}
}
