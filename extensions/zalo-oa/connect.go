// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalooa

// Connecting an Official Account: the tier gate, the first renewal, and the row.
//
// WHY THIS TAKES A TOKEN PAIR RATHER THAN CONDUCTING A BROWSER FLOW. Zalo has no
// client-credentials grant — an OA administrator has to open a permission URL and
// click *Cho phép*, and what returns is a code that is single-use and lives ten
// minutes. That trip mints the FIRST pair and nothing else; from then on the pair
// renews itself and the browser is never involved again.
//
// So the product does not conduct it. An administrator runs the authorization
// once with a tool (`.tmp/zalo-oa/poc`, which does PKCE, the permission URL and
// the code exchange end to end against the live service) and brings the pair
// here. What that buys is not only less code:
//
//   - The one genuinely ambiguous parameter in this provider's documentation —
//     whether `code_challenge` is base64 or base64URL, which differ most of the
//     time and so fail INTERMITTENTLY against a ten-minute code — stops being
//     something a connect flow can get wrong in production.
//   - The developer console stores ONE code challenge per APPLICATION rather
//     than one per request, so a product minting a fresh verifier each time would
//     have to ask an administrator to paste the challenge back into the console
//     before every authorization. That is a wart with no honest wording.
//   - The redirect address no longer has to match a console setting exactly, in
//     two places, or fail silently.
//
// WHAT IT COSTS, stated rather than discovered: onboarding is no longer
// self-serve. Every installation connecting its own account needs somebody who
// can run that tool. For one account that is nothing; if self-serve onboarding
// ever matters, the browser flow comes back here and everything else in this unit
// is unaffected by it.
//
// THE ORDER BELOW IS THE WHOLE HANDLING OF A HALF-CONNECTION, and each step is
// where it is for a reason:
//
//  1. The tier gate runs FIRST, on the access token as pasted. A free-tier
//     account is refused before anything is spent — gating after the renewal
//     would burn an administrator's single-use refresh token to tell them they
//     cannot use the product.
//  2. The refresh token is then SPENT IMMEDIATELY, once. That proves it works in
//     front of the human who brought it rather than twenty-five hours later, it
//     replaces a pasted expiry nobody knows with one the provider stated, and it
//     takes the credential out of the custody of whatever tool produced it — so
//     nothing outside this installation is left holding a live pair that a
//     second use would rotate out from under the connection.
//  3. Only then is anything sealed or written.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// connect gates the account, renews the credential once, and records the
// connection.
func connect(ctx context.Context, rt extension.Runtime, in json.RawMessage) (json.RawMessage, error) {
	return connectVia(ctx, rt, in, newClient, newOAuthClient(), time.Now)
}

