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
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/capture/gmail"
	"github.com/gradionhq/margince/backend/internal/platform/database"
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
