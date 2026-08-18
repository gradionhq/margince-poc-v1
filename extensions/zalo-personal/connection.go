// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// The connection row: what this installation records about a member having
// connected their personal Zalo, and the two operations that read and withdraw
// it. The QR login that CREATES it lives in connect.go.
//
// THE MEMBER IS rt.Caller().UserID AND NOTHING ELSE, in every operation of
// this unit. None of them
// declares a member argument and the strict decoder would refuse one, but the
// rule is written here because of what it protects: this unit's credential
// reads a human's whole personal chat history, so an operation that let one
// member deposit — or withdraw, or inspect — a credential FOR another would be
// this unit's own front door onto a colleague's private life. It is the same
// rule as captured_by being stamped from the authenticated principal, with the
// stakes of a personal messenger rather than a mailbox.
//
// None of them names a workspace either: the Runtime pins the transaction to
// the invocation's tenant before the first statement runs.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// The three statuses this unit writes. `needs_reconnect` is the scheduled
// capture's: a session that stopped being accepted, which only that human
// re-scanning a QR with their phone restores — which is why it is a state a
// screen shows rather than a fault a retry clears.
const (
	statusConnected      = "connected"
	statusNeedsReconnect = "needs_reconnect"
	statusDisconnected   = "disconnected"
)

// connectionColumns is the projection every read and every write returns, in
// one place so a column added to the table is one edit rather than five.
const connectionColumns = `id::text, user_id::text, status, zalo_uid,
	coalesce(display_name, ''), capture_enabled, last_polled_at,
	coalesce(last_error_class, ''), connected_at, idle_streak, version`

// duePromptly is the ONE spelling of "poll this member on the next tick", and it is
// one spelling because three different acts mean it: a fresh connect, a save of the
// conversation list, and any drain that produced a record.
//
// The middle one is the reason it has to be shared rather than repeated. A member
// who has been quiet for a week is sitting inside a capped backoff; the moment they
// arm a conversation they must not wait that backoff out before anything appears —
// they would read that as the feature not working, and they would be right. Two
// spellings of this clause is exactly the drift that would reintroduce that.
const duePromptly = `idle_streak = 0, poll_after = NULL`

// connection is one member's connection, as this unit reads and renders it.
//
// IT CARRIES NO SESSION, and cannot: the column does not exist. What the screen
// learns about a credential is that one is on deposit, which is a separate
// question this unit answers with a boolean.
type connection struct {
	ID     string `json:"id"`
	UserID string `json:"user_id"`
	Status string `json:"status"`
	// ZaloUID is which account was scanned. It is what a captured message's
	// self-echo is recognised by and what a reply is addressed from, so it is
	// taken from the CREDENTIAL — the session says who it belongs to — and
	// never from anything a client sent.
	ZaloUID     string `json:"zalo_uid"`
	DisplayName string `json:"display_name,omitempty"`
	// CaptureEnabled is the mechanism behind "capture nothing until the member
	// chooses": no operation in this change turns it on, and the scheduled
	// capture refuses to open a socket without it.
	CaptureEnabled bool `json:"capture_enabled"`
	// THE CURSOR IS NOT HERE, and its absence is a decision rather than an
	// omission: it lives on each VERDICT row (allowlist.go), one per counterparty.
	// A single cursor per member is a maximum over every conversation, so a message
	// landing from an allowed counterparty buries every lower-numbered message
	// dropped from a conversation the member had not yet chosen — and that
	// conversation then starts empty the moment they choose it.
	LastPolledAt string `json:"last_polled_at,omitempty"`
	// LastErrorClass is a class this unit chose, never a provider's own
	// message: it is rendered, and a remote party's prose is not this
	// installation's to display.
	LastErrorClass string `json:"last_error_class,omitempty"`
	ConnectedAt    string `json:"connected_at,omitempty"`
	// IdleStreak is how many consecutive drains produced nothing new, and the only
	// input to how long this member waits for the next one.
	//
	// OFF THE WIRE AND OFF THE LEDGER IMAGE, deliberately: it is a scheduling
	// counter rather than a fact about this member's account, and putting it in the
	// audit image would churn one field-history entry per member per cadence
	// forever to record that a schedule ran.
	IdleStreak int `json:"-"`
	Version    int `json:"version"`
}

// scanConnection reads connectionColumns off one row.
//
// The two timestamps are scanned as TIMES and rendered afterwards, not scanned
// as text: the columns are timestamptz and the driver refuses to put one into a
// string — a mistake no unit test catches on its own, because a fake hands back
// whatever the fixture scripted while the driver decides by the column's real
// type. RFC 3339 is what the contract declares.
func scanConnection(scan func(...any) error) (connection, error) {
	var (
		c                         connection
		lastPolledAt, connectedAt *time.Time
	)
	err := scan(&c.ID, &c.UserID, &c.Status, &c.ZaloUID, &c.DisplayName, &c.CaptureEnabled,
		&lastPolledAt, &c.LastErrorClass, &connectedAt, &c.IdleStreak, &c.Version)
	if err != nil {
		return connection{}, err
	}
	c.LastPolledAt, c.ConnectedAt = renderTime(lastPolledAt), renderTime(connectedAt)
	return c, nil
}

