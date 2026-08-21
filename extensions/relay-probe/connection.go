// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package relayprobe

// The three operations a member drives from the screen: connect an account,
// see what the poll is doing, disconnect.
//
// None of them names a workspace — the Runtime pins the transaction to the
// invocation's tenant before the first statement runs, and the table's policy
// holds whatever SQL these functions write. All three take the member from the
// INVOCATION rather than from the request body, which is the rule that keeps
// this unit's own surface from forging the consent the ingress port checks.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// The two statuses the row admits, matching the column's CHECK.
const (
	statusConnected = "connected"
	statusReauth    = "reauth_required"
)

// maxTokenBytes bounds a deposited token. A Relay personal access token is
// 68 bytes; this leaves room for a longer format and refuses a paste of
// something that is not a token at all.
const maxTokenBytes = 512

// connectionColumns is the projection every read and every write returns, in
// one place so a column added to the table is one edit rather than five.
const connectionColumns = `id::text, user_id::text, base_url, status,
	coalesce(account_label, ''), coalesce(provider_workspace_id, ''),
	high_water_mark, coalesce(backfill_before, 0), coalesce(pending_high_water_mark, 0),
	last_polled_at, coalesce(last_error_class, ''), version`

// connection is one member's connection, as this unit reads and renders it.
//
// IT CARRIES NO TOKEN, and cannot: the column does not exist. What the screen
// shows about a credential is that one is on deposit, which is what a row here
// means.
type connection struct {
	ID     string `json:"id"`
	UserID string `json:"user_id"`
	// BaseURL is the deployment this member's token authenticates against.
	BaseURL string `json:"base_url"`
	Status  string `json:"status"`
	// AccountLabel and ProviderWorkspaceID are what the provider says this
	// account and its workspace are called, refreshed on every poll. Both are
	// empty until something has been read back, which is the state between
	// depositing a token and the first tick.
	AccountLabel        string `json:"account_label"`
	ProviderWorkspaceID string `json:"provider_workspace_id"`
	// HighWaterMark, BackfillBefore and PendingHighWaterMark are the cursor's
	// three parts; walk.go owns what they mean. Zero means "none" for the
	// latter two, which is why the read coalesces the nulls away: the screen
	// renders a number or nothing, and a pointer here would buy a distinction
	// nobody displays.
	HighWaterMark        int64  `json:"high_water_mark"`
	BackfillBefore       int64  `json:"backfill_before"`
	PendingHighWaterMark int64  `json:"pending_high_water_mark"`
	LastPolledAt         string `json:"last_polled_at,omitempty"`
	// LastErrorClass is a class this unit chose, never a provider's own
	// message: it is rendered, and a remote party's prose is not this
	// installation's to display.
	LastErrorClass string `json:"last_error_class,omitempty"`
	Version        int    `json:"version"`
}

// cursor is where this connection has read to, as the walk reasons about it.
func (c connection) cursor() cursor {
	return cursor{floor: c.HighWaterMark, gap: c.BackfillBefore, top: c.PendingHighWaterMark}
}

// scanConnection reads connectionColumns off one row.
//
// last_polled_at is scanned as a TIME and rendered afterwards, not scanned as
// text. The column is timestamptz, and the driver refuses to put one into a
// string — which no unit test caught, because a fake hands back whatever the
// fixture scripted while the driver decides by the column's real type. The
// rendering is RFC 3339 because that is what the contract declares and what the
// screen's formatter parses.
func scanConnection(scan func(...any) error) (connection, error) {
	var (
		c            connection
		lastPolledAt *time.Time
	)
	err := scan(&c.ID, &c.UserID, &c.BaseURL, &c.Status, &c.AccountLabel, &c.ProviderWorkspaceID,
		&c.HighWaterMark, &c.BackfillBefore, &c.PendingHighWaterMark, &lastPolledAt,
		&c.LastErrorClass, &c.Version)
	if err != nil {
		return connection{}, err
	}
	if lastPolledAt != nil {
		c.LastPolledAt = lastPolledAt.UTC().Format(time.RFC3339)
	}
	return c, nil
}

