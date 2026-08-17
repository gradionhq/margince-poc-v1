// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalooa

// Redeeming what the browser came back with, and THE TIER GATE that stands
// between a grant and a working connection.
//
// It is a file of its own because the two halves of connecting are separated by
// a human leaving the building: connection.go prepares the trip and this one
// completes it. The gate is here rather than beside the row for the same reason —
// what it decides is whether an Official Account may be connected at all, which
// is a question about the ACCOUNT and not about this table.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// connect redeems the code the admin's browser came back with, gates on the
// account's package, and seals the grant.
//
// The ORDER is the whole handling of a half-connection. The code is redeemed,
// then the tier is probed with the token that came back, and only a connection
// that passes both is sealed and marked connected. A grant obtained and then
// refused by the gate leaves no token on deposit and the row in
// `pending_authorization` — which is honest, because a free-tier OA is not
// connected, and leaving the credential sealed would make the screen say it was.
func connect(ctx context.Context, rt extension.Runtime, in json.RawMessage) (json.RawMessage, error) {
	return connectVia(ctx, rt, in, newClient, newOAuthClient())
}

func connectVia(ctx context.Context, rt extension.Runtime, in json.RawMessage,
	dial clientFactory, grants grantExchanger,
) (json.RawMessage, error) {
	args, err := extension.DecodeArgs[struct {
		Code  string `json:"code"`
		OAID  string `json:"oa_id"`
		State string `json:"state"`
	}](in)
	if err != nil {
		return nil, err
	}
	admin, err := callingAdmin(rt, "completing an authorization")
	if err != nil {
		return nil, err
	}
	code, err := boundedSecretish(args.Code, maxAuthCodeBytes, "the code Zalo redirected back with")
	if err != nil {
		return nil, err
	}
	oaID, err := boundedSecretish(args.OAID, maxOAIDBytes, "the oa_id Zalo redirected back with")
	if err != nil {
		return nil, err
	}
	pending, err := pendingAuthorization(ctx, rt, strings.TrimSpace(args.State))
	if err != nil {
		return nil, err
	}
	granted, err := redeem(ctx, rt, grants, pending, admin, code)
	if err != nil {
		return nil, err
	}
	// The gate runs on the token that was just issued, against the account that
	// was just named. A refusal here is the admin's to act on and each kind costs
	// them something different — see admitTier.
	label, err := admitTier(ctx, dial(granted.AccessToken))
	if err != nil {
		return nil, err
	}
	if err := sealTokens(ctx, rt, admin, granted); err != nil {
		return nil, err
	}
	stored, err := markConnected(ctx, rt, pending, admin, oaID, label, granted.ExpiresAt)
	if err != nil {
		return nil, err
	}
	// The verifier has done its work and is single-use by construction: the code
	// it redeemed is dead. Keeping it would leave PKCE material on deposit for an
	// authorization that has completed.
	if err := rt.Secrets().DeleteUser(ctx, admin, verifierKey); err != nil &&
		!errors.Is(err, extension.ErrSecretNotFound) {
		return nil, err
	}
	return json.Marshal(stored)
}

// redeem exchanges the code, using the verifier this caller sealed.
//
// It refuses when the verifier is gone, and the refusal says what to do: a code
// cannot be redeemed without the verifier that minted its challenge, so the
// remedy is a new authorization rather than a retry of this one.
func redeem(ctx context.Context, rt extension.Runtime, grants grantExchanger,
	pending connection, admin extension.UserID, code string,
) (tokenPair, error) {
	verifier, err := rt.Secrets().GetUser(ctx, admin, verifierKey)
	if err != nil {
		if errors.Is(err, extension.ErrSecretNotFound) {
			return tokenPair{}, fmt.Errorf("%w: this authorization has no code verifier on deposit, so its code cannot be redeemed — start the authorization again", extension.ErrInvalid)
		}
		return tokenPair{}, err
	}
	appSecret, err := rt.Secrets().Get(ctx, appSecretKey)
	if err != nil {
		if errors.Is(err, extension.ErrSecretNotFound) {
			return tokenPair{}, fmt.Errorf("%w: this installation has no app secret on deposit — start the authorization again", extension.ErrInvalid)
		}
		return tokenPair{}, err
	}
	return grants.Redeem(ctx, pending.AppID, string(appSecret), code, string(verifier))
}

// admitTier is THE TIER GATE, and it is a capability probe rather than a name
// match.
//
// `getoa` proves the token and yields what an admin reads; one `listrecentchat`
// call decides. That ordering matters: the package name Zalo returns is a
// LOCALIZED VIETNAMESE DISPLAY STRING that it can rename or extend, so a
// connector gating on it would refuse a paying customer the day the tier was
// renamed. Asking the account whether it can do the thing this unit needs is a
// question with one answer that cannot go stale.
//
// The two refusals are told apart because they cost different things. `-224` is
// the OA's package and costs 2,500,000 đ a year. `-212` is a permission group the
// developer app has not enabled, and costs a click. Telling an admin to buy an
// upgrade when they need to toggle a switch is the failure the whole error
// catalog exists to prevent.
func admitTier(ctx context.Context, api *client) (oaProfile, error) {
	found, err := api.profile(ctx)
	if err != nil {
		return oaProfile{}, tierRefusal(err, "reading the Official Account")
	}
	if _, err := api.recentChat(ctx, 0); err != nil {
		return oaProfile{}, tierRefusal(err, "reading the Official Account's conversations")
	}
	return found, nil
}

