// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// corroboratedInput is channelEnsureInput with the address a vouching source
// supplies alongside the account.
func (e *dedupeEnv) corroboratedInput(t *testing.T, ci connector.ChannelIdentity, display, email string) EnsureChannelCounterpartyInput {
	t.Helper()
	in := e.channelEnsureInput(e.asChannelConnector(), t, ci, display)
	in.CorroboratingEmail = email
	return in
}

// The whole point of the change, end to end. A human captured from mail sends a
// direct message; the address routes the ladder onto the record that already
// exists instead of minting a second one for the same person.
//
// Routing alone is not enough, which is why the binding is asserted too: without
// it the person is unreachable for a reply, invisible to a channel-keyed
// erasure, and re-resolved by address on every later message forever.
func TestACorroboratingAddressAdoptsTheMailCapturedIncumbent(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.asChannelConnector()
	const email = "tuyen@acme.example"
	incumbent := e.seedPerson(e.as(), t, "Tuyen Dinh Quang", []string{email}, nil)
	ci := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "990101", Username: "tuyen"}

	res, err := e.store.EnsureChannelCounterparty(ctx, e.corroboratedInput(t, ci, "Tuyen", email))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}

	if res.PersonID != incumbent {
		t.Fatalf("ensure landed on %s, want the mail-captured incumbent %s — one human must not become two records", res.PersonID, incumbent)
	}
	if res.PersonCreated {
		t.Error("a second person was created for a human the address already found")
	}
	if n := e.countInWorkspace(ctx, t,
		`SELECT count(*) FROM person_channel_identity WHERE person_id = $1 AND channel_user_id = $2 AND archived_at IS NULL`,
		incumbent, ci.ChannelUserID); n != 1 {
		t.Fatalf("%d live bindings on the adopted incumbent, want 1 — routing without binding leaves them unreachable", n)
	}
	// The address matched, so it was already there; adopting must not try to
	// write it again and must not claim in the audit trail that it did.
	if n := e.countInWorkspace(ctx, t, `SELECT count(*) FROM person_email WHERE person_id = $1`, incumbent); n != 1 {
		t.Fatalf("%d address rows on the adopted incumbent, want 1", n)
	}
}

// Adoption has to be idempotent for the same reason capture is: the same message
// arrives twice, and the second pass must write nothing rather than fail the
// ensure and log a fault on every poll.
func TestAdoptingTheIncumbentTwiceWritesNothingTheSecondTime(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.asChannelConnector()
	const email = "dung@acme.example"
	incumbent := e.seedPerson(e.as(), t, "Dung Nguyen", []string{email}, nil)
	ci := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "990201", Username: "dung"}

	if _, err := e.store.EnsureChannelCounterparty(ctx, e.corroboratedInput(t, ci, "Dung", email)); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	second, err := e.store.EnsureChannelCounterparty(ctx, e.corroboratedInput(t, ci, "Dung", email))
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}

	if second.PersonID != incumbent || second.PersonCreated {
		t.Fatalf("second ensure = %+v, want the incumbent %s reused", second, incumbent)
	}
	if n := e.countInWorkspace(ctx, t,
		`SELECT count(*) FROM person_channel_identity WHERE person_id = $1 AND archived_at IS NULL`, incumbent); n != 1 {
		t.Fatalf("%d live bindings after a replay, want 1", n)
	}
	if n := e.countInWorkspace(ctx, t, `SELECT count(*) FROM person_email WHERE person_id = $1`, incumbent); n != 1 {
		t.Fatalf("%d address rows after a replay, want 1", n)
	}
}

// A source that vouched for the address and whose provider knew one mints the
// person WITH it. Without this the record is created addressless and the next
// mail from the same human mints a second record — the defect in #1382.
func TestAMintedChannelPersonKeepsTheCorroboratingAddress(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.asChannelConnector()
	const email = "luu@acme.example"
	ci := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "990301", Username: "luu"}

	res, err := e.store.EnsureChannelCounterparty(ctx, e.corroboratedInput(t, ci, "Luu Nguyen Thanh", email))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}

	if !res.PersonCreated {
		t.Fatal("no person was created for an account nothing else knew")
	}
	var got string
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT email FROM person_email WHERE person_id = $1`, res.PersonID).Scan(&got)
	}); err != nil {
		t.Fatalf("reading the minted person's address: %v", err)
	}
	if got != email {
		t.Errorf("stored address %q, want %q", got, email)
	}
}

// A13 on the ADDRESS key. The channel path only ever probed the channel key,
// because it never held an address; once one flows, an erasure keyed on the
// address must still stick — otherwise the subject's next direct message, naming
// them by an account the suppression list never heard of, quietly recreates them.
func TestAnErasedAddressIsNotResurrectedByADirectMessage(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.asChannelConnector()
	const erased = "gone@acme.example"
	ci := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "990401", Username: "gone"}

	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO erasure_suppression (kind, value_hash) VALUES ('email', $1)`,
			storekit.SuppressionHash(erased))
		return err
	}); err != nil {
		t.Fatal(err)
	}

	_, err := e.store.EnsureChannelCounterparty(ctx, e.corroboratedInput(t, ci, "Erased Subject", erased))

	if !errors.Is(err, ErrCounterpartySuppressed) {
		t.Fatalf("ensure of an erased address = %v, want ErrCounterpartySuppressed — deletion sticks on every key", err)
	}
	if n := e.countInWorkspace(ctx, t,
		`SELECT count(*) FROM person_channel_identity WHERE channel_user_id = $1`, ci.ChannelUserID); n != 0 {
		t.Errorf("%d bindings for an erased subject, want 0", n)
	}
}