// connect deposits this member's token and records where their Relay is.
//
// THE MEMBER IS rt.Caller().UserID AND NOTHING ELSE. The operation declares no
// user argument and the strict decoder would refuse one, but the rule is
// written here because it is the load-bearing one: the ingress port treats "a
// user-scoped secret is on deposit with this unit" as the member's consent to
// be acted for, so an operation that let one member deposit a credential FOR
// another would forge that consent through this unit's own front door. It is
// the same rule as captured_by being stamped from the authenticated principal.
//
// Re-connecting REPLACES the token and leaves the cursor where it was — unless
// the deployment changed, and that exception is the load-bearing half.
//
// A member who rotates their token has not asked to re-read their inbox, and
// resetting would re-walk the whole feed for nothing. But a notification id
// belongs to ONE deployment: point the same row at another Relay and the old
// cursor is a number from a different sequence, almost certainly far above
// anything the new deployment has issued — so every walk would stop on its
// first item, the row would look healthy and freshly polled, and nothing would
// ever land again. The cursor is therefore reset exactly when base_url changes.
func connect(ctx context.Context, rt extension.Runtime, in json.RawMessage) (json.RawMessage, error) {
	args, err := extension.DecodeArgs[struct {
		BaseURL string `json:"base_url"`
		Token   string `json:"token"`
	}](in)
	if err != nil {
		return nil, err
	}
	member := rt.Caller().UserID
	if member == "" {
		// A job tick and a bus delivery both answer the zero Caller. Neither
		// can deposit a credential, because there is nobody whose credential
		// it would be.
		return nil, fmt.Errorf("%w: connecting an account is something a person does, and this invocation has nobody behind it", extension.ErrForbidden)
	}
	base, err := connectableBaseURL(args.BaseURL)
	if err != nil {
		return nil, err
	}
	token, err := depositableToken(args.Token)
	if err != nil {
		return nil, err
	}
	// The credential first, and the row second, and the order is the whole
	// handling of a half-connection: a row with no token on deposit polls
	// nothing and answers ErrForbidden at the ingress port on every tick,
	// while a token with no row is invisible to the poll and costs nothing.
	// One of those states is a connection that looks live and is not.
	if err := rt.Secrets().PutUser(ctx, extension.UserID(member), tokenKey, []byte(token)); err != nil {
		return nil, err
	}
	var stored connection
	err = rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		before, err := connectionOf(ctx, tx, member)
		if err != nil {
			return err
		}
		stored, err = scanConnection(tx.QueryRow(ctx,
			`INSERT INTO `+connectionTable+` (workspace_id, user_id, base_url)
			 VALUES (`+callerWorkspace+`, $1::uuid, $2)
			 ON CONFLICT (workspace_id, user_id) DO UPDATE
			    SET base_url = EXCLUDED.base_url,
			        status = '`+statusConnected+`',
			        last_error_class = NULL,
			        -- The cursor survives a token rotation and is reset by a
			        -- DEPLOYMENT change: ids from the old one mean nothing
			        -- against the new, and keeping them stops the feed dead.
			        high_water_mark = CASE WHEN `+connectionTable+`.base_url = EXCLUDED.base_url
			                               THEN `+connectionTable+`.high_water_mark ELSE 0 END,
			        backfill_before = CASE WHEN `+connectionTable+`.base_url = EXCLUDED.base_url
			                               THEN `+connectionTable+`.backfill_before ELSE NULL END,
			        pending_high_water_mark = CASE WHEN `+connectionTable+`.base_url = EXCLUDED.base_url
			                               THEN `+connectionTable+`.pending_high_water_mark ELSE NULL END,
			        provider_workspace_id = CASE WHEN `+connectionTable+`.base_url = EXCLUDED.base_url
			                               THEN `+connectionTable+`.provider_workspace_id ELSE NULL END,
			        version = `+connectionTable+`.version + 1,
			        updated_at = now()
			 RETURNING `+connectionColumns, member, base).Scan)
		if err != nil {
			return err
		}
		return recordConnection(ctx, tx, auditActionFor(before), eventConnected, before, &stored)
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(stored)
}

// auditActionFor reports whether this write created the row or updated one.
// Recording an update as a create would put a ledger row with no before-image
// over a state that existed, which the ledger's own grammar refuses and which
// would read as "this connection appeared now" for a member who reconnected.
func auditActionFor(before *connection) extension.AuditAction {
	if before == nil {
		return extension.AuditCreate
	}
	return extension.AuditUpdate
}

