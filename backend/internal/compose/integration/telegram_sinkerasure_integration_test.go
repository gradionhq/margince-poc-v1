// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The mutex between an Art. 17 erasure and the transaction that makes a channel
// record DURABLE — the activity write, not the ingress edge that admitted it.
//
// The ingress edge already took the account's lock, which closes the race for
// raw_capture. It does not close it for the activity: the ingest worker reads
// its raw payload in one transaction and commits the activity in a later one,
// so a whole erasure can run between the two. The activity that then lands
// carries the subject's verbatim message text and their account id, and it is
// reachable by NEITHER erasure selector afterwards — subjectOnlyActivities
// walks activity_link.person_id (the post-commit ensure is refused, so there is
// no link) and unlinkedSubjectMail walks counterparty_email (NULL on a channel
// record). The suppression row then guarantees the identity is never recreated,
// so no later erasure, SAR or retention pass can ever find it again, while the
// erasure's own audit tombstone records a clean scrub.
//
// These cases pin the refusal in Sink.Upsert's own transaction. The lock is
// proved the same way the erasure side is proved next door, in
// channelidentity_erasurelock_integration_test.go: a caller holds the account's
// lock for the whole call and a lock_timeout turns contention into an answer,
// so there is no goroutine, no clock and no ordering to get lucky with.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// sinkConnectorCtx is the principal the ingest worker acts as: a connector
// acting for no human, permitted to create the activity it captures and the
// person that activity names.
func sinkConnectorCtx(e *Env) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalConnector, ID: "connector:telegram",
		Permissions: principal.Permissions{
			RoleKeys: []string{"channel"},
			Objects: map[string]principal.ObjectGrant{
				"activity": {Create: true}, "person": {Create: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})
	return principal.WithCorrelationID(ctx, ids.NewV7())
}

// beyondEveryStatutoryFloor is deliberately ancient, and fixed rather than
// relative to now: the statutory correspondence floor shields any activity whose
// kind is not 'task'/'note' — which includes every channel message — from
// destructive erasure, and the German pack that declares one ships enabled by
// default. A recent occurred_at therefore exercises the FLOOR, not the
// redaction, and an erasure test written against it passes for the wrong reason
// (nothing is redacted, so "the bystander kept their row" is vacuous). The
// floor's own behaviour is pinned separately below.
var beyondEveryStatutoryFloor = time.Date(2005, 1, 1, 12, 0, 0, 0, time.UTC)

// inboundChannelRecord is one normalized inbound Telegram message from account,
// shaped exactly as telegram.Normalize builds it: identified by its channel
// identity, with no address anywhere, and with Raw deliberately empty (the
// connector stores its original at the ingress edge, not here).
func inboundChannelRecord(account, body string) connector.NormalizedRecord {
	return inboundChannelRecordAt(account, body, beyondEveryStatutoryFloor)
}

func inboundChannelRecordAt(account, body string, at time.Time) connector.NormalizedRecord {
	key := "77:" + account + ":9001"
	return connector.NormalizedRecord{
		EntityType: datasource.EntityActivity,
		NaturalKey: connector.NaturalKey{SourceSystem: "telegram", SourceID: key},
		Fields: capture.ActivityFields{
			Kind: "telegram", Body: body, Direction: connector.DirectionInbound,
			OccurredAt: at,
		},
		Source:     "telegram:" + key,
		CapturedBy: "connector:telegram",
		Counterparty: connector.Counterparty{
			Direction:   connector.DirectionInbound,
			DisplayName: "Erased Subject",
			ChannelIdentity: connector.ChannelIdentity{
				Provider: telegramProvider, ChannelUserID: account, Username: "erased",
			},
		},
		ThreadKey: "telegram:77:" + account,
	}
}

