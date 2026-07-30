// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package identity

// The lock order across the two paths that touch a connection's rows: a human
// revoking a connection at the instant the connector renews it must QUEUE on
// the grant row, not deadlock against a refresh row the other side grabbed
// first. Both paths take oauth_grant before oauth_refresh_token; this suite
// runs them against each other over a real Postgres, because a lock order is
// only ever a property of real transactions.

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// connectFixture is one consented connection: a registered client, the grant
// beneath it, and the refresh token the connector holds.
type connectFixture struct {
	clientID string
	grantID  ids.UUID
	refresh  string
}

// connectOAuth mints a connection through the module's own issuance path, so
// the fixture is the same shape the code exchange commits.
func (e *revocationEnv) connectOAuth(t *testing.T) connectFixture {
	t.Helper()
	// The full id, not a prefix: consecutive v7 ids share their leading bytes
	// within a millisecond, and every attempt in this suite registers its own
	// client.
	clientID := "client-" + ids.NewV7().String()
	if _, err := e.owner.Exec(context.Background(), `
		INSERT INTO oauth_client (workspace_id, client_id, client_name, redirect_uris)
		VALUES ($1, $2, 'lock order', ARRAY['https://client.example/cb'])`,
		e.admin.WorkspaceID, clientID); err != nil {
		t.Fatalf("registering the client: %v", err)
	}

	var out connectFixture
	out.clientID = clientID
	ctx := e.wsCtx(e.admin)
	if err := database.WithWorkspaceTx(ctx, e.svc.pool, func(tx pgx.Tx) error {
		var err error
		out.grantID, out.refresh, err = issueGrant(ctx, tx, issueGrantInput{
			WorkspaceID: e.admin.WorkspaceID, UserID: e.admin.UserID, ClientID: clientID,
			Scopes: []string{"read"}, RefreshAllowed: true,
		})
		return err
	}); err != nil {
		t.Fatalf("issuing the grant: %v", err)
	}
	return out
}

// TestARevokeRacingARotationNeverDeadlocksOrLeavesACredentialLive fires the
// two paths at one grant simultaneously. Whoever wins, two things must hold:
// neither side may fail with a database error — a lock-order inversion shows
// up here as a deadlock abort, which the caller can only answer with a 500 on
// an operation the lock exists to serialize — and a revoked connection must
// have nothing live left behind it, however the two interleaved.
//
// The race is run repeatedly because the interleaving is the scheduler's
// choice, not ours: one pass proves little, and a pass that never deadlocks
// across many attempts is the strongest deterministic statement available
// (see the report — a true deadlock cannot be forced without observing the
// other transaction's locks, so this suite proves the absence, not the
// presence).
func TestARevokeRacingARotationNeverDeadlocksOrLeavesACredentialLive(t *testing.T) {
	e := setupRevocationEnv(t, "oauth-lock-order")

	const attempts = 12
	for attempt := range attempts {
		fixture := e.connectOAuth(t)

		var (
			wg          sync.WaitGroup
			rotateErr   error
			revokeErr   error
			rotationCtx = e.wsCtx(e.admin)
			revokeCtx   = e.wsCtx(e.admin)
		)
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _, rotateErr = e.svc.rotateRefreshToken(rotationCtx, refreshRequest{
				Token: fixture.refresh, ClientID: fixture.clientID,
			})
		}()
		go func() {
			defer wg.Done()
			revokeErr = database.WithWorkspaceTx(revokeCtx, e.svc.pool, func(tx pgx.Tx) error {
				return e.svc.revokeGrantTx(revokeCtx, tx, fixture.grantID, "the human ended the connection")
			})
		}()
		wg.Wait()

		// The rotation either won the grant lock and renewed, or queued behind
		// the revoke and found the connection dead. Any other error is the
		// database refusing the interleaving.
		if rotateErr != nil && !errors.Is(rotateErr, errRefreshRejected) {
			t.Fatalf("attempt %d: rotation failed on the interleaving, not on the rule: %v", attempt, rotateErr)
		}
		if revokeErr != nil {
			t.Fatalf("attempt %d: revoke failed on the interleaving: %v", attempt, revokeErr)
		}

		// Whatever the order, a revoked connection holds no live credential:
		// if the rotation won, the cascade caught the passport and the
		// successor it had just minted.
		e.assertNothingLiveUnder(t, fixture.grantID, attempt)
	}
}

// assertNothingLiveUnder is the end state a revoked connection must always
// reach: the grant dead, no spendable refresh token, no usable passport.
func (e *revocationEnv) assertNothingLiveUnder(t *testing.T, grantID ids.UUID, attempt int) {
	t.Helper()
	ctx := context.Background()
	var revoked bool
	if err := e.owner.QueryRow(ctx,
		`SELECT revoked_at IS NOT NULL FROM oauth_grant WHERE id = $1`, grantID).Scan(&revoked); err != nil {
		t.Fatalf("attempt %d: reading the grant: %v", attempt, err)
	}
	if !revoked {
		t.Fatalf("attempt %d: the grant survived its own revocation", attempt)
	}
	var spendable, usable int
	if err := e.owner.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM oauth_refresh_token WHERE grant_id = $1 AND consumed_at IS NULL),
		       (SELECT count(*) FROM passport WHERE oauth_grant_id = $1 AND revoked_at IS NULL)`,
		grantID).Scan(&spendable, &usable); err != nil {
		t.Fatalf("attempt %d: reading the credentials under the grant: %v", attempt, err)
	}
	if spendable != 0 || usable != 0 {
		t.Fatalf("attempt %d: revoked connection left %d spendable refresh token(s) and %d usable passport(s)",
			attempt, spendable, usable)
	}
}
