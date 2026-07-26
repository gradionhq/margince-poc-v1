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
}

func (c *accountBoundConnector) AccountLabel(auth connector.Auth) (string, error) {
	return string(auth), nil
}

func (c *accountBoundConnector) Sync(context.Context, connector.Auth, connector.Cursor, connector.Sink) (connector.Cursor, error) {
	return connector.Cursor(`{"email":"owner@myco.example"}`), nil
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
