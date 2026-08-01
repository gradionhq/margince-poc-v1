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
	if _, err := e.store.MatchLinkedInConnections(e.as(), e.rep); err != nil {
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
	if _, err := e.store.MatchLinkedInConnections(e.as(), e.rep); err != nil {
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
	if _, err := e.store.MatchLinkedInConnections(e.as(), e.rep); err != nil {
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

func TestAnUnmatchedGhostStillNamesTheSubject(t *testing.T) {
	e := setupDedupe(t)
	org := e.seedOrgNamed(t, "Acme GmbH")
	// Andreas is a contact at Acme. The export names him, but carries no
	// address, so the matcher only SUGGESTS — it never confirms.
	andreas := e.seedContact(t, "Andreas Müller")
	e.employ(t, andreas, org)
	e.importExport(t)
	if _, err := e.store.MatchLinkedInConnections(e.as(), e.rep); err != nil {
		t.Fatalf("matching: %v", err)
	}

	// The ghost holds his name, employer and position — imported from a
	// colleague's export without him ever being asked. An erasure that only
	// swept CONFIRMED matches would leave all of it behind, and would do so
	// while reporting the erasure complete.
	status, _ := e.ghostStatus(t, "Andreas Müller")
	if status != "suggested" {
		t.Fatalf("the ghost is %q, want suggested — this test is no longer about the unconfirmed case", status)
	}
	var byName int
	ctx := e.as()
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM linkedin_connection g
			 WHERE g.normalized_company IS NOT NULL
			   AND g.normalized_name = (SELECT lower(f_unaccent(full_name)) FROM person WHERE id = $1)
			   AND EXISTS (SELECT 1 FROM relationship r
			                WHERE r.person_id = $1 AND r.kind = 'employment'
			                  AND r.archived_at IS NULL AND r.organization_id = g.matched_org_id)`,
			andreas).Scan(&byName)
	}); err != nil {
		t.Fatal(err)
	}
	if byName != 1 {
		t.Errorf("the name+employer reach found %d ghosts for the subject, want 1 — "+
			"erasure and Art. 15 both use this reach, so a miss here is data kept after we certified it destroyed", byName)
	}
}

// The upload matches against what the workspace knows AT THAT SECOND, and on a
// new installation that is close to nothing: the export is uploaded during
// onboarding, and the contacts it could match arrive over the following hours
// as mail capture runs. Every one of those arrivals is a match the upload could
// not have made, and until the sweep existed nothing was going to make it —
// the ghost stayed unmatched forever and the account kept reporting that
// nobody here knew anyone.
//
// Measured on a real 5,064-row export: 54 contacts appeared in it by name and
// the upload-time pass matched 13.
func TestAContactTheWorkspaceLearnsAboutLaterIsStillMatched(t *testing.T) {
	e := setupDedupe(t)

	// The import happens FIRST, on an empty workspace. Nothing to match.
	e.importExport(t)
	if _, err := e.store.MatchLinkedInConnections(e.as(), e.rep); err != nil {
		t.Fatalf("matching at upload time: %v", err)
	}
	if status, _ := e.ghostStatus(t, "Andreas Müller"); status != "unmatched" {
		t.Fatalf("a ghost matched on an empty workspace: %q", status)
	}

	// Capture then does its work: the account and the contact appear.
	org := e.seedOrgNamed(t, "Acme GmbH")
	andreas := e.seedContact(t, "Andreas Müller")
	e.employ(t, andreas, org)
	dana := e.seedContact(t, "Dana Buyer")
	e.seedEmail(t, dana, "dana@acme.test")
	e.employ(t, dana, org)

	// The sweep runs workspace-wide (the zero owner) and catches both.
	matched, err := e.store.MatchLinkedInConnections(e.as(), ids.Nil)
	if err != nil {
		t.Fatalf("sweeping: %v", err)
	}
	if matched.Confirmed != 1 || matched.Suggested != 1 {
		t.Errorf("the sweep reported %+v, want 1 confirmed and 1 suggested", matched)
	}
	if status, person := e.ghostStatus(t, "Dana Buyer"); status != "confirmed" || person == nil || *person != dana.UUID {
		t.Errorf("the address match is %q → %v, want confirmed → %s", status, person, dana)
	}
	if status, person := e.ghostStatus(t, "Andreas Müller"); status != "suggested" || person == nil || *person != andreas.UUID {
		t.Errorf("the name+employer match is %q → %v, want suggested → %s", status, person, andreas)
	}
}

// The profile is the member's own answer about themselves, and it is theirs
// alone: onboarding records it, the settings tab shows it back, and a
// correction sticks. Nothing here reaches another member's row — there is no
// parameter that could.
func TestAMemberOwnsAndCanCorrectTheirLinkedInProfile(t *testing.T) {
	e := setupDedupe(t)

	// Never asked: not connected, and that is not an error.
	before, err := e.store.GetMyLinkedInAccount(e.as())
	if err != nil {
		t.Fatalf("reading an account that does not exist yet: %v", err)
	}
	if before.ProfileURL != nil || before.ConnectedAt != nil {
		t.Errorf("a member who was never asked reads as %+v, want empty", before)
	}

	// The onboarding act's answer.
	saved, err := e.store.SaveMyLinkedInAccount(e.as(), SaveMyLinkedInAccountInput{
		ProfileURL: "https://www.linkedin.com/in/lars", Connected: true,
	})
	if err != nil {
		t.Fatalf("saving the profile: %v", err)
	}
	if saved.ProfileURL == nil || *saved.ProfileURL != "https://www.linkedin.com/in/lars" {
		t.Errorf("saved profile = %v, want the URL given", saved.ProfileURL)
	}
	if saved.ConnectedAt == nil {
		t.Error("the authorization was not recorded")
	}
	connectedAt := *saved.ConnectedAt

	// A correction from the settings tab. It edits the URL and must NOT
	// revoke the authorization — disconnecting is its own deliberate act.
	fixed, err := e.store.SaveMyLinkedInAccount(e.as(), SaveMyLinkedInAccountInput{
		ProfileURL: "https://www.linkedin.com/in/lars-jankowfsky", Connected: false,
	})
	if err != nil {
		t.Fatalf("correcting the profile: %v", err)
	}
	if fixed.ProfileURL == nil || *fixed.ProfileURL != "https://www.linkedin.com/in/lars-jankowfsky" {
		t.Errorf("corrected profile = %v, want the new URL", fixed.ProfileURL)
	}
	if fixed.ConnectedAt == nil || !fixed.ConnectedAt.Equal(connectedAt) {
		t.Errorf("editing the URL changed the authorization: %v, want it untouched at %v",
			fixed.ConnectedAt, connectedAt)
	}

	// Emptying the field CLEARS it rather than leaving the old value behind.
	cleared, err := e.store.SaveMyLinkedInAccount(e.as(), SaveMyLinkedInAccountInput{})
	if err != nil {
		t.Fatalf("clearing the profile: %v", err)
	}
	if cleared.ProfileURL != nil {
		t.Errorf("clearing left %v behind — a member emptying the field means do not record this", cleared.ProfileURL)
	}

	// A headline is not a URL, and saying so beats storing a broken link.
	if _, err := e.store.SaveMyLinkedInAccount(e.as(), SaveMyLinkedInAccountInput{
		ProfileURL: "Lars Jankowfsky | Founder",
	}); err == nil {
		t.Error("a non-URL profile was accepted")
	}
}

// normalized_company is DERIVED and it is part of the natural dedupe key, so
// changing the normalizer changes the key: the same connection stops matching
// the row it already has, and the next import inserts a second copy.
//
// That is not hypothetical. Cleaning LinkedIn's headline company field
// ("najahak.io | نجاحك" → "najahak.io") re-keyed every row carrying a tagline,
// and re-importing the same export produced 209 duplicates on a real
// workspace — double-counting every org-level reach those rows feed.
//
// The backfill is what makes a normalizer change safe: recompute the stored
// keys, collapse what collides, and keep the row a human has decided on.
func TestRenormalizingCollapsesTheDuplicatesAnOldKeyLeft(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()

	// Two rows for ONE connection: the key an older normalizer stored, and the
	// key today's produces. This is exactly the state a re-import left behind.
	var stale, fresh ids.UUID
	_ = fresh
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		insert := `
			INSERT INTO linkedin_connection
			  (workspace_id, owner_user_id, full_name, normalized_name,
			   company_name, normalized_company, match_status, source)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        $1, 'Abbas Fawaz', 'abbas fawaz', 'najahak.io | Growth',
			        $2, $3, 'csv_export')
			RETURNING id`
		if err := tx.QueryRow(ctx, insert,
			e.rep, "najahak.io | growth", "suggested").Scan(&stale); err != nil {
			return err
		}
		return tx.QueryRow(ctx, insert, e.rep, "najahak.io", "unmatched").Scan(&fresh)
	}); err != nil {
		t.Fatalf("seeding the duplicate pair: %v", err)
	}

	result, err := e.store.RenormalizeLinkedInCompanyKeys(ctx)
	if err != nil {
		t.Fatalf("re-normalizing: %v", err)
	}
	if result.Merged != 1 {
		t.Errorf("merged %d rows, want 1 — the duplicate pair is one connection", result.Merged)
	}

	var rows int
	var survivorStatus string
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM linkedin_connection WHERE normalized_name = 'abbas fawaz'`).
			Scan(&rows); err != nil {
			return err
		}
		return tx.QueryRow(ctx,
			`SELECT match_status FROM linkedin_connection WHERE normalized_name = 'abbas fawaz'`).
			Scan(&survivorStatus)
	}); err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if rows != 1 {
		t.Fatalf("%d rows survive, want 1 — every org reach count these feed is multiplied by the duplicate", rows)
	}
	// The row carrying a human's judgement is the one to keep. Discarding it
	// and keeping the undecided copy would silently re-ask a question somebody
	// already answered.
	if survivorStatus != "suggested" {
		t.Errorf("the survivor is %q, want the row a human had already decided on", survivorStatus)
	}

	// Idempotent: a second pass over a clean workspace changes nothing.
	again, err := e.store.RenormalizeLinkedInCompanyKeys(ctx)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if again.Merged != 0 || again.Rekeyed != 0 {
		t.Errorf("a second pass changed %+v, want nothing — it runs on every boot", again)
	}
}
