// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package capture

// The workspace mail-sharing posture (capture.mail_sharing, ON by default):
// switched OFF, an email captured from then on is born participants-only —
// held to the people on the message and the capturing mailbox owner — while
// already-captured mail keeps the audience it has. The setting moves the
// default for NEW mail; it rewrites no history.

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration"
	capturemod "github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

func TestMailSharingOffHoldsNewMailToItsParticipants(t *testing.T) {
	e := integration.SetupSearch(t)
	personID := e.SeedID(t, `INSERT INTO person (id, full_name, source, captured_by) VALUES ($1, 'Inbox Sender', 'manual', 'human:x')`)
	registry := newTestCaptureRegistry(e, newTestKeyvault(t, e))
	fake := &mailFake{linkTo: personID}
	registry.Register(fake)
	grantCtx := humanWithScopes(e, e.Rep1, []principal.Scope{principal.ScopeRead})
	connID, err := registry.Connect(grantCtx, "graph", connector.Auth("token"))
	if err != nil {
		t.Fatal(err)
	}

	audienceOf := func(sourceID string) string {
		t.Helper()
		var audience string
		if err := e.Owner.QueryRow(context.Background(),
			`SELECT audience FROM activity WHERE source_system = 'graph' AND source_id = $1`, sourceID).Scan(&audience); err != nil {
			t.Fatal(err)
		}
		return audience
	}

	// The default: sharing ON, the captured email is workspace-readable.
	if err := registry.SyncOnce(grantCtx, connID); err != nil {
		t.Fatal(err)
	}
	if got := audienceOf("msg-1"); got != "workspace" {
		t.Fatalf("under the default posture the captured email's audience = %q, want workspace", got)
	}

	// An admin switches sharing off through the real settings store.
	adminCtx := principal.WithWorkspaceID(context.Background(), e.WS)
	adminCtx = principal.WithActor(adminCtx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + e.Rep1.String(), UserID: e.Rep1,
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{"capture_settings": {Read: true, Update: true}},
			RowScope: principal.RowScopeAll,
		},
	})
	store := capturemod.NewSettings(compose.NewSettingsStore(e.Pool))
	off := false
	if _, err := store.Update(adminCtx, nil, &off); err != nil {
		t.Fatalf("switching mail sharing off: %v", err)
	}

	// The next captured email is born participants-only; the first keeps
	// the audience it was captured under.
	fake.emitSecondMessage = true
	if err := registry.SyncOnce(grantCtx, connID); err != nil {
		t.Fatal(err)
	}
	if got := audienceOf("msg-2"); got != "participants" {
		t.Errorf("with sharing off the new email's audience = %q, want participants", got)
	}
	if got := audienceOf("msg-1"); got != "workspace" {
		t.Errorf("switching sharing off rewrote already-captured mail to %q — the setting must move the default only", got)
	}
}
