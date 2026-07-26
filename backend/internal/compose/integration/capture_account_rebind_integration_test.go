// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// One human, one provider, one row — but not necessarily one mailbox. A person
// who connects a second account over the first is not resuming the first: the
// watermark and the import history belong to the account, and the row is only
// where they happen to live.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// accountBoundConnector names its account from the sealed bundle, so a test
// reconnects "as somebody else" simply by granting different bytes. Its Sync
// yields a watermark, which is what the rebind has to throw away.
type accountBoundConnector struct {
	*pagedConnector
	// duringPage runs where the provider would be answering, so a test can
	// reconnect while a backfill page is out — the race a reauth actually runs.
	duringPage func()
}

func (c *accountBoundConnector) AccountLabel(auth connector.Auth) (string, error) {
	return string(auth), nil
}

func (c *accountBoundConnector) Sync(context.Context, connector.Auth, connector.Cursor, connector.Sink) (connector.Cursor, error) {
	return connector.Cursor(`{"email":"owner@myco.example"}`), nil
}

func (c *accountBoundConnector) BackfillPage(ctx context.Context, auth connector.Auth, after time.Time, pageToken string, sink connector.Sink) (connector.BackfillPageResult, error) {
	if c.duringPage != nil {
		c.duringPage()
	}
	return c.pagedConnector.BackfillPage(ctx, auth, after, pageToken, sink)
}

// startImportReconnectingMidPage opens a run over account, then reconnects as
// reconnectAs while the page is out at the provider, and returns the run's row
// afterwards. It is the whole race in one helper: the only difference between a
// reauth and a rebind is whether the second grant names the same mailbox.
func startImportReconnectingMidPage(t *testing.T, e *searchEnv, account, reconnectAs string) (status string, scanned int, cursor []byte) {
	t.Helper()
	seedCaptureRole(t, e)
	registry := newTestCaptureRegistry(e, newTestKeyvault(t, e))
	fake := &accountBoundConnector{pagedConnector: &pagedConnector{messages: 25, pageSize: 10}}
	registry.Register(fake)
	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})
	if _, err := registry.Connect(grantCtx, "gmail", connector.Auth(account)); err != nil {
		t.Fatalf("connecting %s: %v", account, err)
	}
	run, err := registry.StartBackfill(grantCtx, "gmail", ids.From[ids.UserKind](e.Rep1), 6, 25, enqueueNothing)
	if err != nil {
		t.Fatalf("StartBackfill: %v", err)
	}
	fake.duringPage = func() {
		if _, err := registry.Connect(grantCtx, "gmail", connector.Auth(reconnectAs)); err != nil {
			t.Errorf("mid-page reconnect as %s: %v", reconnectAs, err)
		}
	}
	if _, _, _, err := registry.RunBackfillStep(principal.WithWorkspaceID(context.Background(), e.WS), run.ID); err != nil {
		t.Fatalf("RunBackfillStep across a reconnect: %v, want a clean return", err)
	}
	status, scanned, _, cursor = readBackfillRow(t, e, run.ID)
	return status, scanned, cursor
}

// A reauth is not a rebind. The reauth_required banner asks its human to
// reconnect the SAME mailbox, and the page already out at the provider belongs
// to the same account and the same import it always did — fencing it off would
// report the import the human was told to repair as "cancelled".
func TestAReauthOfTheSameAccountLetsAnInFlightPageCommit(t *testing.T) {
	e := setupSearch(t)
	status, scanned, cursor := startImportReconnectingMidPage(t, e, "same@example.com", "same@example.com")
	if status != "running" {
		t.Fatalf("status = %s, want running — a routine reauth must not end the import it was meant to keep alive", status)
	}
	if scanned != 10 || len(cursor) == 0 {
		t.Fatalf("the page was fenced off by its own human's reauth: scanned=%d cursor=%q", scanned, cursor)
	}
}

// A rebind is the other case, and it must still fence: the page was fetched from
// a mailbox this connection no longer points at, so its counters and its cursor
// are not history the new account gets to inherit.
func TestRebindingToAnotherAccountFencesAnInFlightPage(t *testing.T) {
	e := setupSearch(t)
	status, scanned, cursor := startImportReconnectingMidPage(t, e, "first@example.com", "second@example.com")
	if scanned != 0 || len(cursor) != 0 {
		t.Fatalf("the second mailbox was credited with the first one's page: scanned=%d cursor=%q", scanned, cursor)
	}
	if status != "cancelled" {
		t.Fatalf("status = %s, want cancelled — every remaining page is fenced the same way, and a live run nothing can finish blocks every later start", status)
	}
}

