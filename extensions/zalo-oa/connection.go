// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalooa

// The connection row, and the three operations that do not involve a browser:
// starting an authorization, reporting one, and withdrawing it. Redeeming what
// the browser comes back with is connect.go.
//
// The flow is in TWO halves because a human leaves the building in the middle of
// it. `authorize` mints the PKCE material, seals it, and hands back a URL; an OA
// administrator opens that URL, clicks *Cho phép*, and Zalo redirects with a code
// that is single-use and dies in ten minutes. The split is the provider's, not a
// preference: there is no client-credentials grant for an Official Account, and
// nothing here can create one.
//
// EVERY operation takes the member from the INVOCATION and never from the
// request body. Whoever completes an authorization becomes `authorized_by`, whose
// sealed credential the poll spends and on whose live authority every record
// lands — so a body-supplied user id here would let one member deposit a
// credential in a colleague's name and forge, through this unit's own front door,
// the consent the ingress port checks.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// The four statuses the row admits, matching the column's CHECK.
const (
	statusPending   = "pending_authorization"
	statusConnected = "connected"
	statusReauth    = "reauth_required"
	statusTierLapse = "tier_lapsed"
)

// The bounds on what an admin may paste. Each is a cap on a field that is
// stored, rendered or sent, and none of them is a format claim: an app id and a
// secret are opaque to this unit, so the only honest checks are that they are
// there and are not a paste of something else entirely.
const (
	maxAppIDBytes       = 128
	maxAppSecretBytes   = 512
	maxRedirectURIBytes = 2048
	maxAuthCodeBytes    = 1024
	maxOAIDBytes        = 64
)

// connectionColumns is the projection every read and every write returns, in one
// place so a column added to the table is one edit rather than six.
const connectionColumns = `id::text, coalesce(oa_id, ''), app_id, redirect_uri,
	authorized_by::text, status, coalesce(account_label, ''), coalesce(package_name, ''),
	coalesce(package_valid_through, ''), access_token_expires_at, refresh_claimed_at,
	high_water_mark, coalesce(backfill_before, 0), coalesce(pending_high_water_mark, 0),
	backfill_offset, last_polled_at, coalesce(last_error_class, ''), version`

// connection is this installation's Zalo connection, as the unit reads and
// renders it.
//
// IT CARRIES NO TOKEN, and cannot: the columns do not exist. What the screen
// shows about a credential is that one is on deposit and when its access half
// expires, which is what a row here means.
type connection struct {
	ID string `json:"id"`
	// OAID is Zalo's own id for the account, and empty until the admin returns
	// from the browser with it.
	OAID        string `json:"oa_id,omitempty"`
	AppID       string `json:"app_id"`
	RedirectURI string `json:"redirect_uri"`
	// AuthorizedBy is the admin whose grant this is: whose sealed token the poll
	// spends, and whose live authority every landed record runs under.
	AuthorizedBy string `json:"authorized_by"`
	Status       string `json:"status"`
	// AccountLabel, PackageName and PackageValidThrough are what the provider
	// says NOW, refreshed on every poll. PackageName is a localized display
	// string shown to an admin as EVIDENCE and compared against nothing.
	AccountLabel        string `json:"account_label,omitempty"`
	PackageName         string `json:"package_name,omitempty"`
	PackageValidThrough string `json:"package_valid_through,omitempty"`
	// AccessTokenExpiresAt lets a screen say when the credential renews without
	// unsealing it. It is mirrored from the sealed document, never a source.
	AccessTokenExpiresAt string `json:"access_token_expires_at,omitempty"`
	// RefreshClaimedAt is the renewal lease: set while one caller is at the
	// token endpoint, and empty otherwise. It is rendered because "a renewal is
	// in flight" is the difference between a connection that is stuck and one
	// that is a few seconds from being fine.
	RefreshClaimedAt string `json:"refresh_claimed_at,omitempty"`
	// The cursor's three parts plus its resume hint; walk.go owns what they
	// mean. Zero means "none" for the middle two, which is why the read
	// coalesces the nulls away: the screen renders a number or nothing, and a
	// pointer here would buy a distinction nobody displays.
	HighWaterMark        int64  `json:"high_water_mark"`
	BackfillBefore       int64  `json:"backfill_before,omitempty"`
	PendingHighWaterMark int64  `json:"pending_high_water_mark,omitempty"`
	BackfillOffset       int    `json:"backfill_offset"`
	LastPolledAt         string `json:"last_polled_at,omitempty"`
	// LastErrorClass is a class this unit chose, never a provider's own message:
	// it is rendered, and a remote party's prose is not this installation's to
	// display.
	LastErrorClass string `json:"last_error_class,omitempty"`
	Version        int    `json:"version"`
}

