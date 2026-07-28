// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package comms

// Finding 1 (security round 1): user_id is derived from the authenticated
// principal, never taken from caller input, and staging fails closed with
// no principal to derive it from. StageInput carries no UserID field at
// all, so there is no code path for a caller to name a different sender,
// forged or otherwise — these tests prove the derivation always tracks the
// true caller. Shares storeEnv/setupStore/actorCtx/stage/baseInput with
// store_integration_test.go.

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// Two different human actors in the same workspace, staging under their own
// contexts, must each be attributed their OWN user_id — proof the value
// written always tracks whichever principal actually made the call.
func TestStageTxStampsUserIDFromTheAuthenticatedPrincipalNeverFromInput(t *testing.T) {
	e := setupStore(t)
	user2 := ids.New[ids.UserKind]()
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'Rep2')`,
		user2, e.ws, "rep2-"+user2.String()+"@comms.test"); err != nil {
		t.Fatal(err)
	}
	ctx2 := actorCtx(e.ws, user2)

	id1 := e.stage(t, e.baseInput(e.activity, "msg-actor-1@example.com"))
	var id2 ids.UUID
	err := database.WithWorkspaceTx(ctx2, e.store.pool, func(tx pgx.Tx) error {
		var err error
		id2, err = e.store.StageTx(ctx2, tx, e.baseInput(e.activity, "msg-actor-2@example.com"))
		return err
	})
	if err != nil {
		t.Fatalf("staging under the second actor: %v", err)
	}

	got1, err := e.store.Load(e.ctx, id1)
	if err != nil {
		t.Fatalf("Load id1: %v", err)
	}
	if got1.UserID != e.user {
		t.Fatalf("delivery staged by actor 1 has user_id %v, want %v (actor 1's own id)", got1.UserID, e.user)
	}
	got2, err := e.store.Load(ctx2, id2)
	if err != nil {
		t.Fatalf("Load id2: %v", err)
	}
	if got2.UserID != user2 {
		t.Fatalf("delivery staged by actor 2 has user_id %v, want %v (actor 2's own id)", got2.UserID, user2)
	}
}

// No authenticated actor bound to ctx at all: StageTx must refuse before
// writing anything, not proceed with a zero-value or borrowed identity.
func TestStageTxFailsClosedWithoutAnAuthenticatedActor(t *testing.T) {
	e := setupStore(t)
	noActor := principal.WithWorkspaceID(context.Background(), e.ws)
	in := e.baseInput(e.activity, "msg-no-actor@example.com")
	err := database.WithWorkspaceTx(noActor, e.store.pool, func(tx pgx.Tx) error {
		_, err := e.store.StageTx(noActor, tx, in)
		return err
	})
	// A bare "err == nil" check would still pass if the app-level guard were
	// removed and only the comms_outbound_user_id_fkey constraint on a
	// zero-value user_id happened to catch it — so assert the refusal is
	// comms's own clean identity error, never a raw SQL constraint error
	// leaked to the caller (T2: no internals — SQLSTATE, table names — in an
	// error message a client can see).
	if err == nil || strings.Contains(err.Error(), "SQLSTATE") || strings.Contains(err.Error(), "constraint") {
		t.Fatalf("StageTx with no authenticated actor: got %v, want a clean fail-closed refusal, not a raw DB error", err)
	}

	var count int
	if qerr := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM comms_outbound WHERE message_id = $1`, in.MessageID).Scan(&count); qerr != nil {
		t.Fatal(qerr)
	}
	if count != 0 {
		t.Fatalf("rows after a no-actor stage attempt = %d, want 0 — fail closed means nothing written", count)
	}
}

// A system principal carries no app_user identity; sending is a human act,
// so StageTx must refuse rather than write a zero-value user_id.
func TestStageTxRefusesASystemPrincipalWithNoAppUserIdentity(t *testing.T) {
	e := setupStore(t)
	systemCtx := principal.WithWorkspaceID(context.Background(), e.ws)
	systemCtx = principal.WithActor(systemCtx, principal.Principal{Type: principal.PrincipalSystem})
	err := database.WithWorkspaceTx(systemCtx, e.store.pool, func(tx pgx.Tx) error {
		_, err := e.store.StageTx(systemCtx, tx, e.baseInput(e.activity, "msg-system@example.com"))
		return err
	})
	if err == nil || strings.Contains(err.Error(), "SQLSTATE") || strings.Contains(err.Error(), "constraint") {
		t.Fatalf("StageTx under a system principal (no app_user identity): got %v, want a clean fail-closed refusal, not a raw DB error — sending is a human act", err)
	}
	if !strings.Contains(err.Error(), "authenticated app_user identity") {
		t.Fatalf("StageTx under a system principal (no app_user identity): got %v, want comms's own IsZero check to name the reason (an actor WAS bound, just with no app_user)", err)
	}
}