// readConnectionAccount reads what a connection is currently bound to and where
// it is up to.
func readConnectionAccount(t *testing.T, e *searchEnv, connID ids.UUID) (label *string, cursor []byte) {
	t.Helper()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(e.Admin(), `
			SELECT account_label, sync_cursor FROM capture_connection WHERE id = $1`, connID).
			Scan(&label, &cursor)
	})
	if err != nil {
		t.Fatal(err)
	}
	return label, cursor
}

// connectAndSync grants the provider under the given account and pulls once, so
// the connection carries a real watermark before anything reconnects.
func connectAndSync(t *testing.T, registry *capture.Registry, e *searchEnv, account string) ids.UUID {
	t.Helper()
	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})
	connID, err := registry.Connect(grantCtx, "gmail", connector.Auth(account))
	if err != nil {
		t.Fatalf("connecting %s: %v", account, err)
	}
	if err := registry.SyncOnce(principal.WithWorkspaceID(context.Background(), e.WS), connID); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if _, cursor := readConnectionAccount(t, e, connID); len(cursor) == 0 {
		t.Fatal("the fixture precondition does not hold: the first account left no watermark to inherit")
	}
	return connID
}

func TestReconnectingADifferentAccountDropsTheFirstAccountsWatermark(t *testing.T) {
	e := setupSearch(t)
	seedCaptureRole(t, e)
	registry := newTestCaptureRegistry(e, newTestKeyvault(t, e))
	registry.Register(&accountBoundConnector{pagedConnector: &pagedConnector{messages: 5, pageSize: 10}})

	connID := connectAndSync(t, registry, e, "first@example.com")

	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})
	if _, err := registry.Connect(grantCtx, "gmail", connector.Auth("second@example.com")); err != nil {
		t.Fatalf("reconnecting as a different account: %v", err)
	}

	label, cursor := readConnectionAccount(t, e, connID)
	if label == nil || *label != "second@example.com" {
		t.Fatalf("account_label = %v, want second@example.com", label)
	}
	if len(cursor) != 0 {
		t.Fatalf("sync_cursor = %q, want cleared — the second mailbox has never been read, and resuming from the first one's watermark skips everything before it", cursor)
	}
}

func TestReconnectingTheSameAccountKeepsItsWatermark(t *testing.T) {
	e := setupSearch(t)
	seedCaptureRole(t, e)
	registry := newTestCaptureRegistry(e, newTestKeyvault(t, e))
	registry.Register(&accountBoundConnector{pagedConnector: &pagedConnector{messages: 5, pageSize: 10}})

	connID := connectAndSync(t, registry, e, "same@example.com")

	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})
	if _, err := registry.Connect(grantCtx, "gmail", connector.Auth("same@example.com")); err != nil {
		t.Fatalf("re-granting the same account: %v", err)
	}

	if _, cursor := readConnectionAccount(t, e, connID); len(cursor) == 0 {
		t.Fatal("re-granting the same mailbox threw away its watermark — a routine reauth would re-read the whole history")
	}
}

func TestANewAccountMayImportANarrowerWindowThanTheOldOne(t *testing.T) {
	e := setupSearch(t)
	seedCaptureRole(t, e)
	registry := newTestCaptureRegistry(e, newTestKeyvault(t, e))
	registry.Register(&accountBoundConnector{pagedConnector: &pagedConnector{messages: 5, pageSize: 10}})
	rep := ids.From[ids.UserKind](e.Rep1)
	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})
	wsCtx := principal.WithWorkspaceID(context.Background(), e.WS)

	connectAndSync(t, registry, e, "first@example.com")
	wide, err := registry.StartBackfill(grantCtx, "gmail", rep, 12, 5, enqueueNothing)
	if err != nil {
		t.Fatalf("the first account's twelve-month import: %v", err)
	}
	done, _, _, err := registry.RunBackfillStep(wsCtx, wide.ID)
	if err != nil || !done {
		t.Fatalf("paging the first import to completion: done=%v err=%v", done, err)
	}

	if _, err := registry.Connect(grantCtx, "gmail", connector.Auth("second@example.com")); err != nil {
		t.Fatalf("reconnecting as a different account: %v", err)
	}

	// Widen-only protects a mailbox from silently losing history it already
	// imported. The second mailbox has imported nothing, so there is nothing to
	// narrow — refusing here would leave the human with no way to import it at
	// all short of importing a year of a mailbox they just connected.
	if _, err := registry.StartBackfill(grantCtx, "gmail", rep, 3, 5, enqueueNothing); err != nil {
		if errors.Is(err, capture.ErrWindowNarrowing) {
			t.Fatal("the new account inherited the previous account's import window")
		}
		t.Fatalf("the second account's three-month import: %v", err)
	}
}