// activityBodyCount counts activities holding this exact text, whatever they are
// linked to. It deliberately does NOT join person or activity_link: the whole
// point of the defect is that the surviving row is joined to nothing.
func activityBodyCount(t *testing.T, e *Env, body string) int {
	t.Helper()
	return e.WsCount(t, `SELECT count(*) FROM activity WHERE body = $1`, body)
}

// The P0 itself. A worker that read its raw payload before the erasure must not
// be able to commit the activity after it: the row would outlive the erasure
// that certified it gone, permanently beyond every lane that could remove it.
func TestTheSinkRefusesARecordNamingAnErasedChannelAccount(t *testing.T) {
	e := Setup(t)
	person := e.SeedPerson(t, "Erased Subject", nil)
	seedChannelIdentity(t, e, person, "20301", "erased")

	// A real erasure, so the suppression row is armed the way production arms it.
	if err := privacy.NewEraser(e.Pool).ErasePerson(e.Admin(), person, "test"); err != nil {
		t.Fatalf("ErasePerson: %v", err)
	}

	const body = "the erased subject's message text"
	_, err := capture.NewSink(e.Pool).Upsert(sinkConnectorCtx(e), inboundChannelRecord("20301", body))
	if !errors.Is(err, connector.ErrSkip) {
		t.Fatalf("Upsert returned %v, want ErrSkip — an erased account's message must not become an activity", err)
	}
	if n := activityBodyCount(t, e, body); n != 0 {
		t.Errorf("%d activities hold the erased subject's text; want 0 — and no erasure, SAR or retention lane could reach them", n)
	}
}

// The refusal above has to be taken under the account's lock, or it is only a
// probe: at READ COMMITTED the erasure can commit between this transaction's
// probe and its write, and the activity lands anyway. Holding the lock for the
// whole call proves Upsert waits for it.
func TestTheSinkWaitsForAnErasureHoldingTheRecordsAccount(t *testing.T) {
	e := Setup(t)
	person := e.SeedPerson(t, "Live Subject", nil)
	seedChannelIdentity(t, e, person, "20302", "live")

	sink := capture.NewSink(lockWaitBoundedPool(t))
	ctx := sinkConnectorCtx(e)

	var upsertErr error
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		if err := storekit.LockChannelIdentities(ctx, tx, []storekit.ChannelIdentityKey{
			{Provider: telegramProvider, ChannelUserID: "20302"},
		}); err != nil {
			return err
		}
		_, upsertErr = sink.Upsert(ctx, inboundChannelRecord("20302", "blocked on the erasure"))
		return nil
	}); err != nil {
		t.Fatalf("holding the identity lock: %v", err)
	}

	var pgErr *pgconn.PgError
	if !errors.As(upsertErr, &pgErr) || pgErr.Code != pgerrcode.LockNotAvailable {
		t.Fatalf("Upsert returned %v, want a lock-wait timeout — it did not take the record's account lock, so an erasure can commit inside its transaction", upsertErr)
	}
	if n := activityBodyCount(t, e, "blocked on the erasure"); n != 0 {
		t.Errorf("%d activities were committed although the lock was held; want 0", n)
	}
}

// The negative control that makes the case above mean something: the lock is per
// ACCOUNT, so an erasure of somebody else never delays this capture — and the
// failure above is the lock, not the bounded pool.
func TestTheSinkIsUnaffectedByALockOnAnotherAccount(t *testing.T) {
	e := Setup(t)
	person := e.SeedPerson(t, "Live Subject", nil)
	seedChannelIdentity(t, e, person, "20303", "live")

	sink := capture.NewSink(lockWaitBoundedPool(t))
	ctx := sinkConnectorCtx(e)

	const body = "an unrelated account's message"
	var upsertErr error
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		if err := storekit.LockChannelIdentities(ctx, tx, []storekit.ChannelIdentityKey{
			{Provider: telegramProvider, ChannelUserID: "20999"},
		}); err != nil {
			return err
		}
		_, upsertErr = sink.Upsert(ctx, inboundChannelRecord("20303", body))
		return nil
	}); err != nil {
		t.Fatalf("holding an unrelated identity lock: %v", err)
	}
	if upsertErr != nil {
		t.Fatalf("Upsert: %v — an unrelated account's erasure must not block this capture", upsertErr)
	}
	if n := activityBodyCount(t, e, body); n != 1 {
		t.Errorf("got %d activities, want 1 — the capture should have landed untouched", n)
	}
}

