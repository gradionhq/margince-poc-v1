// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The mutex between an Art. 17 erasure and an inbound message from the very
// account being erased. Both sides must take the same per-account lock or the
// two overlap: Postgres runs them at READ COMMITTED, so a delivery that probes
// the suppression list, finds nothing, and then writes can have the whole
// erasure commit between its two statements — leaving a verbatim payload
// naming a subject whose person_channel_identity rows, the only handle every
// later erasure and subject-access lane has on raw_capture, are gone for good.
//
// The erasure side is the half proved here; compose's own ingress suite proves
// the delivery side takes the same lock.

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// lockWaitBoundedPool is a second pool whose statements refuse to WAIT for a
// lock. It makes a contended lock decide the outcome instead of the clock: a
// caller that takes the lock under test fails immediately rather than blocking
// the test for as long as the holder lives, and a caller that never takes it
// never waits at all. The bound is a failure guard, not a race — the holding
// transaction below stays open across the whole call.
func lockWaitBoundedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(os.Getenv("MARGINCE_TEST_APP_DSN"))
	if err != nil {
		t.Fatalf("parsing the app DSN: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["lock_timeout"] = "250ms"
	cfg.AfterConnect = func(_ context.Context, conn *pgx.Conn) error {
		database.RegisterIDTypes(conn)
		return nil
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("opening the lock-bounded pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// eraseWhileAccountIsLocked runs a whole erasure INSIDE another transaction
// that holds one account's identity lock, and reports what the erasure did.
// The holder is the caller's own transaction, so the lock is provably held for
// the entire erasure — no goroutine, no clock, no ordering to get lucky with.
func eraseWhileAccountIsLocked(t *testing.T, e *Env, person ids.UUID, lockedAccount string) error {
	t.Helper()
	eraser := privacy.NewEraser(lockWaitBoundedPool(t))
	admin := e.Admin()
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	var eraseErr error
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		if err := storekit.LockChannelIdentities(ctx, tx, []storekit.ChannelIdentityKey{
			{Provider: telegramProvider, ChannelUserID: lockedAccount},
		}); err != nil {
			return err
		}
		eraseErr = eraser.ErasePerson(admin, person, "test")
		return nil
	}); err != nil {
		t.Fatalf("holding the identity lock: %v", err)
	}
	return eraseErr
}

// The erasure must take the SAME per-account lock the ingest path takes, or the
// two can overlap: an inbound message from this very subject can commit its
// verbatim payload after the erasure has already purged raw_capture and armed
// the suppression that guarantees person_channel_identity is never recreated —
// and every lane that could reach that payload again (this file's own erasure
// purge, the Art. 15 raw section) drives off exactly those rows.
//
// The erasure holds object-store I/O and a raw_capture scan inside its
// transaction, so the overlap is a window of seconds, not microseconds, for a
// subject who keeps messaging while their own erasure runs.
func TestErasureWaitsForAnInFlightDeliveryOfTheSubjectsAccount(t *testing.T) {
	e := Setup(t)
	person := e.SeedPerson(t, "Locked Subject", nil)
	seedChannelIdentity(t, e, person, "10108", "locked")

	err := eraseWhileAccountIsLocked(t, e, person, "10108")
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != pgerrcode.LockNotAvailable {
		t.Fatalf("ErasePerson returned %v, want a lock-wait timeout — it did not take the subject's identity lock, so a delivery of their own account can commit inside the erasure", err)
	}
	if !liveIdentityExists(t, e, "10108") {
		t.Error("the identity is gone although the erasure failed — a refused erasure must leave nothing half-done")
	}
	if suppressed(t, e, "10108") {
		t.Error("the account was suppressed although the erasure failed")
	}
}

// The negative control that makes the case above mean something: the lock is
// per ACCOUNT, so a delivery for someone else never delays an erasure — and the
// failure above is the lock, not the bounded pool.
func TestErasureIsUnaffectedByALockOnAnotherAccount(t *testing.T) {
	e := Setup(t)
	person := e.SeedPerson(t, "Unrelated Subject", nil)
	seedChannelIdentity(t, e, person, "10109", "unrelated")

	if err := eraseWhileAccountIsLocked(t, e, person, "10110"); err != nil {
		t.Fatalf("ErasePerson: %v — an unrelated account's delivery must not block an erasure", err)
	}
	if liveIdentityExists(t, e, "10109") {
		t.Error("the erasure reported success but left the identity behind")
	}
	if !suppressed(t, e, "10109") {
		t.Error("the erased account is not suppressed")
	}
}

// liveIdentityExists asks whether one provider account is still bound to
// anybody at all — the state an erasure must leave empty.
func liveIdentityExists(t *testing.T, e *Env, channelUserID string) bool {
	t.Helper()
	return e.WsCount(t,
		`SELECT count(*) FROM person_channel_identity WHERE provider = $1 AND channel_user_id = $2`,
		telegramProvider, channelUserID) > 0
}