// cursor is where this connection has read to, as the walk reasons about it.
func (c connection) cursor() cursor {
	return cursor{
		floor:  c.HighWaterMark,
		gap:    c.BackfillBefore,
		top:    c.PendingHighWaterMark,
		offset: c.BackfillOffset,
	}
}

// scanConnection reads connectionColumns off one row.
//
// The two timestamps are scanned as TIMES and rendered afterwards, not scanned
// as text: the columns are timestamptz and the driver refuses to put one into a
// string. The rendering is RFC 3339 because that is what the contract declares
// and what the screen's formatter parses.
func scanConnection(scan func(...any) error) (connection, error) {
	var (
		c            connection
		expiresAt    *time.Time
		claimedAt    *time.Time
		lastPolledAt *time.Time
	)
	err := scan(&c.ID, &c.OAID, &c.AppID, &c.RedirectURI, &c.AuthorizedBy, &c.Status,
		&c.AccountLabel, &c.PackageName, &c.PackageValidThrough, &expiresAt, &claimedAt,
		&c.HighWaterMark, &c.BackfillBefore, &c.PendingHighWaterMark, &c.BackfillOffset,
		&lastPolledAt, &c.LastErrorClass, &c.Version)
	if err != nil {
		return connection{}, err
	}
	if expiresAt != nil {
		c.AccessTokenExpiresAt = expiresAt.UTC().Format(time.RFC3339)
	}
	if claimedAt != nil {
		c.RefreshClaimedAt = claimedAt.UTC().Format(time.RFC3339)
	}
	if lastPolledAt != nil {
		c.LastPolledAt = lastPolledAt.UTC().Format(time.RFC3339)
	}
	return c, nil
}

// authorize starts the browser round trip: it seals what the exchange will need
// and returns the URL an OA admin opens.
//
// NOTHING IS SPENT HERE and no account is reached. What it produces is a
// verifier sealed under the caller, a row in `pending_authorization`, and two
// strings for the screen: the permission URL, and the code challenge the admin
// must save in their developer console.
//
// The challenge is returned rather than only used because of a real tension in
// this provider's design: the console stores ONE code challenge in the app's
// settings, so the "a fresh verifier per request" advice its own documentation
// gives cannot be followed through the console path. An admin therefore pastes
// the challenge once per authorization. Saying so on the screen is the honest
// handling; silently reusing a verifier across authorizations would be the
// alternative, and a PKCE verifier that never changes is not one.
func authorize(ctx context.Context, rt extension.Runtime, in json.RawMessage) (json.RawMessage, error) {
	args, err := extension.DecodeArgs[struct {
		AppID       string `json:"app_id"`
		AppSecret   string `json:"app_secret"`
		RedirectURI string `json:"redirect_uri"`
	}](in)
	if err != nil {
		return nil, err
	}
	admin, err := callingAdmin(rt, "starting an authorization")
	if err != nil {
		return nil, err
	}
	appID, err := boundedSecretish(args.AppID, maxAppIDBytes, "the App ID from developers.zalo.me")
	if err != nil {
		return nil, err
	}
	appSecret, err := boundedSecretish(args.AppSecret, maxAppSecretBytes, "the App Secret from developers.zalo.me")
	if err != nil {
		return nil, err
	}
	redirect, err := connectableRedirect(args.RedirectURI)
	if err != nil {
		return nil, err
	}
	verifier, err := newVerifier()
	if err != nil {
		return nil, err
	}
	// The two secrets go down BEFORE the row, for the reason every deposit in
	// this tree does: a row with no verifier sealed is an authorization whose
	// code can never be redeemed, and it looks live on the screen until the
	// admin comes back from the browser and finds out. A sealed verifier with no
	// row is invisible and costs nothing.
	if err := rt.Secrets().Put(ctx, appSecretKey, []byte(appSecret)); err != nil {
		return nil, err
	}
	if err := rt.Secrets().PutUser(ctx, admin, verifierKey, []byte(verifier)); err != nil {
		return nil, err
	}
	// The state is the CSRF binding between the URL handed out and the redirect
	// that comes back. It is the connection row's own id, which is unguessable,
	// already stored, and — unlike a second random value — cannot go missing
	// while the row it belongs to survives.
	var stored connection
	err = rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		before, err := currentConnection(ctx, tx)
		if err != nil {
			return err
		}
		stored, err = scanConnection(tx.QueryRow(ctx,
			`INSERT INTO `+connectionTable+` (workspace_id, app_id, redirect_uri, authorized_by)
			 VALUES (`+callerWorkspace+`, $1, $2, $3::uuid)
			 ON CONFLICT (workspace_id) DO UPDATE
			    SET app_id = EXCLUDED.app_id,
			        redirect_uri = EXCLUDED.redirect_uri,
			        authorized_by = EXCLUDED.authorized_by,
			        status = '`+statusPending+`',
			        last_error_class = NULL,
			        version = `+connectionTable+`.version + 1,
			        updated_at = now()
			 RETURNING `+connectionColumns, appID, redirect, string(admin)).Scan)
		if err != nil {
			return err
		}
		return recordConnection(ctx, tx, auditActionFor(before), eventAuthorizationStarted, before, &stored)
	})
	if err != nil {
		return nil, err
	}
	challenge := challengeFor(verifier)
	link, err := permissionLink(appID, redirect, challenge, stored.ID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		PermissionURL string     `json:"permission_url"`
		CodeChallenge string     `json:"code_challenge"`
		Connection    connection `json:"connection"`
	}{PermissionURL: link, CodeChallenge: challenge, Connection: stored})
}