func connectVia(ctx context.Context, rt extension.Runtime, in json.RawMessage,
	dial clientFactory, grants grantExchanger, now clock,
) (json.RawMessage, error) {
	args, err := extension.DecodeArgs[struct {
		AppID        string `json:"app_id"`
		AppSecret    string `json:"app_secret"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}](in)
	if err != nil {
		return nil, err
	}
	admin, err := callingAdmin(rt, "connecting an Official Account")
	if err != nil {
		return nil, err
	}
	pasted, err := pastedGrantOf(args.AppID, args.AppSecret, args.AccessToken, args.RefreshToken)
	if err != nil {
		return nil, err
	}
	label, renewed, resumed, err := admitAndTakeCustody(ctx, rt, dial, grants, admin, pasted, now())
	if err != nil {
		return nil, err
	}
	stored, superseded, err := recordConnected(ctx, rt, admin, pasted.appID, resumed, label, renewed.ExpiresAt)
	if err != nil {
		return nil, err
	}
	// A DEPOSIT EXISTS ONLY FOR THE ROW'S CURRENT authorized_by. Connecting as a
	// different administrator leaves the previous one's sealed pair with nothing
	// referencing it — and the ingress port reads a deposit as live consent to act
	// for that person, so what would be left behind is not a stray blob but a
	// standing authority no screen shows and no disconnect reaches (a disconnect
	// withdraws the row's CURRENT administrator, who is by then somebody else).
	//
	// It is withdrawn after the row is written, because the write is what decides
	// whether the repointing happened at all.
	if superseded != "" && superseded != string(admin) {
		if err := forgetCredential(ctx, rt, extension.UserID(superseded)); err != nil {
			return nil, err
		}
	}
	return json.Marshal(stored)
}

// pastedGrant is what an administrator brings back from the authorization, bound
// and trimmed.
//
// The four values are opaque to this unit — they are the provider's identifiers
// and its credentials — so the only honest checks are that each is there and that
// none is a paste of something else entirely. What they MEAN is established by
// spending them, two calls later, which is the point of spending them here.
type pastedGrant struct {
	appID        string
	appSecret    string
	accessToken  string
	refreshToken string
}

// pastedGrantOf bounds each of the four values an administrator supplies.
func pastedGrantOf(appID, appSecret, accessToken, refreshToken string) (pastedGrant, error) {
	var (
		grant pastedGrant
		err   error
	)
	if grant.appID, err = boundedSecretish(appID, maxAppIDBytes, "the App ID from developers.zalo.me"); err != nil {
		return pastedGrant{}, err
	}
	if grant.appSecret, err = boundedSecretish(appSecret, maxAppSecretBytes, "the App Secret from developers.zalo.me"); err != nil {
		return pastedGrant{}, err
	}
	if grant.accessToken, err = boundedSecretish(accessToken, maxTokenBytes, "the access token from the authorization"); err != nil {
		return pastedGrant{}, err
	}
	if grant.refreshToken, err = boundedSecretish(refreshToken, maxTokenBytes, "the refresh token from the authorization"); err != nil {
		return pastedGrant{}, err
	}
	return grant, nil
}

// admitAndTakeCustody gates the account, renews the credential, and KEEPS what
// it renewed before anything else can fail.
//
// THE TWO HALVES OF A PAIR AGE VERY DIFFERENTLY, and that is what shapes this.
// An access token lasts about 25 hours; a refresh token lasts three months. So a
// pair that has been sitting in a file since yesterday — the ORDINARY state of
// anything a human carries from one tool to another — has a dead access half and
// a perfectly good refresh half.
//
// Gating on the pasted access token alone would therefore refuse almost every
// real pair. Renewing first instead would fix that and spend a single-use refresh
// token before knowing whether the account can be used at all. So the gate is
// tried as pasted, and only if Zalo says that token is no longer accepted is the
// credential renewed and the gate tried again on what comes back.
//
// THE SEALING SITS BETWEEN THE TWO, and that ordering is the whole point of this
// function existing rather than being three lines in connectVia. Once the
// provider has rotated, the pasted refresh token is dead whatever happens next —
// so a tier refusal AFTER the renewal must not also destroy the replacement. It
// is kept first; the refusal then leaves an installation holding a usable pair, a
// human who can fix the package or the console toggle, and a reconnect that
// resumes from what is held. Refusing before keeping would hand somebody a
// remedy they cannot execute: "connect again" with a pair that no longer exists.
func admitAndTakeCustody(ctx context.Context, rt extension.Runtime, dial clientFactory,
	grants grantExchanger, admin extension.UserID, pasted pastedGrant, now time.Time,
) (oaProfile, tokenPair, bool, error) {
	pastedLabel, err := probeAccount(ctx, dial(pasted.accessToken))
	staleAccess := errors.Is(err, errUnauthorized)
	if err != nil && !staleAccess {
		return oaProfile{}, tokenPair{}, false, tierRefusal(err, "reading the Official Account")
	}
	renewed, resumed, err := renewOrResume(ctx, rt, grants, admin, pasted, now)
	if err != nil {
		return oaProfile{}, tokenPair{}, false, err
	}
	if err := keepCredentials(ctx, rt, admin, pasted, renewed, resumed); err != nil {
		return oaProfile{}, tokenPair{}, false, err
	}
	// THE ACCOUNT IS READ FROM THE CREDENTIAL THAT WILL BE SPENT, always, and the
	// probe above is discarded as evidence of anything but the tier.
	//
	// Nothing ties the four pasted values to one account: an access token for one
	// Official Account and an app-and-refresh-token for another are four
	// well-formed strings, and each half would pass its own check. Reading the
	// label from the pasted token while sealing the pair from the exchange would
	// write a row whose oa_id names X over a credential that answers for Y — and
	// that id is the namespace under which every key, thread key and person
	// binding is written, and the prefix accountWithinOA lets a reply through on.
	// The poll reconciles the two on its next tick, but a reply staged before then
	// reaches whoever holds that number at the OTHER account.
	label, err := probeAccount(ctx, dial(renewed.AccessToken))
	if err != nil {
		return oaProfile{}, tokenPair{}, false, tierRefusal(err, "reading the Official Account")
	}
	// And when the pasted token DID name an account, the two must agree. Refusing
	// beats connecting the one the credential happens to answer for: a caller who
	// supplied a token for X and is silently given Y ends up administering
	// something they never asked for and cannot see they asked for.
	if !staleAccess && label.OAID != pastedLabel.OAID {
		return oaProfile{}, tokenPair{}, false, fmt.Errorf("%w: the access token and the refresh token belong to different Official Accounts, so nothing has been connected — supply a pair from one authorization of one account", extension.ErrInvalid)
	}
	return label, renewed, resumed, nil
}

// keepCredentials seals what the connection will spend, before anything that
// could fail afterwards.
//
// The app secret is NOT overwritten on a resume, and that is the load-bearing
// half: a resumed connect is spending a pair this installation already held, and
// the secret that can renew that pair is the one already on deposit beside it.
// Writing the pasted one over it would seal an app secret that cannot renew the
// credential in use, and the next scheduled rotation would park a connection that
// was working.
func keepCredentials(ctx context.Context, rt extension.Runtime, admin extension.UserID,
	pasted pastedGrant, renewed tokenPair, resumed bool,
) error {
	if !resumed {
		if err := rt.Secrets().PutUser(ctx, admin, appSecretKey, []byte(pasted.appSecret)); err != nil {
			return err
		}
	}
	return sealTokens(ctx, rt, admin, renewed)
}

// renewOrResume spends the pasted refresh token — or, when the provider says that
// token is spent and this installation is already holding a usable pair, RESUMES
// from the pair it holds. It reports which of the two happened.
//
// The fallback exists because of what the ordering in admitAndTakeCustody costs
// when a step after the renewal fails. The credential is sealed before the row is
// written, deliberately: a row with nothing behind it reads as a live connection
// and polls nothing. But a rotation that succeeded and a row that did not leaves
// an installation holding the ONLY live pair for that account, with no row naming
// it — and the pasted token is dead, so a retry could not get past this line.
// Without the fallback the remedy is an OA administrator in a browser, for a
// failure that happened entirely on this side.
//
// It is reached ONLY by the two refusals that mean "this token is spent", never
// by every failure: a 503 at the token endpoint says nothing about the pasted
// pair, and resuming on one would quietly substitute a different credential for a
// caller who should simply try again.
func renewOrResume(ctx context.Context, rt extension.Runtime, grants grantExchanger,
	admin extension.UserID, pasted pastedGrant, now time.Time,
) (tokenPair, bool, error) {
	renewed, err := grants.Rotate(ctx, pasted.appID, pasted.appSecret, pasted.refreshToken)
	if err == nil {
		return renewed, false, nil
	}
	if !errors.Is(err, errNoGrant) && !errors.Is(err, errUnauthorized) {
		return tokenPair{}, false, firstRenewalRefusal(err)
	}
	held, heldErr := unsealTokens(ctx, rt, admin)
	if heldErr != nil || !held.usable(now) {
		return tokenPair{}, false, firstRenewalRefusal(err)
	}
	return held, true, nil
}

// probeAccount is THE TIER GATE, and it is a capability probe rather than a name
// match.
//
// `getoa` proves the token and yields what an administrator reads; one
// `listrecentchat` call decides. That ordering matters: the package name Zalo
// returns is a LOCALIZED VIETNAMESE DISPLAY STRING that it can rename or extend,
// so a connector gating on it would refuse a paying customer the day the tier was
// renamed. Asking the account whether it can do the thing this unit needs is a
// question with one answer that cannot go stale.
//
// The two refusals are told apart because they cost different things. `-224` is
// the account's package and costs 2,500,000 đ a year. `-212` is a permission
// group the developer app has not enabled, and costs a click. Telling an
// administrator to buy an upgrade when they need to toggle a switch is the
// failure the whole error catalog exists to prevent.
// It answers the provider's RAW class rather than a worded refusal, because its
// caller has to tell one of those classes apart from the rest: an access token
// Zalo no longer accepts is not a refusal to show anybody, it is a renewal
// waiting to happen.
func probeAccount(ctx context.Context, api *client) (oaProfile, error) {
	found, err := api.profile(ctx)
	if err != nil {
		return oaProfile{}, err
	}
	if _, err := api.recentChat(ctx, 0); err != nil {
		return oaProfile{}, err
	}
	return found, nil
}

// tierRefusal words the gate's answer for the administrator reading it.
func tierRefusal(cause error, doing string) error {
	switch {
	case errors.Is(cause, errTierTooLow):
		return fmt.Errorf("%w: this Official Account's package does not include the conversation API. Margince needs the paid tier (Tăng trưởng, 2.500.000 đ/year at the time of writing) — upgrade the account at oa.zalo.me and connect again. Its current package is shown on this screen once it connects", extension.ErrInvalid)
	case errors.Is(cause, errAPINotRegistered):
		return fmt.Errorf("%w: the Zalo app has not registered the API group this needs, which is a FREE setting and not an upgrade: open developers.zalo.me, and under Sản phẩm → Official Account enable Quản lý thông tin OA, Quản lý tin nhắn người dùng, Gửi tin nhắn and Quản lý người dùng, then connect again", extension.ErrInvalid)
	case errors.Is(cause, errUnauthorized):
		// Reached only when a RENEWED token is refused, since a merely expired
		// pasted one is renewed rather than reported.
		return fmt.Errorf("%w: Zalo did not accept the credential this connection renewed, which usually means the app and the Official Account do not belong together. Check that the App ID is the application the authorization was run through", extension.ErrInvalid)
	case errors.Is(cause, errTransient):
		return fmt.Errorf("zalo-oa: %s did not answer — try connecting again in a moment: %w", doing, cause)
	default:
		return fmt.Errorf("zalo-oa: %s: %w", doing, cause)
	}
}

// firstRenewalRefusal words the failure of the one renewal connecting performs.
//
// It is the CREDENTIAL's refusal here, unlike the same failure on a scheduled
// tick: a human is at the screen, they supplied the token seconds ago, and the
// remedy is to bring a working one rather than to wait for a retry. A refresh
// token is single-use, so the ordinary cause is that this one has been spent
// already — by the tool that produced it, or by a previous connect.
func firstRenewalRefusal(cause error) error {
	switch {
	case errors.Is(cause, errNoGrant), errors.Is(cause, errUnauthorized):
		return fmt.Errorf("%w: Zalo would not renew that refresh token, so nothing has been connected. A Zalo refresh token can only be used ONCE — if the tool that produced this pair has refreshed since, or this pair has been connected before, run the authorization again and use the pair it produces", extension.ErrInvalid)
	case errors.Is(cause, errUnanswered):
		// The request went out and no answer came back, so the token may have
		// rotated into a replacement nobody holds. Saying "try again" would invite
		// spending a token that is dead half the time.
		return fmt.Errorf("%w: Zalo never reported whether it renewed that refresh token, so nothing has been connected and the token cannot be trusted either way. Run the authorization again and connect with a fresh pair", extension.ErrInvalid)
	default:
		return fmt.Errorf("zalo-oa: renewing the credential: %w", cause)
	}
}

// recordConnected writes the connection, and answers who it superseded.
//
// The upsert is on the workspace, because an installation has one Official
// Account: connecting a second time REPLACES the first rather than adding to it,
// which is what makes "one account, one connection, every rep replying through
// it" true in the schema rather than only in the documentation.
func recordConnected(ctx context.Context, rt extension.Runtime, admin extension.UserID,
	pastedAppID string, resumed bool, label oaProfile, expiresAt time.Time,
) (connection, string, error) {
	var (
		stored     connection
		superseded string
	)
	err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		// FOR UPDATE, because what follows this transaction depends on who the
		// row named BEFORE it: two administrators connecting at once would
		// otherwise both read the same pre-image, and the second withdrawal would
		// take a credential nobody holds while leaving the one just superseded.
		before, err := lockedConnection(ctx, tx)
		if err != nil {
			return err
		}
		if before != nil {
			superseded = before.AuthorizedBy
		}
		// THE APP ID STORED IS THE ONE WHOSE SECRET IS ON DEPOSIT. On an ordinary
		// connect that is the pasted one, which was just sealed. On a RESUME the
		// pasted secret was deliberately not written (see keepCredentials), so the
		// app that can renew the credential in use is the one the row already
		// named — and storing the pasted id over it would leave the row describing
		// an application whose secret this installation does not hold.
		appID := pastedAppID
		if resumed && before != nil && before.AppID != "" {
			appID = before.AppID
		}
		stored, err = scanConnection(tx.QueryRow(ctx,
			`INSERT INTO `+connectionTable+` (workspace_id, oa_id, app_id, authorized_by,
			                                  account_label, package_name, package_valid_through,
			                                  access_token_expires_at)
			 VALUES (`+callerWorkspace+`, $1, $2, $3::uuid, $4, $5, $6, $7)
			 ON CONFLICT (workspace_id) DO UPDATE
			    SET oa_id = EXCLUDED.oa_id,
			        app_id = EXCLUDED.app_id,
			        authorized_by = EXCLUDED.authorized_by,
			        account_label = EXCLUDED.account_label,
			        package_name = EXCLUDED.package_name,
			        package_valid_through = EXCLUDED.package_valid_through,
			        access_token_expires_at = EXCLUDED.access_token_expires_at,
			        status = '`+statusConnected+`',
			        last_error_class = NULL,
			        refresh_claimed_at = NULL,
			        -- The cursor is reset when the ACCOUNT changes, and survives a
			        -- reconnection of the same one. A message time from one account
			        -- means nothing against another's log, and keeping it would put
			        -- the floor above everything the new account has ever said — a
			        -- connection that looks healthy and lands nothing, forever.
			        --
			        -- EVERY name on the right is TABLE-QUALIFIED, and in an upsert
			        -- that is not style. A bare column here is ambiguous between
			        -- this row and EXCLUDED — PostgreSQL refuses the statement with
			        -- 42702 rather than picking one — which is the opposite of a
			        -- plain UPDATE, where an unqualified name IS the old value.
			        --
			        -- The comparison avoids IS NOT DISTINCT FROM, which reads
			        -- identically here because neither side is ever null, and which
			        -- would put the word FROM where a reader has to decide whether
			        -- a table follows it.
			        high_water_mark = CASE WHEN `+connectionTable+`.oa_id = EXCLUDED.oa_id
			                               THEN `+connectionTable+`.high_water_mark ELSE 0 END,
			        backfill_before = CASE WHEN `+connectionTable+`.oa_id = EXCLUDED.oa_id
			                               THEN `+connectionTable+`.backfill_before ELSE NULL END,
			        pending_high_water_mark = CASE WHEN `+connectionTable+`.oa_id = EXCLUDED.oa_id
			                               THEN `+connectionTable+`.pending_high_water_mark ELSE NULL END,
			        backfill_offset = CASE WHEN `+connectionTable+`.oa_id = EXCLUDED.oa_id
			                               THEN `+connectionTable+`.backfill_offset ELSE 0 END,
			        version = `+connectionTable+`.version + 1,
			        updated_at = now()
			 RETURNING `+connectionColumns,
			label.OAID, appID, string(admin), label.Name, label.PackageName,
			label.PackageValidThroughDate, expiresAt).Scan)
		if err != nil {
			return err
		}
		return recordConnection(ctx, tx, auditActionFor(before), eventConnected, before, &stored)
	})
	return stored, superseded, err
}
