// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package gcal is a read-only Google Calendar capture connector: it authorizes
// a user's calendar over OAuth2, pulls events incrementally through the
// Calendar v3 sync-token API, and normalizes each external meeting into a
// meeting activity. It implements connector.Connector, so every captured row
// lands through the ONE capture Sink (audit + outbox in one transaction) — this
// package owns the provider I/O (client.go) and the pure event mapping
// (event.go), nothing about the write.
//
// Like the Gmail connector (and unlike the one-shot IMAP puller) a calendar
// connection is standing: the refresh token is persisted (via the registry →
// keyvault) and the sync cursor is the Calendar syncToken, so each Sync resumes
// where the last left off and is idempotent on (gcal, event id). Polling-first
// by design: real-time push (Calendar watch channels) is a later follow-up —
// this connector is driven only by the background DueConnections → SyncOnce
// poll.
package gcal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/modules/capture/googleconn"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

const connectorName = "gcal"

// Connector authorizes and syncs Google Calendars. It holds NO per-connection
// or per-run state: one instance is registered and shared, and every Sync
// derives the owner as a local so concurrent syncs of different connections
// never race. (owner is a field only for the pure Normalize surface, which is
// test-only — Sync never touches it.)
type Connector struct {
	oauth OAuth
	api   API
	owner string // used ONLY by Normalize (the test-guarded pure mapping); never set by Sync
}

// New returns a Calendar connector over the given OAuth + API surfaces.
func New(oauth OAuth, api API) *Connector { return &Connector{oauth: oauth, api: api} }

var _ connector.Connector = (*Connector)(nil)

// cursorState is the persisted incremental watermark: Calendar's syncToken.
type cursorState struct {
	SyncToken string `json:"sync_token"`
}

// AuthRequestFrom packages an OAuth callback's code into the opaque connector
// AuthRequest the callback handler passes to Authenticate. It is the shared
// Google handshake — the calendar owner is resolved from the primary calendar
// during Authenticate (googleconn.OwnerResolver).
func AuthRequestFrom(code, redirectURI string) (connector.AuthRequest, error) {
	return googleconn.AuthRequestFrom(code, redirectURI)
}

// Descriptor is the connector's static metadata: name "gcal", read-only
// (TierGreen), producing activities — the shared Google connector shape.
func (c *Connector) Descriptor() connector.Descriptor {
	return googleconn.Descriptor(connectorName)
}

// Authenticate exchanges the authorization code for a refresh token, resolves
// the calendar owner (the primary calendar's address), and returns the opaque
// Auth the registry seals into the vault. The shared Google handshake does the
// work; the only calendar-specific step is resolving the owner.
func (c *Connector) Authenticate(ctx context.Context, req connector.AuthRequest) (connector.Auth, error) {
	return googleconn.Authenticate(ctx, c.oauth, req, c.Descriptor().Scopes, c.api.PrimaryOwner)
}

// Sync mints a fresh access token, then pulls incrementally: with no cursor it
// backfills a bounded window and anchors the returned syncToken; with a cursor
// it lists the events changed since. A cursor Google no longer honors
// (ErrSyncTokenGone) degrades to a bounded re-list rather than a full re-scan.
// The advanced syncToken is returned as the new cursor; the registry persists
// it only on a fully-successful Sync.
func (c *Connector) Sync(ctx context.Context, auth connector.Auth, cursor connector.Cursor, sink connector.Sink) (connector.Cursor, error) {
	owner, access, err := googleconn.Session(ctx, c.oauth, auth)
	if err != nil {
		return nil, err
	}

	start, err := parseCursor(cursor)
	if err != nil {
		// A stored cursor we can't read is a bug/corruption, NOT a fresh
		// calendar: stop and let the next cycle retry rather than silently
		// backfilling and overwriting the watermark.
		return nil, err
	}
	events, nextToken, err := c.selectEvents(ctx, access, start)
	if err != nil {
		return nil, err
	}

	for _, raw := range events {
		if err := captureOne(ctx, raw, sink, owner); err != nil {
			return nil, err
		}
	}

	// selectEvents (through listPages) guarantees a non-empty syncToken on a
	// successful pull, so the advanced watermark is always real here.
	return marshalCursor(nextToken), nil
}

