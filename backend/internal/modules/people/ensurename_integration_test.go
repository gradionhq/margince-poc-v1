// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// What capture stores as a counterparty's NAME, over a real Postgres.
//
// The unit table in personname_test.go proves the parser reads a header
// correctly. These prove the reading survives the write: that the split columns
// actually land on the row, that a second message completes a record the first
// one left incomplete, and — the one that matters most — that a name a human
// typed is never overwritten by whatever a later mail header happens to spell.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// storedName reads the three name columns back off the row.
func (e *dedupeEnv) storedName(ctx context.Context, t *testing.T, id ids.PersonID) (full string, first, last *string) {
	t.Helper()
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT full_name, first_name, last_name FROM person WHERE id = $1`, id).
			Scan(&full, &first, &last)
	}); err != nil {
		t.Fatalf("reading person %s: %v", id, err)
	}
	return full, first, last
}

// nameOrNull renders a nullable name column for a failure message. The
// package's own deref spells NULL as "", which is exactly the distinction these
// tests are about — a column that says "we do not know" versus one holding an
// empty string.
func nameOrNull(s *string) string {
	if s == nil {
		return "NULL"
	}
	return *s
}

func TestEnsureCounterpartyStoresAParsedName(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()

	// No display name at all: the local part is the only evidence, and it
	// carries a first and a last name.
	res, err := e.store.EnsureCounterparty(ctx, e.ensureInput(ctx, t, "lars.ferner@parsed.test", "", "parsed.test"))
	if err != nil || !res.PersonCreated {
		t.Fatalf("ensure = %+v (err %v), want a created person", res, err)
	}
	full, first, last := e.storedName(ctx, t, res.PersonID)
	if full != "Lars Ferner" || nameOrNull(first) != "Lars" || nameOrNull(last) != "Ferner" {
		t.Fatalf("stored name = %q / %q / %q, want Lars Ferner / Lars / Ferner",
			full, nameOrNull(first), nameOrNull(last))
	}
}

func TestEnsureCounterpartyLeavesSplitNamesNullWhenItCannotTell(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()

	// A lone surname names the person but says nothing about a given name, and
	// a role mailbox names nobody. Both must display honestly and split neither.
	for _, tc := range []struct{ email, wantFull string }{
		{"schluepmann@parsed.test", "Schluepmann"},
		{"mail@parsed.test", "mail"},
	} {
		res, err := e.store.EnsureCounterparty(ctx, e.ensureInput(ctx, t, tc.email, "", "parsed.test"))
		if err != nil {
			t.Fatalf("ensure %s: %v", tc.email, err)
		}
		full, first, last := e.storedName(ctx, t, res.PersonID)
		if full != tc.wantFull {
			t.Errorf("%s full_name = %q, want %q", tc.email, full, tc.wantFull)
		}
		if first != nil || last != nil {
			t.Errorf("%s split names = %q / %q, want both NULL — the local part did not say",
				tc.email, nameOrNull(first), nameOrNull(last))
		}
	}
}

func TestEnsureCounterpartyFillsASplitNameItLearnsLater(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()

	// First contact is from a mailbox whose local part says nothing.
	first, err := e.store.EnsureCounterparty(ctx, e.ensureInput(ctx, t, "hh@later.test", "", "later.test"))
	if err != nil || !first.PersonCreated {
		t.Fatalf("first ensure = %+v (err %v), want a created person", first, err)
	}
	if _, f, l := e.storedName(ctx, t, first.PersonID); f != nil || l != nil {
		t.Fatalf("split names = %q / %q on first contact, want both NULL", nameOrNull(f), nameOrNull(l))
	}

	// The same human writes again, this time with their name in the header.
	second, err := e.store.EnsureCounterparty(ctx, e.ensureInput(ctx, t, "hh@later.test", "Hanna Hoffmann", "later.test"))
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if second.PersonID != first.PersonID {
		t.Fatalf("second ensure minted %s, want the incumbent %s", second.PersonID, first.PersonID)
	}
	_, f, l := e.storedName(ctx, t, first.PersonID)
	if nameOrNull(f) != "Hanna" || nameOrNull(l) != "Hoffmann" {
		t.Fatalf("split names = %q / %q, want Hanna / Hoffmann filled in on the second contact",
			nameOrNull(f), nameOrNull(l))
	}
}

// The guard: an automatic fill may only ever ADD. A name a human typed is the
// authority on that person, and no later header may rewrite it.
func TestEnsureCounterpartyNeverOverwritesANameAHumanSet(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()

	res, err := e.store.EnsureCounterparty(ctx, e.ensureInput(ctx, t, "wrong.spelling@human.test", "", "human.test"))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	// A human corrects the record — the spelling capture derived was wrong.
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE person SET full_name = $2, first_name = $3, last_name = $4 WHERE id = $1`,
			res.PersonID, "Wolfgang Schmitt-Rink", "Wolfgang", "Schmitt-Rink")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// More mail arrives, spelling the name differently. The header parses
	// CONFIDENTLY — an unconfident one would be refused before the write and
	// would prove nothing about the fill guard itself.
	again, err := e.store.EnsureCounterparty(ctx,
		e.ensureInput(ctx, t, "wrong.spelling@human.test", "Wolf Rink", "human.test"))
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if again.PersonID != res.PersonID {
		t.Fatalf("second ensure minted %s, want the incumbent %s", again.PersonID, res.PersonID)
	}
	full, first, last := e.storedName(ctx, t, res.PersonID)
	if full != "Wolfgang Schmitt-Rink" || nameOrNull(first) != "Wolfgang" || nameOrNull(last) != "Schmitt-Rink" {
		t.Fatalf("stored name = %q / %q / %q after later mail, want the human's spelling untouched",
			full, nameOrNull(first), nameOrNull(last))
	}
}

