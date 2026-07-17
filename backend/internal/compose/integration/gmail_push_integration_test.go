// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The Gmail Pub/Sub push resolution path against a stubbed Google: Connect
// stamps account_email from the connector's AccountIdentifier seam, and
// ResolveByAccountEmail finds the connected connection cross-tenant so a push
// can drive SyncOnce (CAP-DDL-2, CAP-WIRE-N-4, ADR-0062).

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/capture/gmail"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func connectGmailForPush(t *testing.T, e *searchEnv, owner string) ids.UUID {
	t.Helper()
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
	return connID
}

func readAccountEmail(t *testing.T, e *searchEnv, connID ids.UUID) *string {
	t.Helper()
	var email *string
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT account_email FROM capture_connection WHERE id = $1`, connID).Scan(&email)
	})
	if err != nil {
		t.Fatalf("reading account_email: %v", err)
	}
	return email
}

func TestConnectStampsAccountEmail(t *testing.T) {
	e := setupSearch(t)
	const owner = "rep@ws.example"
	connID := connectGmailForPush(t, e, owner)
	got := readAccountEmail(t, e, connID)
	if got == nil || *got != owner {
		t.Fatalf("account_email = %v, want %q", got, owner)
	}
}

func TestResolveByAccountEmailFindsConnected(t *testing.T) {
	e := setupSearch(t)
	const owner = "rep@ws.example"
	connID := connectGmailForPush(t, e, owner)

	registry := newTestCaptureRegistry(e, newTestKeyvault(t, e))
	due, err := registry.ResolveByAccountEmail(context.Background(), "gmail", owner)
	if err != nil {
		t.Fatalf("ResolveByAccountEmail: %v", err)
	}
	if len(due) != 1 || due[0].ID != connID {
		t.Fatalf("ResolveByAccountEmail = %+v, want the one connection %s", due, connID)
	}

	// An unknown mailbox resolves to nothing.
	none, err := registry.ResolveByAccountEmail(context.Background(), "gmail", "stranger@nowhere.test")
	if err != nil {
		t.Fatalf("ResolveByAccountEmail(unknown): %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("ResolveByAccountEmail(unknown) = %+v, want empty", none)
	}

	// An empty email is a no-op, not a fleet scan.
	empty, err := registry.ResolveByAccountEmail(context.Background(), "gmail", "")
	if err != nil {
		t.Fatalf("ResolveByAccountEmail(empty): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("ResolveByAccountEmail(empty) = %+v, want empty", empty)
	}
}

func countActivities(t *testing.T, e *searchEnv) int {
	t.Helper()
	var n int
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT count(*) FROM activity`).Scan(&n)
	})
	if err != nil {
		t.Fatalf("counting activity: %v", err)
	}
	return n
}

// TestGmailPushJobSyncsAndRedeliveryIsNoop proves the api-enqueues/worker-executes
// split end to end: an inserter (standing in for the push webhook) enqueues a
// GmailPushArgs job, the worker resolves the mailbox and runs SyncOnce, and a
// second, identical push (Pub/Sub's at-least-once redelivery) leaves the
// activity count unchanged — SyncOnce's capture key + cursor make the resync a
// no-op (ADR-0062, CAP-WIRE-N-4).
func TestGmailPushJobSyncsAndRedeliveryIsNoop(t *testing.T) {
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
	if _, err := registry.Connect(grantCtx, "gmail", auth); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner, err := compose.NewJobRunner(e.Pool, quiet, time.Hour, time.Hour, registry, time.Hour, compose.GmailWatchConfig{})
	if err != nil {
		t.Fatalf("NewJobRunner: %v", err)
	}
	sub, cancelSub := runner.SubscribeCompleted()
	defer cancelSub()
	if err := runner.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = runner.Stop(stopCtx)
	}()

	ins, err := jobs.NewInserter(e.Pool, quiet)
	if err != nil {
		t.Fatalf("NewInserter: %v", err)
	}

	// First push: the job runs SyncOnce and captures the stubbed mailbox.
	if err := ins.Insert(context.Background(), compose.GmailPushArgs{EmailAddress: owner}, nil); err != nil {
		t.Fatalf("Insert push job: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	awaitWatchKindCompleted(waitCtx, t, sub, compose.GmailPushArgs{}.Kind())
	after := countActivities(t, e)
	if after == 0 {
		t.Fatalf("first push captured nothing; want >0 activities")
	}

	// Redelivery: a second push over the same window creates no new rows.
	if err := ins.Insert(context.Background(), compose.GmailPushArgs{EmailAddress: owner}, nil); err != nil {
		t.Fatalf("Insert redelivery job: %v", err)
	}
	waitCtx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel2()
	awaitWatchKindCompleted(waitCtx2, t, sub, compose.GmailPushArgs{}.Kind())
	if got := countActivities(t, e); got != after {
		t.Fatalf("redelivery changed activity count: before=%d after=%d (want no-op)", after, got)
	}
}