// selectEvents resolves which events to pull and the syncToken to advance to,
// choosing the initial-backfill or the incremental path and folding the
// stale-token fallback into one place.
func (c *Connector) selectEvents(ctx context.Context, access, start string) ([][]byte, string, error) {
	if start == "" {
		return c.api.ListInitial(ctx, access)
	}
	events, next, err := c.api.ListIncremental(ctx, access, start)
	if errors.Is(err, ErrSyncTokenGone) {
		return c.api.ListInitial(ctx, access)
	}
	if err != nil {
		return nil, "", err
	}
	return events, next, nil
}

// captureOne parses, drops, or upserts one raw event — the same discipline the
// mail connectors use. A parse failure or a deliberate skip (cancelled,
// all-internal) is a no-op; only a real Sink write fault returns a non-nil
// error (which stops the pull). It is a package function (no receiver) so a
// pull holds no shared state.
func captureOne(ctx context.Context, raw []byte, sink connector.Sink, owner string) error {
	m, err := parseEvent(raw, owner)
	if err != nil {
		return nil //nolint:nilerr // a single unparseable event is a skip, not a fatal pull error (mirrors the mail connectors)
	}
	if _, drop := m.SkipReason(); drop {
		return nil
	}
	if _, err := sink.Upsert(ctx, m.ToRecord(connectorName, raw)); err != nil {
		if errors.Is(err, connector.ErrSkip) {
			return nil
		}
		return err
	}
	return nil
}

// Normalize maps ONE raw Calendar event resource to its meeting activity. Pure
// — no I/O — so the mapping is the test-guarded surface; it returns an
// ErrSkip-wrapped error for an event this connector intentionally drops
// (cancelled, or all attendees internal).
func (c *Connector) Normalize(_ context.Context, raw connector.RawRecord) ([]connector.NormalizedRecord, error) {
	m, err := parseEvent(raw, c.owner)
	if err != nil {
		return nil, err
	}
	if reason, drop := m.SkipReason(); drop {
		return nil, fmt.Errorf("gcal: dropping %s (%s): %w", m.ID(), reason, connector.ErrSkip)
	}
	return []connector.NormalizedRecord{m.ToRecord(connectorName, raw)}, nil
}

// HealthCheck confirms the stored credential still mints a token and the
// calendar answers. An outage degrades capture but never blocks core CRM.
func (c *Connector) HealthCheck(ctx context.Context, auth connector.Auth) error {
	_, access, err := googleconn.Session(ctx, c.oauth, auth)
	if err != nil {
		return err
	}
	if _, err := c.api.PrimaryOwner(ctx, access); err != nil {
		return err
	}
	return nil
}

// parseCursor reads the stored watermark. An empty cursor means a genuinely
// fresh calendar (→ initial backfill); a NON-empty but unreadable cursor is an
// error, not a silent re-anchor — the caller stops rather than backfill and
// overwrite the watermark.
func parseCursor(cur connector.Cursor) (string, error) {
	if len(cur) == 0 {
		return "", nil
	}
	var cs cursorState
	if err := json.Unmarshal(cur, &cs); err != nil {
		return "", fmt.Errorf("gcal: unreadable sync cursor: %w", err)
	}
	if cs.SyncToken == "" {
		// A stored-but-empty token is corruption, NOT a fresh calendar: stop
		// rather than silently re-backfill and overwrite the watermark.
		return "", fmt.Errorf("gcal: sync cursor carries no token")
	}
	return cs.SyncToken, nil
}

func marshalCursor(syncToken string) connector.Cursor {
	// cursorState has only a string field, so Marshal cannot fail here.
	b, _ := json.Marshal(cursorState{SyncToken: syncToken}) //nolint:errchkjson // string-only struct never errors
	return b
}
