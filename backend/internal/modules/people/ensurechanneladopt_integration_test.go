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
	auditsAfterFirst := e.countInWorkspace(ctx, t,
		`SELECT count(*) FROM audit_log WHERE entity_type = $1 AND entity_id = $2`, entityPerson, incumbent)
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
	// An audit row per replayed message would claim a change on every message
	// that changed nothing — the same noise as a duplicate write, in the form
	// nobody notices until they are reading the trail for a reason. Compared
	// against the first ensure rather than a fixed number, so the assertion is
	// about what the REPLAY wrote and not about how the incumbent was seeded.
	if n := e.countInWorkspace(ctx, t,
		`SELECT count(*) FROM audit_log WHERE entity_type = $1 AND entity_id = $2`,
		entityPerson, incumbent); n != auditsAfterFirst {
		t.Errorf("audit rows went %d -> %d across a replay; a message that changed nothing must write nothing", auditsAfterFirst, n)
	}
}

// A source that vouched for the address, whose provider knew one, mints the
// person WITH it. An addressless record is one the next mail from the same human
// cannot match, so it mints a second record.
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

// A13 on the ADDRESS key: an erasure keyed on an address must stick even though
// this path is entered by account. Otherwise the subject's next direct message,
// naming them by an account the suppression list never heard of, quietly
// recreates them.
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

// The one disagreement the resolver cannot report for itself.
//
// This is the LOST BIND RACE, and it is reachable only between two statements:
// the ladder's channel lane reads before a rival's binding exists — so it routes
// on the address alone and reports no conflict — and by the time the bind runs,
// the rival owns the account. The committed state is then two records describing
// one human, and nothing downstream would say so, because compose raises the
// identity review only when a conflict comes back.
//
// The race is driven by calling the adopt step with exactly the resolution the
// ladder returns when only the email lane hit, against a rival binding that has
// since committed. Everything else is the real thing: the real bind path decides
// the winner, and the real database arbitrates.
func TestLosingTheBindRaceStillRaisesTheIdentityReview(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.asChannelConnector()
	const email = "shared@acme.example"
	incumbent := e.seedPerson(e.as(), t, "Mail Captured", []string{email}, nil)
	rival := e.seedPerson(e.as(), t, "Channel Captured", nil, nil)
	ci := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "990501", Username: "shared"}
	// The rival commits its binding in the window the ladder had already read
	// past.
	e.bindIdentity(ctx, t, rival, ci)

	var res EnsureChannelCounterpartyResult
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return e.store.adoptEmailRoutedIncumbent(ctx, tx,
			e.corroboratedInput(t, ci, "Shared", email),
			// What DedupePerson returns when the channel lane missed and the
			// address routed: a collision on the email lane, and no conflict,
			// because at read time no rival binding existed to disagree.
			PersonResolution{
				Decision:    DecisionExactCollision,
				PersonID:    incumbent,
				MatchedLane: LaneEmail,
			}, &res)
	}); err != nil {
		t.Fatalf("adopting the incumbent: %v", err)
	}

	if res.PersonID != rival {
		t.Fatalf("landed on %s, want the account's real owner %s — the binding is the arbiter, not the address", res.PersonID, rival)
	}
	if res.Conflict == nil {
		t.Fatal("no conflict reported — the address names one person and the account another, and nothing downstream would ever surface it")
	}
	if res.Conflict.RoutedTo != rival || res.Conflict.Rival != incumbent {
		t.Errorf("conflict = routed %s / rival %s, want routed %s / rival %s",
			res.Conflict.RoutedTo, res.Conflict.Rival, rival, incumbent)
	}
	if res.Conflict.RoutedLane != laneChannelIdentity || res.Conflict.RivalLane != LaneEmail {
		t.Errorf("conflict lanes = %s/%s, want %s/%s — the binding outranks the address",
			res.Conflict.RoutedLane, res.Conflict.RivalLane, laneChannelIdentity, LaneEmail)
	}
}

// An address a connector's directory vouched for identifies the person, and it
// must not also settle the MAIL ladder's question about that address.
//
// The ladder reads "a live person holds this address" as a verdict of `real`,
// and the noise sweep refuses to touch an address a person holds. That reading
// is sound for correspondence and for a human typing a contact in; it is not
// sound for a stranger who sent one direct message. Left indistinguishable, that
// stranger's address would be auto-created on every later bulk mail and made
// permanently unsweepable — by messaging a member once.
func TestAVouchedAddressIsNotEvidenceOfCorrespondence(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.asChannelConnector()
	const vouched = "stranger@spamco.example"
	ci := connector.ChannelIdentity{Provider: telegramProvider, ChannelUserID: "990601", Username: "stranger"}

	res, err := e.store.EnsureChannelCounterparty(ctx, e.corroboratedInput(t, ci, "A Stranger", vouched))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}

	// Collected: the person is identified by it, which is what it is for.
	if n := e.countInWorkspace(ctx, t,
		`SELECT count(*) FROM person_email WHERE person_id = $1 AND email = $2`, res.PersonID, vouched); n != 1 {
		t.Fatalf("%d address rows on the minted person, want the vouched address collected", n)
	}
	// But not as correspondence, which is what the mail ladder reads.
	if n := e.countInWorkspace(ctx, t,
		`SELECT count(*) FROM person_email WHERE person_id = $1 AND from_correspondence`, res.PersonID); n != 0 {
		t.Errorf("%d of the minted person's addresses count as correspondence — one direct message would settle the mail ladder's verdict on a stranger's address", n)
	}
}

// The control that makes the case above mean something: an address this
// workspace really did correspond with still counts, or the flag would be
// switching the ladder off rather than scoping it.
func TestAMailCapturedAddressStillCountsAsCorrespondence(t *testing.T) {
	e := setupDedupe(t)
	const known = "real.contact@client.io"
	person := e.seedPerson(e.as(), t, "Real Contact", []string{known}, nil)

	if n := e.countInWorkspace(e.as(), t,
		`SELECT count(*) FROM person_email WHERE person_id = $1 AND from_correspondence`, person); n != 1 {
		t.Errorf("%d of a normally created person's addresses count as correspondence, want 1", n)
	}
}
