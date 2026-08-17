// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalooa

// The sealed credential, and its renewal — the sharpest correctness requirement
// in this unit.
//
// A ZALO REFRESH TOKEN IS SINGLE-USE. Spending it kills it and issues a
// replacement pair, so a rotation that is issued and not kept does not degrade
// anything: it ENDS the connection, and the only way back is an OA admin
// clicking *Cho phép* in a browser, at another company. Everything below is
// arranged around that one fact.
//
// Four rules, each visible in the code rather than only here:
//
//  1. ONE DOCUMENT. The access token, the refresh token and the expiry are
//     sealed as a single value, so a rotation is one write and the halves cannot
//     land apart — a live access token beside a dead refresh token is a
//     connection that works for a day and then cannot be renewed by anybody.
//  2. ONE WRITER. Renewal is claimed with an atomic compare-and-set on the
//     connection row before anything is sent, so exactly one caller ever
//     presents a given refresh token. The claim is a LEASE, because the winner
//     can die.
//  3. PERSIST BEFORE USE. The new pair is sealed before it is spent or mirrored.
//  4. AN UNKEPT ROTATION PARKS, and does not retry. Once the provider has
//     rotated, presenting the old token again cannot succeed — so a second
//     attempt is not a recovery, it is a slower way to end at the same place with
//     the evidence gone.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// The credential's own refusal classes, told apart because each sends a human
// somewhere different.
const (
	// errCredentialGone is no sealed pair for the authorizing admin: they were
	// never connected, or somebody withdrew the deposit.
	//nolint:gosec // G101 false positive: this is the text of a refusal an operator reads, not a credential
	errCredentialGone zaloError = "zalo-oa: no credential is on deposit for the admin who authorized this connection"

	// errRefreshInFlight is another caller holding the renewal lease. It is
	// transient in the truest sense — the work is being done, elsewhere, right
	// now — so the caller stops and the next tick finds a fresh token.
	errRefreshInFlight zaloError = "zalo-oa: another caller is renewing this credential"

	// errRotationLost is a rotation the provider performed and this side did not
	// keep. It is PERMANENT: the old refresh token is dead, the new one is
	// unknown, and only a human can end it.
	errRotationLost zaloError = "zalo-oa: the credential was rotated and the replacement was not kept"
)

// refreshLease is how long a claim is honoured before another caller may try.
//
// It is longer than the token endpoint's own timeout so an ordinary slow
// renewal is never overtaken, and short enough that a worker that died holding
// the claim does not hold the connection shut for a shift.
const refreshLease = 2 * time.Minute

// sealTokens writes the pair as ONE document under the authorizing admin.
//
// Rotation is a Put, and the port destroys the superseded material once the
// replacement is durable — so a credential renewed daily does not accumulate.
func sealTokens(ctx context.Context, rt extension.Runtime, admin extension.UserID, pair tokenPair) error {
	//nolint:gosec // G117: encoding the token pair is what SEALING it means — the bytes go straight to the custodian and are never logged, returned or written to a row
	sealed, err := json.Marshal(pair)
	if err != nil {
		return fmt.Errorf("zalo-oa: sealing the token pair: %w", err)
	}
	return rt.Secrets().PutUser(ctx, admin, tokenKey, sealed)
}

// unsealTokens reads the pair back.
func unsealTokens(ctx context.Context, rt extension.Runtime, admin extension.UserID) (tokenPair, error) {
	sealed, err := rt.Secrets().GetUser(ctx, admin, tokenKey)
	if err != nil {
		if errors.Is(err, extension.ErrSecretNotFound) {
			return tokenPair{}, errCredentialGone
		}
		return tokenPair{}, err
	}
	var pair tokenPair
	if err := json.Unmarshal(sealed, &pair); err != nil {
		// A sealed document this unit cannot read is not a token problem to
		// retry: nothing later will decode it either.
		return tokenPair{}, fmt.Errorf("%w: the sealed credential is not the shape this unit wrote", errRotationLost)
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" {
		return tokenPair{}, fmt.Errorf("%w: the sealed credential is missing half of the pair", errRotationLost)
	}
	return pair, nil
}

// forgetCredential removes everything sealed for one admin: the token pair, and
// any PKCE verifier left from an authorization.
//
// A key that holds nothing is not an error here — this is the withdrawal path,
// and "it was already gone" is the outcome asked for.
func forgetCredential(ctx context.Context, rt extension.Runtime, admin extension.UserID) error {
	for _, key := range []string{tokenKey, verifierKey} {
		if err := rt.Secrets().DeleteUser(ctx, admin, key); err != nil &&
			!errors.Is(err, extension.ErrSecretNotFound) {
			return err
		}
	}
	return nil
}