// renderTime formats a nullable timestamp for the wire, empty for none.
func renderTime(at *time.Time) string {
	if at == nil {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
}

// status answers what this installation holds for the CALLER's own account.
//
// It returns the row and a BOOLEAN about the credential, and never a byte of
// the credential itself — not masked, not truncated, not a fingerprint. A
// masked secret is still a secret with information removed, and there is no
// question a screen asks about this one that "is a session on deposit" does not
// answer.
func status(ctx context.Context, rt extension.Runtime, in json.RawMessage) (json.RawMessage, error) {
	if _, err := extension.DecodeArgs[struct{}](in); err != nil {
		return nil, err
	}
	member, err := connectingMember(rt)
	if err != nil {
		return nil, err
	}
	var (
		found   *connection
		allowed int
	)
	if err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		if found, err = connectionOf(ctx, tx, string(member)); err != nil || found == nil {
			return err
		}
		// Counted only where there IS a connection: how many conversations are
		// armed is a question about an account, and asking it for a member who
		// has connected none would spend a statement to answer nothing.
		allowed, err = countAllowed(ctx, tx, string(member))
		return err
	}); err != nil {
		return nil, err
	}
	deposited, err := sessionDeposited(ctx, rt, member)
	if err != nil {
		return nil, err
	}
	// No connection at all is `connected: false`, not an error: not having
	// connected yet is the ordinary state of this screen.
	return json.Marshal(struct {
		Connected        bool        `json:"connected"`
		SessionDeposited bool        `json:"session_deposited"`
		AllowedCount     int         `json:"allowed_count"`
		Connection       *connection `json:"connection,omitempty"`
	}{
		Connected:        found != nil && found.Status == statusConnected,
		SessionDeposited: deposited,
		AllowedCount:     allowed,
		Connection:       found,
	})
}

// disconnect withdraws the calling member's account.
//
// BOTH SECRETS FIRST, THEN THE ROW, and the order is the point rather than a
// preference: the credential is what actually reads their messages, so deleting
// the row first would leave a window — however short — in which this
// installation still holds a live session behind a screen that says
// "disconnected". `pending-login` goes too, because a half-finished handshake
// is a credential in its own right.
//
// Captured activities STAY. They are the CRM's record of business conversations
// that happened, and their retention is the CRM's rule rather than this
// connector's; erasing a person's records is the core's erasure path, reached
// the same way it is for every other connector.
func disconnect(ctx context.Context, rt extension.Runtime, in json.RawMessage) (json.RawMessage, error) {
	if _, err := extension.DecodeArgs[struct{}](in); err != nil {
		return nil, err
	}
	member, err := connectingMember(rt)
	if err != nil {
		return nil, err
	}
	for _, key := range []string{sessionKey, pendingKey} {
		if err := forget(ctx, rt, member, key); err != nil {
			return nil, err
		}
	}
	var moved bool
	err = rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		before, err := connectionOf(ctx, tx, string(member))
		if err != nil || before == nil {
			return err
		}
		// The row is KEPT and marked, rather than deleted: it is what the
		// member's screen reads to explain why capture stopped, and what the
		// next reconnect updates. capture_enabled goes back to false with it —
		// re-connecting must not silently re-arm a list the member has not
		// looked at since.
		after, err := scanConnection(tx.QueryRow(ctx,
			`UPDATE `+connectionTable+`
			    SET status = '`+statusDisconnected+`', capture_enabled = false,
			        last_error_class = NULL, version = version + 1, updated_at = now()
			  WHERE user_id = $1::uuid
			 RETURNING `+connectionColumns, string(member)).Scan)
		if err != nil {
			return err
		}
		moved = true
		return recordConnection(ctx, tx, extension.AuditUpdate, eventDisconnected, before, &after)
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Disconnected bool `json:"disconnected"`
	}{Disconnected: moved})
}