// A counterparty is named by an address OR by a channel identity. Classifying a
// record carrying both as mail binds no channel identity and, because every mail
// gate keys off the address, records no fault either — the one capture outcome
// with no breadcrumb. No in-tree producer emits this shape, so these two cases
// are the only thing holding the gate up and they assert the specific refusal:
// "some error occurred" would keep passing if the gate were deleted and an
// unrelated fault took its place.
func TestTheSinkRefusesACounterpartyNamedTwice(t *testing.T) {
	e := Setup(t)

	rec := inboundChannelRecord("20304", "named twice")
	rec.Counterparty.Email = "someone@example.com"

	_, err := capture.NewSink(e.Pool).Upsert(sinkConnectorCtx(e), rec)
	if !errors.Is(err, capture.ErrCounterpartyNamedTwice) {
		t.Fatalf("Upsert returned %v, want ErrCounterpartyNamedTwice", err)
	}
	if n := activityBodyCount(t, e, "named twice"); n != 0 {
		t.Errorf("%d activities were committed for a malformed counterparty; want 0", n)
	}
}

// The residual half of the P0. The Sink commits the activity in ONE transaction
// and people's ensure writes the person link in a LATER one, so an erasure
// landing in that gap leaves an activity linked to nobody — invisible to the
// link-walking selector — and with no counterparty_email, so invisible to the
// mail selector too. Before the channel selector arm existed, that row survived
// every erasure, subject-access and retention pass forever.
//
// This test builds exactly that state: a Sink with no channel ensurer wired
// never writes the link at all, which is the same row the race produces.
func TestAnErasureReachesAChannelActivityWithNoPersonLink(t *testing.T) {
	e := Setup(t)
	person := e.SeedPerson(t, "Orphaned Subject", nil)
	seedChannelIdentity(t, e, person, "20401", "orphaned")

	const body = "a message no link points at"
	if _, err := capture.NewSink(e.Pool).Upsert(sinkConnectorCtx(e), inboundChannelRecord("20401", body)); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM activity_link WHERE person_id = $1`, person); n != 0 {
		t.Fatalf("the fixture linked the activity to the person (%d links); this test needs the UNLINKED state", n)
	}

	if err := privacy.NewEraser(e.Pool).ErasePerson(e.Admin(), person, "test"); err != nil {
		t.Fatalf("ErasePerson: %v", err)
	}

	if n := activityBodyCount(t, e, body); n != 0 {
		t.Errorf("%d unlinked channel activities kept the erased subject's text; want 0", n)
	}
	// And the identifier itself: source_id is botID:accountID:messageID and
	// thread_key is provider:botID:accountID, so both name the human directly.
	// Emptying subject/body/raw never touched either — a silent Art. 17 hole on
	// the ORDINARY path, with no race involved.
	if n := e.WsCount(t,
		`SELECT count(*) FROM activity WHERE source_id LIKE '%20401%' OR thread_key LIKE '%20401%'`); n != 0 {
		t.Errorf("%d activities still carry the erased subject's account id in source_id/thread_key; want 0", n)
	}
}

// The guard that makes the arm above safe. An account id is a numeric string,
// so a match that ignored the provider — or that compared the id anywhere in the
// row — would redact other people's timelines. This pins that erasing one
// subject touches only their own account's rows.
func TestAnErasureLeavesAnotherAccountsChannelActivityUntouched(t *testing.T) {
	e := Setup(t)
	erased := e.SeedPerson(t, "Erased Subject", nil)
	seedChannelIdentity(t, e, erased, "20501", "erased")
	bystander := e.SeedPerson(t, "Bystander", nil)
	seedChannelIdentity(t, e, bystander, "20502", "bystander")

	sink := capture.NewSink(e.Pool)
	ctx := sinkConnectorCtx(e)
	const bystanderBody = "the bystander's own message"
	if _, err := sink.Upsert(ctx, inboundChannelRecord("20501", "the erased subject's message")); err != nil {
		t.Fatalf("Upsert (subject): %v", err)
	}
	if _, err := sink.Upsert(ctx, inboundChannelRecord("20502", bystanderBody)); err != nil {
		t.Fatalf("Upsert (bystander): %v", err)
	}

	if err := privacy.NewEraser(e.Pool).ErasePerson(e.Admin(), erased, "test"); err != nil {
		t.Fatalf("ErasePerson: %v", err)
	}

	if n := activityBodyCount(t, e, bystanderBody); n != 1 {
		t.Fatalf("got %d bystander activities, want 1 — erasing one subject redacted another's timeline", n)
	}
	if n := e.WsCount(t,
		`SELECT count(*) FROM activity WHERE thread_key LIKE '%20502%'`); n != 1 {
		t.Errorf("the bystander's thread_key was cleared by someone else's erasure (%d rows retain it, want 1)", n)
	}
}

// The statutory floor outranks Art. 17 for recent correspondence, and it applies
// to channel messages too: the predicate shields every activity whose kind is
// not 'task'/'note', and a Telegram message qualifies. So a RECENT channel
// message from an erased subject is retained verbatim, account id included,
// until the floor lapses.
//
// That is the shipped GoBD posture (F-012 refuses a floor bypass), not an
// oversight — and it is pinned here precisely because the arm above looks like
// it should redact everything. Anyone who "fixes" this test by widening the
// redaction has written a floor bypass. If the retention of channel messages
// under a commercial-correspondence floor is wrong, that is a product and legal
// question for the spec, not a change to make here.
func TestARecentChannelMessageIsShieldedFromErasureByTheStatutoryFloor(t *testing.T) {
	e := Setup(t)
	person := e.SeedPerson(t, "Recent Subject", nil)
	seedChannelIdentity(t, e, person, "20601", "recent")

	const body = "a message inside the statutory floor"
	rec := inboundChannelRecordAt("20601", body, time.Now().UTC())
	if _, err := capture.NewSink(e.Pool).Upsert(sinkConnectorCtx(e), rec); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := privacy.NewEraser(e.Pool).ErasePerson(e.Admin(), person, "test"); err != nil {
		t.Fatalf("ErasePerson: %v", err)
	}
	if n := activityBodyCount(t, e, body); n != 1 {
		t.Errorf("got %d retained activities, want 1 — the statutory correspondence floor must outrank the erasure for a recent message", n)
	}
}

// Half a channel identity is refused too, and for a sharper reason than symmetry:
// Provider is hashed into both the advisory lock key and the suppression key, so
// a provider-less identity would lock and probe a key space the eraser never
// touches — the erasure gate would pass while an erasure was mid-purge.
func TestTheSinkRefusesHalfAChannelIdentity(t *testing.T) {
	e := Setup(t)

	rec := inboundChannelRecord("20305", "half an identity")
	rec.Counterparty.ChannelIdentity.Provider = ""

	_, err := capture.NewSink(e.Pool).Upsert(sinkConnectorCtx(e), rec)
	if !errors.Is(err, capture.ErrChannelIdentityIncomplete) {
		t.Fatalf("Upsert returned %v, want ErrChannelIdentityIncomplete", err)
	}
	if n := activityBodyCount(t, e, "half an identity"); n != 0 {
		t.Errorf("%d activities were committed for an unqualified channel identity; want 0", n)
	}
}