// usableToken hands back an access token worth spending, renewing it first when
// it is close to expiring.
//
// PROACTIVE, never lazily on a refusal inside a send: a renewal discovered
// mid-transmission is a renewal racing a message, and this provider's send has
// no idempotency key to make that race safe. The margin is on tokenPair.usable.
//
// It returns the connection as it stands afterwards, because a renewal moves the
// row (the expiry mirror, the lease) and the caller's copy is stale from that
// point on.
//
// NOTHING HERE RUNS INSIDE A CALLER'S TRANSACTION. The secret port reaches the
// pool on its own, so unsealing while holding a transaction would take a second
// connection while holding one — which on a small pool does not fail, it hangs.
// The lease exists precisely so the serialization does not need a held
// transaction to provide it.
func usableToken(ctx context.Context, rt extension.Runtime, grants grantExchanger,
	conn connection, now time.Time,
) (tokenPair, connection, error) {
	admin := extension.UserID(conn.AuthorizedBy)
	pair, err := unsealTokens(ctx, rt, admin)
	if err != nil {
		return tokenPair{}, conn, err
	}
	if pair.usable(now) {
		return pair, conn, nil
	}
	// The app secret is read BEFORE the lease is claimed. It is the one input a
	// rotation needs that this side can be missing, and discovering that while
	// holding the lease would shut the renewal for the lease's whole length over
	// a fault that has nothing to do with the provider.
	appSecret, err := rt.Secrets().Get(ctx, appSecretKey)
	if err != nil {
		if errors.Is(err, extension.ErrSecretNotFound) {
			return tokenPair{}, conn, fmt.Errorf("%w: this installation has no app secret on deposit, so the credential cannot be renewed", errCredentialGone)
		}
		return tokenPair{}, conn, err
	}
	claimed, won, err := claimRefresh(ctx, rt, conn)
	if err != nil {
		return tokenPair{}, conn, err
	}
	if !won {
		return tokenPair{}, conn, errRefreshInFlight
	}
	return rotate(ctx, rt, grants, claimed, admin, string(appSecret), pair)
}

// rotate spends the refresh token and keeps what comes back.
//
// Every failure below is a DIFFERENT thing to do about it, which is why they are
// not one branch: a refusal means the old token is dead and a human is needed; an
// unanswered call means nobody knows whether it is dead, which is the same
// remedy for a worse reason; and a failure to keep a rotation the provider
// performed is the one this whole file is arranged to make survivable — it is
// not, so it is at least made loud.
func rotate(ctx context.Context, rt extension.Runtime, grants grantExchanger,
	conn connection, admin extension.UserID, appSecret string, spent tokenPair,
) (tokenPair, connection, error) {
	renewed, err := grants.Rotate(ctx, conn.AppID, appSecret, spent.RefreshToken)
	switch {
	case errors.Is(err, errUnanswered):
		// The request went out and no answer came back, so the old refresh token
		// may be dead and may not, and nothing this side holds can tell. Retrying
		// would present a token that is dead half the time and would consume the
		// live half — so it parks, with the uncertainty on the record.
		parked, perr := park(ctx, rt, conn, "refresh_outcome_unknown", statusReauth)
		return tokenPair{}, parked, errors.Join(fmt.Errorf("%w: %w", errRotationLost, err), perr)
	case errors.Is(err, errUnauthorized):
		parked, perr := park(ctx, rt, conn, "refresh_token_rejected", statusReauth)
		return tokenPair{}, parked, errors.Join(err, perr)
	case err != nil:
		// The provider was unreachable or unreadable and nothing was spent. The
		// lease is released so the next tick may try again.
		released, rerr := releaseRefresh(ctx, rt, conn)
		return tokenPair{}, released, errors.Join(err, rerr)
	}
	// THE PROVIDER HAS NOW ROTATED. From here the old refresh token is dead
	// whatever happens, so the replacement is sealed before it is used, mirrored
	// or reported.
	if err := sealTokens(ctx, rt, admin, renewed); err != nil {
		parked, perr := park(ctx, rt, conn, "refresh_rotation_lost", statusReauth)
		return tokenPair{}, parked, errors.Join(fmt.Errorf("%w: %w", errRotationLost, err), perr)
	}
	after, err := completeRefresh(ctx, rt, conn, renewed.ExpiresAt)
	if err != nil {
		// The credential IS sealed, so nothing is lost — what failed is the
		// mirror and the lease release, and the next tick reads a usable pair and
		// carries on. It is still reported rather than swallowed: a row that
		// cannot be written is a fact about this installation, not about Zalo.
		return tokenPair{}, conn, err
	}
	return renewed, after, nil
}