// status answers what the poll is doing for the CALLER's own connection.
//
// Their own, and not a member named in the arguments: the row carries a cursor,
// a last-polled time and an error class, which together say when somebody was
// last messaged and whether their account still works. That is not a fact this
// unit hands to a colleague because they hold the same RBAC object.
func status(ctx context.Context, rt extension.Runtime, in json.RawMessage) (json.RawMessage, error) {
	if _, err := extension.DecodeArgs[struct{}](in); err != nil {
		return nil, err
	}
	member := rt.Caller().UserID
	if member == "" {
		return nil, fmt.Errorf("%w: this invocation has nobody behind it, so there is no connection to report", extension.ErrForbidden)
	}
	var (
		found     *connection
		connected bool
	)
	err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		var err error
		found, err = connectionOf(ctx, tx, member)
		connected = found != nil
		return err
	})
	if err != nil {
		return nil, err
	}
	// A member with no connection is `connected: false`, not an error: not
	// having connected yet is the ordinary state of this screen, and the
	// caller is asking exactly that question.
	return json.Marshal(struct {
		Connected  bool        `json:"connected"`
		Connection *connection `json:"connection,omitempty"`
	}{Connected: connected, Connection: found})
}

// disconnect removes the caller's own connection and their deposited token.
//
// BOTH, and the row goes rather than taking a third status: a "disconnected"
// row would be a connection the poll skips, the screen shows as absent, and the
// ingress port would still accept records for — because the consent it reads is
// the deposited credential, not this table. Deleting the credential is what
// actually ends the authority, and deleting the row is what stops the poll
// enumerating it.
func disconnect(ctx context.Context, rt extension.Runtime, in json.RawMessage) (json.RawMessage, error) {
	if _, err := extension.DecodeArgs[struct{}](in); err != nil {
		return nil, err
	}
	member := rt.Caller().UserID
	if member == "" {
		return nil, fmt.Errorf("%w: this invocation has nobody behind it, so there is no connection to remove", extension.ErrForbidden)
	}
	// The credential first, for the reason connect deposits it first: if the
	// row survives a failure here the poll keeps running against a token the
	// member asked to withdraw, which is the one ordering that leaves
	// authority behind.
	if err := rt.Secrets().DeleteUser(ctx, extension.UserID(member), tokenKey); err != nil &&
		!errors.Is(err, extension.ErrSecretNotFound) {
		return nil, err
	}
	var removed bool
	err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		gone, err := scanConnection(tx.QueryRow(ctx,
			`DELETE FROM `+connectionTable+` WHERE user_id = $1::uuid RETURNING `+connectionColumns, member).Scan)
		if err != nil {
			if isNoRows(err) {
				return nil
			}
			return err
		}
		removed = true
		return recordConnection(ctx, tx, extension.AuditErase, eventDisconnected, &gone, nil)
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Disconnected bool `json:"disconnected"`
	}{Disconnected: removed})
}

// connectionOf reads one member's connection, or nothing.
func connectionOf(ctx context.Context, tx extension.Tx, member string) (*connection, error) {
	found, err := scanConnection(tx.QueryRow(ctx,
		`SELECT `+connectionColumns+` FROM `+connectionTable+` WHERE user_id = $1::uuid`, member).Scan)
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	return &found, nil
}

// isNoRows reports whether a single-row read matched nothing.
//
// It is the PUBLISHED sentinel, and the distinction cost a UAT to find: an
// earlier version matched the driver's own text ("no rows in result set"),
// which the seam never hands a unit — the core translates it into
// extension.ErrNoRows precisely so a unit does not match on a driver's
// wording. Every read here then reported "no such connection" as a 500, and
// the unit's own suite agreed with it, because the fake returned the text the
// handler was looking for rather than what the core returns.
func isNoRows(err error) bool {
	return errors.Is(err, extension.ErrNoRows)
}

// connectableBaseURL validates what a member typed, through the same parser the
// client uses, so a URL this unit would refuse to dial cannot be stored as a
// working connection. The refusal is worded for a person reading a screen.
func connectableBaseURL(raw string) (string, error) {
	parsed, err := parseBaseURL(raw)
	if err != nil {
		return "", err
	}
	return parsed.String(), nil
}

// depositableToken bounds what is sealed. A token is opaque to this unit, so
// the only honest checks are that it is there and that it is not a paste of
// something else entirely.
func depositableToken(raw string) (string, error) {
	token := strings.TrimSpace(raw)
	switch {
	case token == "":
		return "", fmt.Errorf("%w: connecting needs the personal access token from your Relay profile", extension.ErrInvalid)
	case len(token) > maxTokenBytes:
		return "", fmt.Errorf("%w: that token is %d bytes, over the %d-byte cap — check that a whole page was not pasted", extension.ErrInvalid, len(token), maxTokenBytes)
	}
	return token, nil
}
