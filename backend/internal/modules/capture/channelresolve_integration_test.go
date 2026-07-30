// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package capture_test

// ResolveChannelConnection (telegram-oa design §6.1): the ingress webhook's
// only lever to find its workspace before it has a session. Both invariants
// under test are fleet-probe facts a mock pool could only pretend to have —
// which workspace a global id actually resolves under, and that a
// pending/disconnected/archived row reads identically to "no such
// connection" — so this runs on a real migrated Postgres, over the same
// fixture the connect suite already establishes.

import (
	"context"
	"fmt"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// A connected row must resolve to the workspace that actually owns it, with
// its webhook secret ref carried along — the one thing List/Get never
// expose, and the one thing the webhook's secret comparison cannot do
// without.
func TestResolveChannelConnectionFindsTheOwningWorkspace(t *testing.T) {
	f := newChannelFixture(t, nil)
	token, botID := f.api.withNewBot("resolvable_bot")

	conn, err := f.store.Connect(f.ctx, capture.ConnectRequest{
		Provider: capture.ProviderTelegram, BotToken: token,
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	_, pool := setupCaptureDB(t)
	resolved, ok, err := capture.ResolveChannelConnection(context.Background(), pool, conn.ID)
	if err != nil {
		t.Fatalf("ResolveChannelConnection: %v", err)
	}
	if !ok {
		t.Fatal("ResolveChannelConnection reported not-found for a live connected row")
	}
	if resolved.WorkspaceID != f.ws {
		t.Fatalf("resolved workspace = %s, want the owning workspace %s", resolved.WorkspaceID, f.ws)
	}
	if resolved.ID != conn.ID {
		t.Fatalf("resolved id = %s, want %s", resolved.ID, conn.ID)
	}
	wantChannelID := fmt.Sprintf("%d", botID)
	if resolved.ChannelID != wantChannelID {
		t.Fatalf("resolved channel id = %q, want %q", resolved.ChannelID, wantChannelID)
	}
	if resolved.WebhookSecretRef == "" {
		t.Fatal("resolved connection carries no webhook secret ref — the ingress secret compare cannot unseal anything")
	}
	_, wantSecretRef := f.vaultRefs(t, conn.ID)
	if resolved.WebhookSecretRef != wantSecretRef {
		t.Fatalf("resolved webhook secret ref = %q, want %q (the row's own ref)", resolved.WebhookSecretRef, wantSecretRef)
	}
}

// A connection nobody has connected resolves to nothing — the baseline an
// attacker probing ids over the unauthenticated webhook must see for every
// state, connected or not.
func TestResolveChannelConnectionUnknownIDIsNotFound(t *testing.T) {
	_, pool := setupCaptureDB(t)
	_, ok, err := capture.ResolveChannelConnection(context.Background(), pool, ids.NewV7())
	if err != nil {
		t.Fatalf("ResolveChannelConnection: %v", err)
	}
	if ok {
		t.Fatal("ResolveChannelConnection matched an id nobody connected")
	}
}

// pending (registration not yet confirmed) and disconnected (archived on
// withdrawal) must both read identically to "not found" — Disconnect
// archives, and ingress must not distinguish "wrong state" from "no such
// connection" over an edge attackers can probe.
func TestResolveChannelConnectionIgnoresPendingAndDisconnected(t *testing.T) {
	_, pool := setupCaptureDB(t)

	t.Run("pending", func(t *testing.T) {
		f := newChannelFixture(t, nil)
		token, _ := f.api.withNewBot("pending_bot")
		conn, err := f.store.Connect(f.ctx, capture.ConnectRequest{
			Provider: capture.ProviderTelegram, BotToken: token,
		})
		if err != nil {
			t.Fatalf("Connect: %v", err)
		}
		owner, _ := setupCaptureDB(t)
		if _, err := owner.Exec(context.Background(),
			`UPDATE channel_connection SET status = 'pending' WHERE id = $1`, conn.ID); err != nil {
			t.Fatalf("forcing status back to pending: %v", err)
		}

		_, ok, err := capture.ResolveChannelConnection(context.Background(), pool, conn.ID)
		if err != nil {
			t.Fatalf("ResolveChannelConnection: %v", err)
		}
		if ok {
			t.Fatal("ResolveChannelConnection matched a pending (not yet confirmed) connection")
		}
	})

	t.Run("disconnected", func(t *testing.T) {
		f := newChannelFixture(t, nil)
		token, _ := f.api.withNewBot("disconnected_bot")
		conn, err := f.store.Connect(f.ctx, capture.ConnectRequest{
			Provider: capture.ProviderTelegram, BotToken: token,
		})
		if err != nil {
			t.Fatalf("Connect: %v", err)
		}
		if err := f.store.Disconnect(f.ctx, conn.ID); err != nil {
			t.Fatalf("Disconnect: %v", err)
		}

		_, ok, err := capture.ResolveChannelConnection(context.Background(), pool, conn.ID)
		if err != nil {
			t.Fatalf("ResolveChannelConnection: %v", err)
		}
		if ok {
			t.Fatal("ResolveChannelConnection matched a disconnected (archived) connection")
		}
	})
}