// claimRefresh takes the renewal lease, atomically.
//
// The compare-and-set is the serialization: PostgreSQL takes a row lock for the
// UPDATE, so of two callers arriving together exactly one finds the lease free
// and the other finds it held. `won` is false for the loser — not an error,
// because nothing went wrong; somebody else is doing the work.
//
// The version is deliberately NOT part of the predicate. A poll and a send
// racing hold different versions of a row that is otherwise fine, and refusing
// the renewal because the row moved would leave a connection unable to renew for
// the reason that it is busy.
func claimRefresh(ctx context.Context, rt extension.Runtime, conn connection) (connection, bool, error) {
	var (
		claimed connection
		won     bool
	)
	err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		updated, err := scanConnection(tx.QueryRow(ctx,
			`UPDATE `+connectionTable+`
			    SET refresh_claimed_at = now(), version = version + 1, updated_at = now()
			  WHERE id = $1::uuid
			    AND (refresh_claimed_at IS NULL OR refresh_claimed_at < now() - $2::interval)
			 RETURNING `+connectionColumns, conn.ID, refreshLease.String()).Scan)
		if err != nil {
			if isNoRows(err) {
				// Either another caller holds the lease, or the connection was
				// disconnected while this tick was reading. Both mean this caller
				// does not renew, and neither is this caller's to repair.
				return nil
			}
			return err
		}
		claimed, won = updated, true
		return nil
	})
	return claimed, won, err
}

// releaseRefresh drops the lease without recording a rotation, for the case
// where nothing was spent.
func releaseRefresh(ctx context.Context, rt extension.Runtime, conn connection) (connection, error) {
	var released connection
	err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		updated, err := scanConnection(tx.QueryRow(ctx,
			`UPDATE `+connectionTable+`
			    SET refresh_claimed_at = NULL, version = version + 1, updated_at = now()
			  WHERE id = $1::uuid
			 RETURNING `+connectionColumns, conn.ID).Scan)
		if err != nil {
			if isNoRows(err) {
				return nil
			}
			return err
		}
		released = updated
		return nil
	})
	if err != nil {
		return conn, err
	}
	if released.ID == "" {
		return conn, nil
	}
	return released, nil
}

// completeRefresh records a rotation that succeeded and was kept: the new
// expiry, the lease released, and a ledger row saying when this connection last
// renewed.
//
// It is recorded rather than treated as bookkeeping because of what the class
// refresh_rotation_lost means: between an event that says "renewed at 04:11" and
// a status that says "the replacement was not kept", a human can always tell
// which side of a single-use rotation a connection is on.
func completeRefresh(ctx context.Context, rt extension.Runtime, conn connection, expiresAt time.Time) (connection, error) {
	var after connection
	err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		updated, err := scanConnection(tx.QueryRow(ctx,
			`UPDATE `+connectionTable+`
			    SET access_token_expires_at = $2,
			        refresh_claimed_at = NULL,
			        last_error_class = NULL,
			        version = version + 1,
			        updated_at = now()
			  WHERE id = $1::uuid
			 RETURNING `+connectionColumns, conn.ID, expiresAt).Scan)
		if err != nil {
			if isNoRows(err) {
				// Disconnected mid-renewal. The sealed pair is already gone with
				// it, and writing this would recreate nothing.
				return nil
			}
			return err
		}
		after = updated
		return recordConnection(ctx, tx, extension.AuditUpdate, eventCredentialRotated, &conn, &updated)
	})
	if err != nil {
		return conn, err
	}
	if after.ID == "" {
		return conn, nil
	}
	return after, nil
}

// park stops this connection with a class a screen can render and a human can
// act on, releasing the lease so the row does not also look busy.
//
// It is a SEPARATE transaction from whatever failed, deliberately: the write
// that must record a failure cannot be the write that failed.
func park(ctx context.Context, rt extension.Runtime, conn connection, class, status string) (connection, error) {
	var parked connection
	err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		updated, err := scanConnection(tx.QueryRow(ctx,
			`UPDATE `+connectionTable+`
			    SET status = $2, last_error_class = $3, refresh_claimed_at = NULL,
			        last_polled_at = now(), version = version + 1, updated_at = now()
			  WHERE id = $1::uuid
			 RETURNING `+connectionColumns, conn.ID, status, class).Scan)
		if err != nil {
			if isNoRows(err) {
				return nil
			}
			return err
		}
		parked = updated
		return recordConnection(ctx, tx, extension.AuditUpdate, parkEvent(status), &conn, &updated)
	})
	if err != nil {
		return conn, err
	}
	if parked.ID == "" {
		return conn, nil
	}
	return parked, nil
}

// parkEvent names what listeners hear when a connection stops.
func parkEvent(status string) string {
	if status == statusTierLapse {
		return eventTierLapsed
	}
	return eventReauth
}