// tierRefusal words the gate's answer for the admin reading it.
func tierRefusal(cause error, doing string) error {
	switch {
	case errors.Is(cause, errTierTooLow):
		return fmt.Errorf("%w: this Official Account's package does not include the conversation API. Margince needs the paid tier (Tăng trưởng, 2.500.000 đ/year at the time of writing) — upgrade the OA at oa.zalo.me and connect again. The account's current package is shown on this screen once it connects", extension.ErrInvalid)
	case errors.Is(cause, errAPINotRegistered):
		return fmt.Errorf("%w: the Zalo app has not registered the API group this needs, which is a FREE setting and not an upgrade: open developers.zalo.me, and under Sản phẩm → Official Account enable Quản lý thông tin OA, Quản lý tin nhắn người dùng, Gửi tin nhắn and Quản lý người dùng, then connect again", extension.ErrInvalid)
	case errors.Is(cause, errUnauthorized):
		return fmt.Errorf("%w: Zalo did not accept the token this authorization produced — start the authorization again, and check that the code challenge saved in the developer console is the one this screen showed", extension.ErrInvalid)
	case errors.Is(cause, errTransient):
		return fmt.Errorf("zalo-oa: %s did not answer — try connecting again in a moment: %w", doing, cause)
	default:
		return fmt.Errorf("zalo-oa: %s: %w", doing, cause)
	}
}

// markConnected flips the pending row to a live connection.
func markConnected(ctx context.Context, rt extension.Runtime, pending connection,
	admin extension.UserID, oaID string, label oaProfile, expiresAt time.Time,
) (connection, error) {
	var stored connection
	err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		updated, err := scanConnection(tx.QueryRow(ctx,
			`UPDATE `+connectionTable+`
			    SET oa_id = $2,
			        authorized_by = $3::uuid,
			        status = '`+statusConnected+`',
			        account_label = $4,
			        package_name = $5,
			        package_valid_through = $6,
			        access_token_expires_at = $7,
			        last_error_class = NULL,
			        -- The cursor is reset when the ACCOUNT changes, and survives
			        -- a re-authorization of the same one. A message time from one
			        -- OA means nothing against another's log, and keeping it
			        -- would put the floor above everything the new account has
			        -- ever said — a connection that looks healthy and lands
			        -- nothing, forever.
			        --
			        -- The unqualified names on the right are the row's OLD values:
			        -- that is what an UPDATE's SET expressions read, so the table
			        -- does not have to be spelled again to reach them.
			        --
			        -- The account is compared through coalesce rather than
			        -- IS NOT DISTINCT FROM, which reads identically here because
			        -- $2 is never empty. It also keeps the word FROM out of a
			        -- place where a reader has to decide whether a table follows
			        -- it — which the tree's SQL-scope gate does, and got wrong.
			        high_water_mark = CASE WHEN coalesce(oa_id, '') = $2
			                               THEN high_water_mark ELSE 0 END,
			        backfill_before = CASE WHEN coalesce(oa_id, '') = $2
			                               THEN backfill_before ELSE NULL END,
			        pending_high_water_mark = CASE WHEN coalesce(oa_id, '') = $2
			                               THEN pending_high_water_mark ELSE NULL END,
			        backfill_offset = CASE WHEN coalesce(oa_id, '') = $2
			                               THEN backfill_offset ELSE 0 END,
			        version = version + 1,
			        updated_at = now()
			  WHERE id = $1::uuid
			 RETURNING `+connectionColumns,
			pending.ID, oaID, string(admin), label.Name, label.PackageName,
			label.PackageValidThroughDate, expiresAt).Scan)
		if err != nil {
			return err
		}
		stored = updated
		return recordConnection(ctx, tx, extension.AuditUpdate, eventConnected, &pending, &updated)
	})
	return stored, err
}

// pendingAuthorization reads the row a redemption must belong to, and checks the
// state the redirect carried against it.
//
// The state check is a REFUSAL rather than a warning: it is the binding between
// the URL this installation handed out and the browser that came back, and a
// code arriving under another row's state is either a stale tab or somebody
// else's redirect. Both are answered the same way, because this side cannot tell
// them apart and only one of them is harmless.
func pendingAuthorization(ctx context.Context, rt extension.Runtime, state string) (connection, error) {
	var found *connection
	if err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		var err error
		found, err = currentConnection(ctx, tx)
		return err
	}); err != nil {
		return connection{}, err
	}
	if found == nil {
		return connection{}, fmt.Errorf("%w: no authorization is in progress for this installation — start one before redeeming a code", extension.ErrInvalid)
	}
	if state != found.ID {
		return connection{}, fmt.Errorf("%w: this redirect belongs to a different authorization than the one in progress — it is probably a stale browser tab, so start the authorization again from this screen", extension.ErrInvalid)
	}
	return *found, nil
}
