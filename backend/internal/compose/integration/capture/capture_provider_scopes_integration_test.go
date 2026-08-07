// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package capture

// Two scope vocabularies, two columns. capture_connection.scopes is this
// system's internal permission set, frozen at grant time; provider_scopes is
// what the provider says it granted, in the provider's own words. The list
// surface serves the second — that is the question a human asking "what can
// this connection do to my mailbox?" is actually asking — and a connection
// whose connector cannot report it serves absence, never an empty grant.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	capturemod "github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// grantingFake is a connector that can report its provider grant — the shape
// every OAuth connector has after consent.
type grantingFake struct {
	scopeFake
	granted []string
}

func (f *grantingFake) GrantedScopes(connector.Auth) ([]string, error) { return f.granted, nil }

func TestConnectPersistsTheProviderGrantedScopes(t *testing.T) {
	e := integration.SetupSearch(t)
	granted := []string{"offline_access", "User.Read", "Mail.Read"}
	registry := capturemod.NewRegistry(e.Pool, capturemod.NewSink(e.Pool), fakeAuthority{}, newTestKeyvault(t, e))
	registry.Register(&grantingFake{granted: granted})

	grantCtx := humanWithScopes(e, e.Rep1, []principal.Scope{principal.ScopeRead})
	connID, err := registry.Connect(grantCtx, "graph", connector.Auth("token"))
	if err != nil {
		t.Fatal(err)
	}

	var internal, provider []string
	if err := database.WithWorkspaceTx(grantCtx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT scopes, provider_scopes FROM capture_connection WHERE id = $1`, connID).Scan(&internal, &provider)
	}); err != nil {
		t.Fatal(err)
	}
	if len(internal) != 1 || internal[0] != string(principal.ScopeRead) {
		t.Errorf("the internal scopes column = %v, want the unchanged [read]", internal)
	}
	if len(provider) != len(granted) {
		t.Fatalf("provider_scopes = %v, want %v", provider, granted)
	}
	for i, want := range granted {
		if provider[i] != want {
			t.Errorf("provider_scopes[%d] = %q, want %q", i, provider[i], want)
		}
	}

	views, err := registry.Connections(grantCtx)
	if err != nil || len(views) != 1 {
		t.Fatalf("Connections = %+v err=%v, want one row", views, err)
	}
	if len(views[0].ProviderScopes) != len(granted) {
		t.Errorf("the list surface served %v, want the provider grant %v", views[0].ProviderScopes, granted)
	}
}

func TestAConnectorThatCannotReportItsGrantRecordsNoClaim(t *testing.T) {
	e := integration.SetupSearch(t)
	registry := capturemod.NewRegistry(e.Pool, capturemod.NewSink(e.Pool), fakeAuthority{}, newTestKeyvault(t, e))
	registry.Register(&mailFake{})

	grantCtx := humanWithScopes(e, e.Rep1, []principal.Scope{principal.ScopeRead})
	connID, err := registry.Connect(grantCtx, "graph", connector.Auth("token"))
	if err != nil {
		t.Fatal(err)
	}
	var provider []string
	if err := database.WithWorkspaceTx(grantCtx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT provider_scopes FROM capture_connection WHERE id = $1`, connID).Scan(&provider)
	}); err != nil {
		t.Fatal(err)
	}
	if provider != nil {
		t.Errorf("provider_scopes = %v, want NULL — unknown must not read as an empty grant", provider)
	}
}