// status answers what this installation's connection is doing.
//
// Unlike the sibling connector's, this is not one member's own row — there is one
// connection per installation and the operation is gated on an RBAC object no
// seeded role holds. What it discloses is which account is connected, who
// authorized it, and how the poll is faring: administration facts, for the people
// an operator granted administration to.
func status(ctx context.Context, rt extension.Runtime, in json.RawMessage) (json.RawMessage, error) {
	if _, err := extension.DecodeArgs[struct{}](in); err != nil {
		return nil, err
	}
	var found *connection
	err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		var err error
		found, err = currentConnection(ctx, tx)
		return err
	})
	if err != nil {
		return nil, err
	}
	// No connection is `connected: false`, not an error: not having connected yet
	// is the ordinary state of this screen, and the caller is asking exactly that
	// question.
	return json.Marshal(struct {
		Connected  bool        `json:"connected"`
		Connection *connection `json:"connection,omitempty"`
	}{Connected: found != nil && found.Status == statusConnected, Connection: found})
}

// disconnect removes the connection and the credential behind it.
//
// BOTH, and the row goes rather than taking a fifth status: a "disconnected" row
// would be a connection the poll skips and the screen shows as absent, while the
// ingress port would still read the deposited credential as consent. Deleting
// the credential is what actually ends the authority; deleting the row is what
// stops the poll finding it.
//
// The token deleted is the AUTHORIZING ADMIN's, taken from the row and not from
// the caller: any admin holding this unit's object may disconnect, and one who
// deleted only their own deposit would leave a live credential behind for a
// connection that no longer exists.
func disconnect(ctx context.Context, rt extension.Runtime, in json.RawMessage) (json.RawMessage, error) {
	if _, err := extension.DecodeArgs[struct{}](in); err != nil {
		return nil, err
	}
	var existing *connection
	if err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		var err error
		existing, err = currentConnection(ctx, tx)
		return err
	}); err != nil {
		return nil, err
	}
	if existing == nil {
		return json.Marshal(struct {
			Disconnected bool `json:"disconnected"`
		}{Disconnected: false})
	}
	// The credential first, for the reason the deposits go first: if the row
	// survives a failure here the poll keeps running against a token the operator
	// asked to withdraw, which is the one ordering that leaves authority behind.
	if err := forgetCredential(ctx, rt, extension.UserID(existing.AuthorizedBy)); err != nil {
		return nil, err
	}
	err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		gone, err := scanConnection(tx.QueryRow(ctx,
			`DELETE FROM `+connectionTable+` WHERE id = $1::uuid RETURNING `+connectionColumns,
			existing.ID).Scan)
		if err != nil {
			if isNoRows(err) {
				// Another admin disconnected between the two transactions. The
				// credential is gone either way, which is the outcome asked for.
				return nil
			}
			return err
		}
		return recordConnection(ctx, tx, extension.AuditErase, eventDisconnected, &gone, nil)
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Disconnected bool `json:"disconnected"`
	}{Disconnected: true})
}

