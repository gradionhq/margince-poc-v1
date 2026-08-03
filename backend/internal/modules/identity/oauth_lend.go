// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// What a consent COMMITS. The screen's read model and the predicate deciding
// which passports a human may lend live next door in oauth_consent.go; this file
// is the write that ends the flow — one transaction holding the lent passport's
// row, the single-use authorization code, and the audit row naming the lend.
// Split out of oauth.go so the authorization server's request handling and the
// decision it commits stay one concept per file.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// mintLentAuthorizationCode commits one consent decision: it re-resolves the
// passport the human offered to lend, writes the single-use code the client will
// redeem, and records which passport was lent — all in ONE transaction, with the
// lent passport's row locked for its duration. It returns the plaintext courier;
// only the courier's hash is stored. The scopes it records are the ones the human
// actually lent — the lent passport's own, never the client's request.
//
// lendable is false when the passport did not survive the re-check, and then this
// transaction writes NOTHING: no code row, no audit row. Refusing is the ordinary
// outcome of a stale selection, not a failure, so it is reported as an answer
// rather than an error.
//
// The re-check runs INSIDE this transaction and takes the passport's row lock
// (lockLentPassport). That is what makes "a passport revoked in another tab is
// not lendable" hold under concurrency: a revocation racing this consent either
// commits before the lock is taken — and is then seen, so no code is written — or
// waits behind this transaction and finds the lend already recorded.
//
// The offline_access marker's durable home is oauth_grant.refresh_allowed, and
// no grant exists until the code is redeemed — so it rides in the code's
// unconstrained scopes column to survive the round trip instead of dying here.
// The exchange re-derives the boolean from it and strips it before any scope
// reaches the passport (oauth_token.go).
//
// WHERE THAT LOCK'S REACH ENDS is the code's own five minutes (authCodeTTL). A
// passport revoked after this transaction commits does not stop the code from
// being redeemed, and the redemption re-checks the human
// (requireLiveConsentingUser) and nothing else. That asymmetry is the design:
// the human's authority is what a connection borrows, while the lent passport
// contributes its scopes and then stops being party to the connection. The
// client ends up holding a NEW grant-bound passport (oauth_token.go), so
// revoking a lent passport leaves connections already derived from it working,
// and ending one goes through its grant instead (proven:
// TestALentPassportRevokedAfterConsentStillRedeems).
//
// oauth_authorization_code.lent_passport_id NAMES the lent passport (migration
// 0171), and that does not move this boundary — writing the id down is not the
// same act as consulting it. What WOULD move it is a redemption that reads the
// column to decide anything: that is a decision about what a lend means, taken
// in a WHERE clause, and the test above is what fails when someone takes it.
// The column exists so Settings can answer "which of my passports is this
// connection derived from?" without reading the audit log, and the exchange
// forwards it to the grant untouched.
func (s *Service) mintLentAuthorizationCode(
	ctx context.Context, id Identity, rawPassportID string, req authorizeRequest,
) (code string, lendable bool, err error) {
	// Lending authority is a decision only the human who holds it may take,
	// refused at this seam exactly as SelectablePassports refuses it, rather than
	// trusted to the transport that got here.
	if err := auth.RequireHuman(ctx); err != nil {
		return "", false, err
	}
	code, err = randomToken()
	if err != nil {
		return "", false, err
	}
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockConsentingUser(ctx, tx, id); err != nil {
			return err
		}
		lent, ok, err := lockLentPassport(ctx, tx, id, rawPassportID)
		if err != nil {
			return err
		}
		if !ok {
			// The transaction ends having written nothing at all: there is no code
			// row to roll back and no audit row to explain away.
			return nil
		}
		if err := writeAuthorizationCode(ctx, tx, code, req, id, lent); err != nil {
			return err
		}
		lendable = true
		return nil
	})
	if err != nil {
		return "", false, err
	}
	if !lendable {
		// The courier reached no row, so it is not handed back: a caller holding
		// one could only put it in a redirect no exchange would honour.
		return "", false, nil
	}
	return code, true, nil
}

