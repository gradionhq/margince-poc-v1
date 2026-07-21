// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The Gmail push-watch lifecycle against a stubbed Google: the registry's
// fleet-wide DueWatches scan selects a connection whose watch is missing or
// nearing expiry, and RenewWatch registers a users.watch and stores the
// returned expiration in capture_connection.watch_expires_at (CAP-DDL-2). It
// asserts the two boundary decisions of the slice: the renewal writes the
// deadline the scan keys on, and it does NOT disturb the sync_cursor (which
// SyncOnce owns — anchoring it from the watch would suppress the first-sync
// backfill).

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/capture/gmail"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

const gmailPushTopic = "projects/margince/topics/gmail-push"

func TestGmailWatchRegistersRenewsAndLeavesCursor(t *testing.T) {
	e := setupSearch(t)
	const owner = "rep@ws.example"
	stub := gmailStub(t, owner)

	oauth := gmail.NewOAuth(gmail.OAuthConfig{ClientID: "cid", ClientSecret: "sec", TokenURL: stub.URL + "/token"})
	api := gmail.NewAPI(stub.Client(), stub.URL)

	registry := newTestCaptureRegistry(e, newTestKeyvault(t, e))
	registry.Register(gmail.New(oauth, api))

	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})

	authReq, err := gmail.AuthRequestFrom("the-code", "https://app.test/v1/connectors/gmail/callback")
	if err != nil {
		t.Fatal(err)
	}
	auth, err := gmail.New(oauth, api).Authenticate(context.Background(), authReq)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	connID, err := registry.Connect(grantCtx, "gmail", auth)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// A sync first, so the connection carries a real cursor the watch must not
	// clobber.
	if err := registry.SyncOnce(grantCtx, connID); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	cursorBefore := readCursor(t, e, connID)
	if len(cursorBefore) == 0 {
		t.Fatalf("precondition: expected a cursor after the first sync, got none")
	}

	// A just-connected connection has watch_expires_at NULL, so it is due for
	// an initial watch regardless of the renewal threshold.
	due, err := registry.DueWatches(context.Background(), "gmail", 48*time.Hour)
	if err != nil {
		t.Fatalf("DueWatches: %v", err)
	}
	if len(due) != 1 || due[0].ID != connID {
		t.Fatalf("DueWatches (never-watched) = %+v, want the one connection %s", due, connID)
	}

	// Register the watch: it must store the expiration and leave the cursor.
	if err := registry.RenewWatch(grantCtx, connID, gmailPushTopic); err != nil {
		t.Fatalf("RenewWatch: %v", err)
	}
	exp := readWatchExpiry(t, e, connID)
	if exp == nil {
		t.Fatalf("watch_expires_at was not stored")
	}
	if !exp.After(time.Now()) {
		t.Errorf("watch_expires_at = %v, want a future deadline", exp)
	}
	if got := readCursor(t, e, connID); string(got) != string(cursorBefore) {
		t.Errorf("sync_cursor changed by the watch: before=%q after=%q (watch must not touch the cursor)", cursorBefore, got)
	}

	// A watch renewed 7 days out is no longer within a 48h renewal window.
	due2, err := registry.DueWatches(context.Background(), "gmail", 48*time.Hour)
	if err != nil {
		t.Fatalf("DueWatches after renewal: %v", err)
	}
	if len(due2) != 0 {
		t.Fatalf("DueWatches after renewal = %+v, want empty (watch not near expiry)", due2)
	}

	// A disconnected connection is never selected (the scan mirrors the poll).
	if err := registry.Disconnect(grantCtx, "gmail"); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	d, err := registry.DueWatches(context.Background(), "gmail", 365*24*time.Hour)
	if err != nil {
		t.Fatalf("DueWatches after disconnect: %v", err)
	}
	if len(d) != 0 {
		t.Fatalf("DueWatches after disconnect = %+v, want empty", d)
	}
}

// TestGmailWatchJobRenewsOnSchedule proves the wiring end to end: booting the
// worker's River job runner with a Pub/Sub topic configured registers/renews
// the watch on RunOnStart and writes watch_expires_at — the scheduled path,
// not a direct RenewWatch call.
func TestGmailWatchJobRenewsOnSchedule(t *testing.T) {
	e := setupSearch(t)
	applyRiverSchema(t)
	const owner = "rep@ws.example"
	stub := gmailStub(t, owner)

	oauth := gmail.NewOAuth(gmail.OAuthConfig{ClientID: "cid", ClientSecret: "sec", TokenURL: stub.URL + "/token"})
	api := gmail.NewAPI(stub.Client(), stub.URL)

	registry := newTestCaptureRegistry(e, newTestKeyvault(t, e))
	registry.Register(gmail.New(oauth, api))

	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})
	authReq, err := gmail.AuthRequestFrom("the-code", "https://app.test/v1/connectors/gmail/callback")
	if err != nil {
		t.Fatal(err)
	}
	auth, err := gmail.New(oauth, api).Authenticate(context.Background(), authReq)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	connID, err := registry.Connect(grantCtx, "gmail", auth)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner, err := compose.NewJobRunner(e.Pool, quiet, compose.JobRunnerConfig{
		CloseDateInterval: time.Hour,
		ReconcileInterval: time.Hour,
		TimeScanInterval:  time.Hour,
		GmailRegistry:     registry,
		GmailWatch:        compose.GmailWatchConfig{Topic: gmailPushTopic, Interval: time.Hour, RenewWithin: 48 * time.Hour},
	})
	if err != nil {
		t.Fatalf("NewJobRunner: %v", err)
	}
	sub, cancelSub := runner.SubscribeCompleted()
	defer cancelSub()

	ctx := context.Background()
	if err := runner.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := runner.Stop(stopCtx); err != nil {
			t.Errorf("Stop: %v", err)
		}
	}()

	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	awaitWatchKindCompleted(waitCtx, t, sub, compose.GmailWatchArgs{}.Kind())

	if exp := readWatchExpiry(t, e, connID); exp == nil || !exp.After(time.Now()) {
		t.Fatalf("scheduled watch job did not set a future watch_expires_at: %v", exp)
	}
}

// awaitWatchKindCompleted blocks until a job of the given kind reports
// completion, or the context deadline fires. No polling, no sleep.
func awaitWatchKindCompleted(ctx context.Context, t *testing.T, sub <-chan *river.Event, kind string) {
	t.Helper()
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %q to complete: %v", kind, ctx.Err())
		case ev := <-sub:
			if ev != nil && ev.Job != nil && ev.Job.Kind == kind {
				return
			}
		}
	}
}

func readCursor(t *testing.T, e *searchEnv, connID ids.UUID) []byte {
	t.Helper()
	var cursor []byte
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT sync_cursor FROM capture_connection WHERE id = $1`, connID).Scan(&cursor)
	})
	if err != nil {
		t.Fatalf("reading sync_cursor: %v", err)
	}
	return cursor
}

func readWatchExpiry(t *testing.T, e *searchEnv, connID ids.UUID) *time.Time {
	t.Helper()
	var exp *time.Time
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT watch_expires_at FROM capture_connection WHERE id = $1`, connID).Scan(&exp)
	})
	if err != nil {
		t.Fatalf("reading watch_expires_at: %v", err)
	}
	return exp
}
