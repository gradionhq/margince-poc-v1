// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The Gmail Pub/Sub push resolution path against a stubbed Google: Connect
// stamps account_email from the connector's AccountIdentifier seam, and
// ResolveByAccountEmail finds the connected connection cross-tenant so a push
// can drive SyncOnce (CAP-DDL-2, CAP-WIRE-N-4, ADR-0062).

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
		if err := runner.Stop(stopCtx); err != nil {
			t.Errorf("Stop: %v", err)
		}
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

// pushBodyInt builds the Pub/Sub push envelope body. It is a local copy of
// compose's unexported pushBody helper (gmailpush_test.go) — that helper
// lives in package compose and isn't visible from this integration package.
func pushBodyInt(t *testing.T, email string) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"emailAddress": email})
	if err != nil {
		t.Fatalf("marshal push payload: %v", err)
	}
	env := map[string]any{
		"message":      map[string]any{"data": base64.StdEncoding.EncodeToString(data)},
		"subscription": "projects/p/subscriptions/s",
	}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal push envelope: %v", err)
	}
	return b
}

// TestPushRouteMountedReturns501WhenUnconfigured proves the route survives
// the real mux (session middleware, panic recovery, secure headers) and
// still answers the repo's standard 501 when the server is built with no
// push config — the wired verify→enqueue→sync path is covered by Task 7's
// job test and Task 8's handler unit tests (CAP-WIRE-N-4, ADR-0062).
func TestPushRouteMountedReturns501WhenUnconfigured(t *testing.T) {
	e := setupSearch(t)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := compose.New(e.Pool, quiet) // no compose.WithGmailPush option: unconfigured.
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/hooks/gmail/push", "application/json", bytes.NewReader(pushBodyInt(t, "rep@ws.example")))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotImplemented)
	}
}
