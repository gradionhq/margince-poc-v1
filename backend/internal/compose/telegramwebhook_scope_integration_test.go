// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// What the ingress webhook must NOT persist, proved against a real
// transaction. Both claims here are about an absence, and an absence is
// exactly what a unit test cannot see: the first is a payload that must never
// reach raw_capture because nothing downstream would ever be able to erase it,
// and the second is a lock that must be held while the row is written because
// an erasure of the same human can otherwise commit around it.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/capture/telegram"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// telegramGroupUpdateBody renders the same message posted in a GROUP: in scope
// for Telegram's delivery, out of scope for this connector (design §1).
func telegramGroupUpdateBody(updateID int64) []byte {
	return []byte(fmt.Sprintf(
		`{"update_id":%d,"message":{"message_id":1,"chat":{"id":-1001,"type":"supergroup"},`+
			`"from":{"id":770002,"username":"grouptalker"},"date":1785000000,"text":"/help"}}`, updateID))
}

// A group message must leave NOTHING behind, and say so with the same 200 a
// captured delivery gets.
//
// Refusing it further down the pipeline — where the ingest worker skips a
// non-private chat — is strictly worse than not refusing it at all: the
// verbatim payload is already in raw_capture by then, holding the sender's
// numeric id, handle, names and every word of the message, and no Person is
// ever minted for it. Every lane that could reach a raw row again drives off
// person_channel_identity (the erasure purge, the subject-access section), and
// raw_capture has no retention sweep, so that payload would outlive every
// erasure request the sender could ever file. Anyone who knows the bot's
// @username can add it to a group.
func TestAGroupMessageIsAcceptedAndPersistsNothing(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)
	vault := keyvault.NewMemory()
	conn, secret := connectTestTelegramBot(t, e, vault, 91000006, "group_scope_bot")

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	inserter, err := jobs.NewInserter(e.Pool, quiet)
	if err != nil {
		t.Fatalf("NewInserter: %v", err)
	}
	srv := httptest.NewServer(newTelegramTestMux(e.Pool, vault, inserter))
	defer srv.Close()

	// The captured delivery first: its answer is the one the refused delivery
	// has to be indistinguishable from, and asserting a fixed 200 instead would
	// not notice the two drifting apart.
	const capturedUpdate, groupUpdate = 5006, 5007
	captured := postTelegramWebhook(t, srv, conn.ID, secret, telegramUpdateBody(capturedUpdate))
	capturedStatus := captured.StatusCode
	if err := captured.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}
	if capturedStatus != http.StatusOK {
		t.Fatalf("the captured delivery status = %d, want 200", capturedStatus)
	}

	group := postTelegramWebhook(t, srv, conn.ID, secret, telegramGroupUpdateBody(groupUpdate))
	groupStatus := group.StatusCode
	if err := group.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}
	if groupStatus != capturedStatus {
		t.Errorf("the group delivery answered %d and the captured one %d — an unauthenticated caller can read which chats this installation accepts",
			groupStatus, capturedStatus)
	}

	if n := e.WsCount(t,
		`SELECT count(*) FROM raw_capture WHERE source_system = 'telegram' AND payload->>'update_id' = $1`,
		fmt.Sprintf("%d", groupUpdate)); n != 0 {
		t.Errorf("%d raw rows for a group message, want 0 — the sender's id, handle, name and words were stored where no erasure can reach them", n)
	}
	if n := riverJobCount(t, e, "telegram_ingest", conn.ID.String()); n != 1 {
		t.Errorf("%d ingest jobs, want 1 — only the private-chat delivery may be queued for capture", n)
	}
}

// lockProbingInserter runs inside the webhook's REAL write transaction (the
// enqueue is the last statement before the commit) and asks, from a SEPARATE
// connection, whether the erasure lock on the delivery's subject is available.
// The webhook's transaction is still open at that moment — it is blocked in
// here — so a lock the webhook took is definitively held, and one it did not
// take is definitively free. No goroutine, no clock.
type lockProbingInserter struct {
	pool    *pgxpool.Pool
	ws      ids.UUID
	subject storekit.ChannelIdentityKey
	other   storekit.ChannelIdentityKey

	subjectFree bool
	otherFree   bool
	probeErr    error
}

func (i *lockProbingInserter) EnqueueTx(_ context.Context, _ pgx.Tx, _ river.JobArgs, _ *river.InsertOpts) error {
	i.subjectFree, i.probeErr = i.lockIsFree(i.subject)
	if i.probeErr != nil {
		return i.probeErr
	}
	i.otherFree, i.probeErr = i.lockIsFree(i.other)
	return i.probeErr
}

// lockIsFree reports whether a second transaction can take the identity's lock
// right now. lock_timeout bounds the wait so a held lock fails instead of
// hanging the test; the answer does not depend on how long it waits, because
// the holder cannot commit until this probe returns.
func (i *lockProbingInserter) lockIsFree(key storekit.ChannelIdentityKey) (bool, error) {
	ctx := principal.WithWorkspaceID(context.Background(), i.ws)
	err := database.WithWorkspaceTx(ctx, i.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '250ms'`); err != nil {
			return err
		}
		return storekit.LockChannelIdentities(ctx, tx, []storekit.ChannelIdentityKey{key})
	})
	if err == nil {
		return true, nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.LockNotAvailable {
		return false, nil
	}
	return false, fmt.Errorf("probing the identity lock for %s: %w", key.ChannelUserID, err)
}

// The webhook must hold the erasure lock on its update's subject while it
// writes, or the raw row it commits can land inside an erasure of that very
// human: READ COMMITTED gives its suppression probe and its insert two
// snapshots, and an erasure that commits between them arms a suppression which
// guarantees person_channel_identity is never recreated — the row every lane
// that could later reach the raw payload drives off.
func TestTheWebhookHoldsTheSubjectsErasureLockWhileItWrites(t *testing.T) {
	e := integration.Setup(t)
	vault := keyvault.NewMemory()
	conn, secret := connectTestTelegramBot(t, e, vault, 91000007, "lock_bot")

	probe := &lockProbingInserter{
		pool:    e.Pool,
		ws:      e.WS,
		subject: storekit.ChannelIdentityKey{Provider: telegram.Provider, ChannelUserID: "770001"},
		other:   storekit.ChannelIdentityKey{Provider: telegram.Provider, ChannelUserID: "770999"},
	}
	srv := httptest.NewServer(newTelegramTestMux(e.Pool, vault, probe))
	defer srv.Close()

	// telegramUpdateBody's sender is 770001 — the account probe.subject names.
	resp := postTelegramWebhook(t, srv, conn.ID, secret, telegramUpdateBody(5008))
	status := resp.StatusCode
	if err := resp.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}
	if probe.probeErr != nil {
		t.Fatalf("the in-transaction lock probe failed: %v", probe.probeErr)
	}
	if status != http.StatusOK {
		t.Fatalf("delivery status = %d, want 200", status)
	}
	if probe.subjectFree {
		t.Error("the sender's erasure lock was free while the webhook was writing their message — an erasure of this human can commit between the suppression probe and the insert")
	}
	if !probe.otherFree {
		t.Error("an unrelated account's lock was held too — the lock must be per identity, or one conversation serializes every other")
	}
}