// upsertConnection records a confirmed login on the member's own row.
func upsertConnection(ctx context.Context, rt extension.Runtime, member extension.UserID,
	uid, displayName string,
) error {
	if uid == "" {
		return errors.New("zalo-personal: the resumed session did not say which account it belongs to, so there is nothing to bind this connection to")
	}
	return rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		before, err := connectionOf(ctx, tx, string(member))
		if err != nil {
			return err
		}
		after, err := scanConnection(tx.QueryRow(ctx,
			`INSERT INTO `+connectionTable+`
			        (workspace_id, user_id, status, zalo_uid, display_name, connected_at)
			 VALUES (`+callerWorkspace+`, $1::uuid, '`+statusConnected+`', $2, NULLIF($3, ''), now())
			 ON CONFLICT (workspace_id, user_id) DO UPDATE
			    SET status = '`+statusConnected+`',
			        zalo_uid = EXCLUDED.zalo_uid,
			        display_name = EXCLUDED.display_name,
			        connected_at = now(),
			        last_error_class = NULL,
			        -- A member re-scanning the SAME account keeps the list they
			        -- chose; one connecting a DIFFERENT account starts from
			        -- capture-nothing, because the entries in that list name
			        -- counterparties of the account they just replaced.
			        capture_enabled = CASE WHEN `+connectionTable+`.zalo_uid = EXCLUDED.zalo_uid
			                               THEN `+connectionTable+`.capture_enabled ELSE false END,
			        -- A RE-SCAN IS DUE NOW, whichever account it was. A member who
			        -- fixed a dead session is standing in front of the screen
			        -- waiting for it to work, and making them wait out a backoff
			        -- earned while the session was broken reads as the re-scan
			        -- having failed.
			        `+duePromptly+`,
			        version = `+connectionTable+`.version + 1,
			        updated_at = now()
			 RETURNING `+connectionColumns, string(member), uid, displayName).Scan)
		if err != nil {
			return err
		}
		// A DIFFERENT ACCOUNT INVALIDATES EVERYTHING SCOPED TO THE OLD ONE, and
		// this is the one place that says what that is: the statement above has
		// already dropped the chosen-conversations flag and the cursor, and the
		// send markers go with them. An id minted by the account just replaced
		// would otherwise suppress a real message in the new one.
		if before != nil && before.ZaloUID != after.ZaloUID {
			if err := forgetSentMarkersOf(ctx, tx, string(member)); err != nil {
				return err
			}
			// The verdict cursors go with them, and for a harder reason than the
			// markers do: a cursor is a high-water mark in the OTHER account's
			// message-id space, so a different account whose ids happen to sit
			// below it would have that counterparty's first messages silently
			// filtered as already-landed. The verdicts themselves are kept —
			// capture is disarmed above, so the member re-reads their own list
			// before anything is captured again.
			if err := forgetVerdictCursorsOf(ctx, tx, string(member)); err != nil {
				return err
			}
		}
		return recordConnection(ctx, tx, auditActionFor(before), eventConnected, before, &after)
	})
}

// auditActionFor reports whether this write created the row or updated one.
// Recording an update as a create would put a ledger row with no before-image
// over a state that existed, and read as "this connection appeared now" for a
// member who reconnected.
func auditActionFor(before *connection) extension.AuditAction {
	if before == nil {
		return extension.AuditCreate
	}
	return extension.AuditUpdate
}

// connectingMember is the ONE place the caller binding is read, so there is one
// place to look when asking whose credential an operation touches.
//
// A job tick and a bus delivery both answer the zero Caller. Neither can hold a
// personal account's credential, because there is nobody whose credential it
// would be.
func connectingMember(rt extension.Runtime) (extension.UserID, error) {
	member := rt.Caller().UserID
	if member == "" {
		return "", fmt.Errorf("%w: connecting a personal Zalo account is something a person does, and this invocation has nobody behind it", extension.ErrForbidden)
	}
	return extension.UserID(member), nil
}

// sessionDeposited reports whether a sealed session exists, WITHOUT returning
// any part of it. The material it reads back is dropped here and never travels
// further; the boolean is the whole of what a screen is told.
func sessionDeposited(ctx context.Context, rt extension.Runtime, member extension.UserID) (bool, error) {
	if _, err := rt.Secrets().GetUser(ctx, member, sessionKey); err != nil {
		if errors.Is(err, extension.ErrSecretNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// forget removes one of this member's sealed secrets. A key that holds nothing
// is not an error on a withdrawal path: "it was already gone" is the outcome
// asked for.
func forget(ctx context.Context, rt extension.Runtime, member extension.UserID, key string) error {
	if err := rt.Secrets().DeleteUser(ctx, member, key); err != nil &&
		!errors.Is(err, extension.ErrSecretNotFound) {
		return err
	}
	return nil
}

// connectionOf reads one member's connection, or nothing.
func connectionOf(ctx context.Context, tx extension.Tx, member string) (*connection, error) {
	found, err := scanConnection(tx.QueryRow(ctx,
		`SELECT `+connectionColumns+` FROM `+connectionTable+` WHERE user_id = $1::uuid`, member).Scan)
	if err != nil {
		if errors.Is(err, extension.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &found, nil
}
