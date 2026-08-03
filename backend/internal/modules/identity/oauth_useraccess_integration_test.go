// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package identity

// Ending a HUMAN's access has to end what borrows it. A connector's authority
// is not the passport it holds — that is one credential of a connection whose
// refresh chain mints replacements and slides its own renewal window forward
// with nobody in the loop. So the two halves proven here: the kill path
// (DeactivateUser) reaches the grant cascade, and rotation refuses on its own
// for a human who is no longer active, which is the backstop for a kill path
// that forgets to.

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// TestDeactivatingAHumanEndsTheConnectionsTheyConsentedTo is the sibling of the
// cascade RevokePassport takes: revoking the credentials and leaving the
// consent alive makes the kill a pause, not a kill. The connector renews
// straight through it, and reactivating the human hands the connection full
// authority again with no new consent.
func TestDeactivatingAHumanEndsTheConnectionsTheyConsentedTo(t *testing.T) {
	e := setupRevocationEnv(t, "deactivate-ends-connection")
	ctx := principal.WithWorkspaceID(context.Background(), e.admin.WorkspaceID.UUID)
	fixture := e.connectOAuthFor(t, e.member)
	passportID := e.mintUnderGrantFor(t, fixture.grantID, e.member)

	if _, err := e.svc.AuthenticateAgentByID(ctx, passportID); err != nil {
		t.Fatalf("the connector's passport must work before the deactivation: %v", err)
	}

	if err := e.svc.DeactivateUser(e.wsCtx(e.admin), e.admin, DeactivateUserInput{UserID: e.member.UserID}); err != nil {
		t.Fatalf("deactivate: %v", err)
	}

	e.assertNothingLiveUnder(t, fixture.grantID, "after deactivating the human who consented")
	if _, _, err := e.svc.rotateRefreshToken(e.wsCtx(e.member), refreshRequest{
		Token: fixture.refresh, ClientID: fixture.clientID,
	}); !errors.Is(err, errRefreshRejected) {
		t.Errorf("a deactivated human's connector renewed itself: err = %v, want the refusal", err)
	}

	// Reactivation returns the HUMAN's access, never the connector's: a
	// connection that came back to life on the admin's undo would carry
	// authority no human consented to a second time.
	if err := e.svc.ReactivateUser(e.wsCtx(e.admin), e.admin, e.member.UserID); err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	if _, err := e.svc.AuthenticateAgentByID(ctx, passportID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("reactivation revived the connector's passport: err = %v, want not-found", err)
	}
	if _, _, err := e.svc.rotateRefreshToken(e.wsCtx(e.member), refreshRequest{
		Token: fixture.refresh, ClientID: fixture.clientID,
	}); !errors.Is(err, errRefreshRejected) {
		t.Errorf("reactivation made the refresh chain spendable again: err = %v, want the refusal", err)
	}
}

// The same property, from the other side and without the cascade: the human's
// status is cut in the STORE, so nothing walked down to the grant. Rotation has
// to refuse anyway — that is what makes a kill path nobody extended fail
// closed, exactly as the agent-auth liveness rule does for calls.
func TestRotationRefusesForAHumanWhoIsNoLongerActive(t *testing.T) {
	for name, statement := range map[string]string{
		"deactivated": `UPDATE app_user SET status = 'deactivated' WHERE id = $1`,
		"suspended":   `UPDATE app_user SET status = 'suspended' WHERE id = $1`,
		"archived":    `UPDATE app_user SET archived_at = now() WHERE id = $1`,
	} {
		t.Run(name, func(t *testing.T) {
			e := setupRevocationEnv(t, "rotation-human-"+name)
			fixture := e.connectOAuthFor(t, e.member)

			// One successful rotation first, and its successor is what the
			// connector would present next. Without this precondition the
			// refusal below could be coming from the fixture rather than from
			// the human's state.
			_, successor, err := e.svc.rotateRefreshToken(e.wsCtx(e.member), refreshRequest{
				Token: fixture.refresh, ClientID: fixture.clientID,
			})
			if err != nil {
				t.Fatalf("the fixture's first rotation must succeed: %v", err)
			}

			if _, err := e.owner.Exec(context.Background(), statement, e.member.UserID); err != nil {
				t.Fatal(err)
			}
			if _, _, err := e.svc.rotateRefreshToken(e.wsCtx(e.member), refreshRequest{
				Token: successor, ClientID: fixture.clientID,
			}); !errors.Is(err, errRefreshRejected) {
				t.Errorf("rotation for a %s human: err = %v, want the refusal", name, err)
			}
		})
	}
}
