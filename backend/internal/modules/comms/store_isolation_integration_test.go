// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package comms

// RLS, not application logic, is what keeps one workspace from ever touching
// another's delivery — a bare id is the entire argument to
// Load/RecordSent/Park/RecordFailure, so this is the one thing standing
// between a wrong id and another tenant's recipients, subject and body.
// Shares storeEnv/setupStore/stage/baseInput with store_integration_test.go.

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// foreignWorkspace seeds a second, unrelated workspace and returns a store
// bound to it beside a context naming it — no actor needed, since none of the
// methods this test drives from it (Load/RecordSent/Park/RecordFailure)
// require one.
//
// The store as well as the ctx, because the workspace a statement runs in is
// the handle's: asking THIS tenant's store with the other tenant's ctx would
// read this tenant's own rows, and every assertion below would hold for the
// wrong reason.
func (e *storeEnv) foreignWorkspace(t *testing.T) (*Store, context.Context) {
	t.Helper()
	other := ids.NewV7()
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO workspace (id, slug) VALUES ($1, $2)`,
		other, "comms-other-"+other.String()); err != nil {
		t.Fatal(err)
	}
	return NewStore(database.BindTo(e.store.db.Pool(), ids.From[ids.WorkspaceKind](other)), e.store.now, nil),
		principal.WithWorkspaceID(context.Background(), other)
}

func TestDeliveryIsInvisibleAndUnmutableFromAnotherWorkspace(t *testing.T) {
	e := setupStore(t)
	id := e.stage(t, e.baseInput(e.activity, "msg-cross-workspace@example.com"))
	foreign, other := e.foreignWorkspace(t)

	if _, err := foreign.Load(other, id); err != ErrTerminal {
		t.Fatalf("Load from another workspace: got %v, want ErrTerminal (RLS must hide the row entirely)", err)
	}
	if err := foreign.RecordSent(other, id, connector.SendReceipt{ProviderMessageID: "stolen-receipt"}); err != ErrTerminal {
		t.Fatalf("RecordSent from another workspace: got %v, want ErrTerminal", err)
	}
	if err := foreign.Park(other, id, "stolen-park"); err != ErrTerminal {
		t.Fatalf("Park from another workspace: got %v, want ErrTerminal", err)
	}
	if err := foreign.RecordFailure(other, id, "stolen-failure"); err != ErrTerminal {
		t.Fatalf("RecordFailure from another workspace: got %v, want ErrTerminal", err)
	}
	// The deferral is the one transition that also moves the attempt counter,
	// so a cross-workspace call reaching it would not merely write a foreign
	// row — it would hand another tenant's delivery a free rung of its ladder.
	if err := foreign.RecordDeferral(other, id, "stolen-deferral"); err != ErrTerminal {
		t.Fatalf("RecordDeferral from another workspace: got %v, want ErrTerminal", err)
	}

	// Sanity: the row is untouched and still loadable from its OWN
	// workspace — proves the isolation above is RLS actually working, not a
	// fixture that silently staged nothing.
	got, err := e.store.Load(e.ctx, id)
	if err != nil {
		t.Fatalf("Load from the owning workspace after the cross-workspace attempts: %v", err)
	}
	if got.Status != StatusPending {
		t.Fatalf("status = %q after cross-workspace mutation attempts, want still pending", got.Status)
	}
	var providerMessageID, reason *string
	if err := e.owner.QueryRow(context.Background(),
		`SELECT provider_message_id, reason FROM comms_outbound WHERE id = $1`, id).
		Scan(&providerMessageID, &reason); err != nil {
		t.Fatal(err)
	}
	if providerMessageID != nil || reason != nil {
		t.Fatalf("provider_message_id=%v reason=%v after cross-workspace mutation attempts, want both untouched (nil)", providerMessageID, reason)
	}
}