// A message signed only "Lars" from an address the installation KNOWS is not a
// one-token mystery: the workspace already holds that human's full name, typed
// by a person. The header alone would parse to "Lars" with no first or last,
// which is honest but worse than the evidence available.
func TestACounterpartyWhoIsAKnownHumanGetsTheirRealName(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	const addr = "lars@jankowfsky.test"

	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO app_user (id, email, display_name)
			VALUES ($1, $2, 'Lars Jankowfsky')`, ids.NewV7(), addr)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// The installation has written to this address, which is the unforgeable
	// half: without it a spoofed From could claim any colleague's identity.
	e.attestOutbound(ctx, t, addr)

	res, err := e.store.EnsureCounterparty(ctx, e.ensureInput(ctx, t, addr, "Lars", "jankowfsky.test"))
	if err != nil || !res.PersonCreated {
		t.Fatalf("ensure = %+v (err %v), want a created person", res, err)
	}
	full, first, last := e.storedName(ctx, t, res.PersonID)
	if full != "Lars Jankowfsky" || nameOrNull(first) != "Lars" || nameOrNull(last) != "Jankowfsky" {
		t.Fatalf("stored %q / %q / %q, want the name the installation already knew",
			full, nameOrNull(first), nameOrNull(last))
	}
}

// The header still wins when it actually says something. An app_user display
// name can be stale — a maiden name, a placeholder typed at setup — and it must
// not overwrite what a person signs their own mail with.
func TestAConfidentHeaderOutranksAKnownHumansStoredName(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	const addr = "anna@known.test"

	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO app_user (id, email, display_name)
			VALUES ($1, $2, 'Anna Old-Surname')`, ids.NewV7(), addr)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	res, err := e.store.EnsureCounterparty(ctx, e.ensureInput(ctx, t, addr, "Anna Weber", "known.test"))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if full, _, last := e.storedName(ctx, t, res.PersonID); full != "Anna Weber" || nameOrNull(last) != "Weber" {
		t.Fatalf("stored %q / %q, want the name she signs her own mail with", full, nameOrNull(last))
	}
}

// attestOutbound records that this installation provably wrote to an address —
// the T1 evidence, stamped only by a connector reading the owner's own sent
// copy or by the governed send path.
func (e *dedupeEnv) attestOutbound(ctx context.Context, t *testing.T, email string) {
	t.Helper()
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO activity (id, kind, subject, counterparty_email, counterparty_outbound_attested, source, captured_by)
			VALUES ($1, 'email', 'hi', $2, true, 'test', 'human:test')`,
			ids.NewV7(), email)
		return err
	}); err != nil {
		t.Fatalf("attesting outbound to %s: %v", email, err)
	}
}

// A forged From naming a colleague's address must not put that colleague's
// stored name on a stranger's record. The header is forgeable; the installation
// having WRITTEN to the address is not.
func TestASpoofedHeaderCannotBorrowAKnownHumansName(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	const addr = "colleague@elsewhere.test"

	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO app_user (id, email, display_name)
			VALUES ($1, $2, 'Real Colleague')`, ids.NewV7(), addr)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// No attested correspondence: nothing proves this installation ever wrote
	// to the address, so the name stays what the header actually said.
	res, err := e.store.EnsureCounterparty(ctx, e.ensureInput(ctx, t, addr, "Lars", "elsewhere.test"))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	full, first, last := e.storedName(ctx, t, res.PersonID)
	if full != "Lars" || first != nil || last != nil {
		t.Fatalf("stored %q / %q / %q, want the header's own word and no borrowed identity",
			full, nameOrNull(first), nameOrNull(last))
	}
}

// The record a person LOOKS at has to change. A fill that writes the split
// columns and leaves full_name reading "Lars" reports success and changes
// nothing visible.
func TestFillingASplitNameAlsoFixesTheDisplayedName(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	const addr = "lars@displayed.test"

	// First contact: the header says one word, so that is what is stored.
	first, err := e.store.EnsureCounterparty(ctx, e.ensureInput(ctx, t, addr, "Lars", "displayed.test"))
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if full, _, _ := e.storedName(ctx, t, first.PersonID); full != "Lars" {
		t.Fatalf("full_name = %q on first contact, want the header's word", full)
	}

	// A later message carries the whole name.
	if _, err := e.store.EnsureCounterparty(ctx,
		e.ensureInput(ctx, t, addr, "Lars Jankowfsky", "displayed.test")); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	full, f, l := e.storedName(ctx, t, first.PersonID)
	if full != "Lars Jankowfsky" || nameOrNull(f) != "Lars" || nameOrNull(l) != "Jankowfsky" {
		t.Fatalf("stored %q / %q / %q, want the displayed name fixed too",
			full, nameOrNull(f), nameOrNull(l))
	}
}
