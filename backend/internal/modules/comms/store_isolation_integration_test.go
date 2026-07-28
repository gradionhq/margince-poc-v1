// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package comms

// Finding 3 (security round 1): RLS, not application logic, is what keeps
// one workspace from ever touching another's delivery — a bare id is the
// entire argument to Load/RecordSent/Park/RecordFailure, so this is the
// one thing standing between a wrong id and another tenant's recipients,
// subject and body. Shares storeEnv/setupStore/stage/baseInput with
// store_integration_test.go.

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// foreignWorkspaceCtx seeds a second, unrelated workspace and returns a
// context bound to it — no actor needed, since none of the methods this
// test drives from it (Load/RecordSent/Park/RecordFailure) require one.
func (e *storeEnv) foreignWorkspaceCtx(t *testing.T) context.Context {
	t.Helper()
	other := ids.NewV7()
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'CommsOther', $2, 'EUR')`,
		other, "comms-other-"+other.String()); err != nil {
		t.Fatal(err)
	}
	return principal.WithWorkspaceID(context.Background(), other)
}

func TestDeliveryIsInvisibleAndUnmutableFromAnotherWorkspace(t *testing.T) {
	e := setupStore(t)
	id := e.stage(t, e.baseInput(e.activity, "msg-cross-workspace@example.com"))
	other := e.foreignWorkspaceCtx(t)

	if _, err := e.store.Load(other, id); err != ErrTerminal {
		t.Fatalf("Load from another workspace: got %v, want ErrTerminal (RLS must hide the row entirely)", err)
	}
	if err := e.store.RecordSent(other, id, "stolen-receipt"); err != ErrTerminal {
		t.Fatalf("RecordSent from another workspace: got %v, want ErrTerminal", err)
	}
	if err := e.store.Park(other, id, "stolen-park"); err != ErrTerminal {
		t.Fatalf("Park from another workspace: got %v, want ErrTerminal", err)
	}
	if err := e.store.RecordFailure(other, id, "stolen-failure"); err != ErrTerminal {
		t.Fatalf("RecordFailure from another workspace: got %v, want ErrTerminal", err)
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