// currentConnection reads this installation's connection, or nothing.
//
// It names no workspace: the Runtime pins the transaction to the invocation's
// tenant before the first statement runs, the table's policy holds it, and the
// row is unique per workspace — so "the one row" is unambiguous.
func currentConnection(ctx context.Context, tx extension.Tx) (*connection, error) {
	found, err := scanConnection(tx.QueryRow(ctx,
		`SELECT `+connectionColumns+` FROM `+connectionTable).Scan)
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	return &found, nil
}

// callingAdmin is the human behind the invocation, refused when there is none.
func callingAdmin(rt extension.Runtime, doing string) (extension.UserID, error) {
	member := rt.Caller().UserID
	if member == "" {
		// A job tick and a bus delivery both answer the zero Caller. Neither can
		// authorize an account, because there is nobody whose grant it would be.
		return "", fmt.Errorf("%w: %s is something a person does, and this invocation has nobody behind it", extension.ErrForbidden, doing)
	}
	return extension.UserID(member), nil
}

// auditActionFor reports whether this write created the row or updated one.
// Recording an update as a create would put a ledger row with no before-image
// over a state that existed, which the ledger's own grammar refuses.
func auditActionFor(before *connection) extension.AuditAction {
	if before == nil {
		return extension.AuditCreate
	}
	return extension.AuditUpdate
}

// isNoRows reports whether a single-row read matched nothing. It is the
// PUBLISHED sentinel: the core translates the driver's own wording into it
// precisely so a unit does not match on a driver's text.
func isNoRows(err error) bool {
	return errors.Is(err, extension.ErrNoRows)
}

// boundedSecretish checks that an opaque value an admin pasted is there and is
// not a paste of something else entirely. It cannot check a FORMAT, because
// these are the provider's identifiers and this unit has no grammar for them —
// so the refusal says what was expected rather than what the value should look
// like.
func boundedSecretish(raw string, cap int, what string) (string, error) {
	value := strings.TrimSpace(raw)
	switch {
	case value == "":
		return "", fmt.Errorf("%w: this needs %s", extension.ErrInvalid, what)
	case len(value) > cap:
		return "", fmt.Errorf("%w: %s is %d bytes, over the %d-byte cap — check that a whole page was not pasted", extension.ErrInvalid, what, len(value), cap)
	}
	return value, nil
}

// connectableRedirect validates where Zalo will send the admin's browser back
// to.
//
// This unit never dials it — it is handed to a browser — so what is checked is
// what a browser and the provider will do with it: https, because an
// authorization code travels on it; no credentials, query or fragment, because
// the provider appends its own and a URL carrying either would come back
// ambiguous.
func connectableRedirect(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("%w: this needs the address Zalo should send the admin's browser back to, which must also be saved as the OA Callback Url in the developer console", extension.ErrInvalid)
	}
	if len(value) > maxRedirectURIBytes {
		return "", fmt.Errorf("%w: the redirect address is %d bytes, over the %d-byte cap", extension.ErrInvalid, len(value), maxRedirectURIBytes)
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("%w: the redirect address is not a URL", extension.ErrInvalid)
	}
	switch {
	case parsed.Scheme != "https":
		return "", fmt.Errorf("%w: the redirect address must be https — a single-use authorization code travels back on it", extension.ErrInvalid)
	case parsed.Host == "":
		return "", fmt.Errorf("%w: the redirect address names no host", extension.ErrInvalid)
	case parsed.User != nil:
		return "", fmt.Errorf("%w: the redirect address carries credentials", extension.ErrInvalid)
	case parsed.RawQuery != "" || parsed.Fragment != "":
		return "", fmt.Errorf("%w: the redirect address carries a query or fragment, and Zalo appends its own — the code and oa_id would come back ambiguous", extension.ErrInvalid)
	}
	return parsed.String(), nil
}