// lockConsentingUser takes the consenting human's app_user row FOR KEY SHARE —
// the very lock the code row's foreign key takes anyway — but takes it FIRST,
// ahead of the lent passport's row lock. app_user before passport is the order
// every path that holds both already takes: DeactivateUser locks app_user FOR
// UPDATE and only then revokes that human's passports (users.go), which is the
// same chain the connection rows continue (the lock order in oauth_grant.go).
// Locking the passport first would invert it, and an admin deactivating the human
// at the instant they consent would deadlock instead of one side queueing.
//
// It decides nothing, and the MODE is why it can afford not to. Whether the human
// is still active is judged where the grant is built
// (requireLiveConsentingUser), which takes the row FOR UPDATE; this lock exists
// only to fix an order, so it takes the weakest mode that does — the FK's own —
// and the insert below then finds it already held. FOR KEY SHARE does not
// self-conflict, so one human's two concurrent consents still run in parallel. It
// DOES queue behind the FOR UPDATE a deactivation (users.go) or a failed-login
// fold (lockout.go) holds on this row; both are short, and in the deactivation's
// case queueing is the point — the consent then re-reads a passport that cascade
// has already revoked.
//
// An absent row is this server contradicting itself — the session middleware
// resolved this identity from it — so it is reported rather than mistaken for a
// passport that may not be lent.
func lockConsentingUser(ctx context.Context, tx pgx.Tx, id Identity) error {
	var locked ids.UserID
	err := tx.QueryRow(ctx,
		`SELECT id FROM app_user WHERE id = $1 AND workspace_id = $2 FOR KEY SHARE`,
		id.UserID, id.WorkspaceID).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("oauth: consent for user %s: the app_user row is absent", id.UserID)
	}
	return err
}

// writeAuthorizationCode is the pair of rows a lend commits, inside the caller's
// transaction: the single-use code — carrying the lent passport's id, which the
// redemption copies onto the grant so Settings can name it — and the audit row
// naming the same passport. They commit TOGETHER because which passport a human
// handed to a client is the central authority fact of this flow, and a code that
// existed without it would be a lend nobody could trace.
//
// The code's lent_passport_id is PROVENANCE, and the redemption reads it for
// exactly that. It is deliberately not a second copy of the lend's authority:
// the scopes column already carries what the connection receives, resolved under
// the row lock, and nothing downstream re-derives them from the passport this
// column names (mintLentAuthorizationCode's §"WHERE THAT LOCK'S REACH ENDS").
func writeAuthorizationCode(
	ctx context.Context, tx pgx.Tx, code string, req authorizeRequest, id Identity, lent lentPassport,
) error {
	storedScopes := lent.Scopes
	if req.Offline {
		storedScopes = append(append([]string{}, lent.Scopes...), scopeOfflineAccess)
	}
	var codeID ids.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO oauth_authorization_code
		  (workspace_id, code_hash, client_id, user_id, scopes, code_challenge, redirect_uri, resource, expires_at,
		   lent_passport_id)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
		        $1, $2, $3, $4, $5, $6, NULLIF($7, ''), now() + $8::interval,
		        $9)
		RETURNING id`,
		hashOAuthCode(code), req.ClientID, id.UserID, storedScopes, req.CodeChallenge,
		req.RedirectURI, req.Resource, authCodeTTL.String(), lent.ID).Scan(&codeID); err != nil {
		return err
	}
	return auditLend(ctx, tx, codeID, req, lent)
}

// auditLend records WHICH of the human's passports was lent to this client,
// and the authority that went with it.
//
// The code and grant rows now carry lent_passport_id for the Settings list, so
// this is no longer the only record of the lend — and it is still not
// redundant. Those columns are ON DELETE SET NULL and hold only the CURRENT
// answer; this row holds the dated one, alongside the actor and the scopes as
// they stood at consent, which is what an investigation reads.
//
// The after image is the authority actually handed over — the lent passport's
// scopes, never the client's request — and refresh_allowed beside them, the
// same pair issueGrant records when the code is later redeemed, so the consent
// and its redemption read as one story. The actor is stamped by storekit from
// the authenticated principal; the session middleware bound it, so it can never
// come from the request body. Only the code's hash is ever stored, and the
// plaintext courier appears in no audit field.
//
// No outbox event rides with it. The events.md §5 catalog is closed and defines
// no oauth-consent verb — exactly as it defines none for oauth_grant, which is
// why issueGrant audits without emitting too (oauth_grant.go). The one type
// that would fit structurally, audit.appended, is declared in the contract as
// having no emit site and none planned for V1, so emitting it would need the
// contract changed first: raised upstream (P3) rather than filled here with a
// type that means something else.
func auditLend(
	ctx context.Context, tx pgx.Tx, codeID ids.UUID, req authorizeRequest, lent lentPassport,
) error {
	_, err := storekit.Audit(ctx, tx, "create", "oauth_authorization_code", codeID, nil,
		map[string]any{
			auditFieldPassportID:     lent.ID,
			auditFieldClientID:       req.ClientID,
			auditFieldScopes:         lent.Scopes,
			auditFieldRefreshAllowed: req.Offline,
		})
	return err
}
