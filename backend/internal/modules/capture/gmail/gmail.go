// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package gmail is a read-only Gmail capture connector: it authorizes a
// user's mailbox over OAuth2, pulls mail incrementally through the Gmail
// history API, and normalizes each message into an email activity. It
// implements connector.Connector, so every captured row lands through the
// ONE capture Sink (audit + outbox in one transaction) — this package owns
// the provider I/O (client.go) and composes the pure RFC822 mapping
// (capture/mailmap), nothing about the write.
//
// Unlike the one-shot IMAP puller, a Gmail connection is standing: the
// refresh token is persisted (via the registry → keyvault) and the sync
// cursor is Gmail's historyId, so each Sync resumes where the last left off
// and is idempotent on (gmail, RFC822 Message-ID).
package gmail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/modules/capture/googleconn"
	"github.com/gradionhq/margince/backend/internal/modules/capture/mailmap"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

const (
	connectorName = "gmail"

	// backfillWindow bounds the first sync (before any cursor exists) so a
	// large mailbox does not stream unbounded on connect; steady-state sync
	// is delta-only via the history API.
	backfillWindow = 50
)

// Connector authorizes and syncs Gmail mailboxes. It holds NO per-mailbox or
// per-run state: one instance is registered and shared, and every Sync derives
// the owner + counters as locals so concurrent syncs of different connections
// never race. (owner is a field only for the pure Normalize surface, which is
// test-only — Sync never touches it.)
type Connector struct {
	oauth OAuth
	api   API
	owner string // used ONLY by Normalize (the test-guarded pure mapping); never set by Sync
}

// New returns a Gmail connector over the given OAuth + API surfaces.
func New(oauth OAuth, api API) *Connector { return &Connector{oauth: oauth, api: api} }

var (
	_ connector.Connector = (*Connector)(nil)
	_ connector.Watcher   = (*Connector)(nil)
)

// cursorState is the persisted incremental watermark: Gmail's historyId.
type cursorState struct {
	HistoryID string `json:"history_id"`
}

// AuthRequestFrom packages an OAuth callback's code into the opaque connector
// AuthRequest the callback handler passes to Authenticate — the shared Google
// handshake (googleconn); the mailbox owner is resolved during Authenticate.
func AuthRequestFrom(code, redirectURI string) (connector.AuthRequest, error) {
	return googleconn.AuthRequestFrom(code, redirectURI)
}

// Descriptor is the connector's static metadata: name "gmail", read-only
// (TierGreen), producing activities — the shared Google connector shape.
func (c *Connector) Descriptor() connector.Descriptor {
	return googleconn.Descriptor(connectorName)
}

// Authenticate exchanges the authorization code for a refresh token, resolves
// the mailbox owner, and returns the opaque Auth the registry seals into the
// vault. The shared Google handshake does the work; the only Gmail-specific step
// is resolving the owner from the mailbox profile.
func (c *Connector) Authenticate(ctx context.Context, req connector.AuthRequest) (connector.Auth, error) {
	return googleconn.Authenticate(ctx, c.oauth, req, c.Descriptor().Scopes,
		func(ctx context.Context, access string) (string, error) {
			owner, _, err := c.api.Profile(ctx, access)
			return owner, err
		})
}

// Sync mints a fresh access token, then pulls incrementally: with no cursor
// it anchors at the mailbox's current historyId and backfills a bounded
// window; with a cursor it walks the history API for messages added since.
// A cursor Gmail no longer holds (ErrHistoryGone) degrades to a bounded
// re-list rather than a full re-scan. The advanced historyId is returned as
// the new cursor; the registry persists it only on a fully-successful Sync.
func (c *Connector) Sync(ctx context.Context, auth connector.Auth, cursor connector.Cursor, sink connector.Sink) (connector.Cursor, error) {
	owner, access, err := googleconn.Session(ctx, c.oauth, auth)
	if err != nil {
		return nil, err
	}

	start, err := parseCursor(cursor)
	if err != nil {
		// A stored cursor we can't read is a bug/corruption, NOT a fresh mailbox:
		// stop and let the next cycle retry rather than silently backfilling and
		// overwriting the watermark (which would drop everything in between).
		return nil, err
	}
	ids, nextHistory, err := c.selectMessages(ctx, access, start)
	if err != nil {
		return nil, err
	}

	for _, id := range ids {
		raw, err := c.api.GetRaw(ctx, access, id)
		if err != nil {
			// A fetch fault is transient — stop the pull without advancing the
			// cursor so the next cycle retries from the same watermark.
			return nil, err
		}
		if err := captureOne(ctx, raw, sink, owner); err != nil {
			return nil, err
		}
	}

	if nextHistory == "" {
		nextHistory = start // nothing new; keep the prior watermark
	}
	return marshalCursor(nextHistory), nil
}

// selectMessages resolves which message ids to pull and the historyId to
// advance to, choosing the initial-backfill or the incremental path and
// folding the stale-cursor fallback into one place.
func (c *Connector) selectMessages(ctx context.Context, access, start string) ([]string, string, error) {
	if start == "" {
		return c.backfill(ctx, access)
	}
	added, next, err := c.api.History(ctx, access, start)
	if errors.Is(err, ErrHistoryGone) {
		return c.backfill(ctx, access)
	}
	if err != nil {
		return nil, "", err
	}
	return added, next, nil
}

// backfill anchors the cursor at the mailbox's current historyId and returns
// a bounded window of recent messages — used on first connect and when the
// stored cursor has aged out.
func (c *Connector) backfill(ctx context.Context, access string) ([]string, string, error) {
	_, historyID, err := c.api.Profile(ctx, access)
	if err != nil {
		return nil, "", err
	}
	ids, err := c.api.ListRecent(ctx, access, backfillWindow)
	if err != nil {
		return nil, "", err
	}
	return ids, historyID, nil
}

// captureOne parses, drops, or upserts one raw message — the same discipline
// the IMAP connector uses. A parse failure or a deliberate skip is a no-op;
// only a real Sink write fault returns a non-nil error (which stops the pull).
// It is a package function (no receiver) so a pull holds no shared state.
func captureOne(ctx context.Context, raw []byte, sink connector.Sink, owner string) error {
	msg, err := mailmap.Parse(raw, owner)
	if err != nil {
		return nil //nolint:nilerr // a single unparseable message is a skip, not a fatal pull error (mirrors the IMAP connector)
	}
	if _, drop := msg.SkipReason(); drop {
		return nil
	}
	if _, err := sink.Upsert(ctx, msg.ToRecord(connectorName, raw)); err != nil {
		if errors.Is(err, connector.ErrSkip) {
			return nil
		}
		return err
	}
	return nil
}

// Normalize maps ONE raw RFC822 message (a Gmail format=RAW payload, already
// decoded) to its activity record. Pure — no I/O — so the mapping is the
// test-guarded surface; it returns an ErrSkip-wrapped error for mail this
// connector intentionally drops.
func (c *Connector) Normalize(_ context.Context, raw connector.RawRecord) ([]connector.NormalizedRecord, error) {
	msg, err := mailmap.Parse(raw, c.owner)
	if err != nil {
		return nil, err
	}
	if reason, drop := msg.SkipReason(); drop {
		return nil, fmt.Errorf("gmail: dropping %s (%s): %w", msg.ID(), reason, connector.ErrSkip)
	}
	return []connector.NormalizedRecord{msg.ToRecord(connectorName, raw)}, nil
}

// Watch registers a Gmail users.watch against the Pub/Sub topic so Gmail
// publishes change notifications for the mailbox, returning the mailbox's
// historyId at watch time and when the watch expires (Gmail caps it at 7 days).
// Re-calling it renews the watch. Like Sync, it mints a fresh access token from
// the stored refresh token; it never touches the CRM or the connection row —
// the registry persists the expiration into capture_connection.watch_expires_at.
func (c *Connector) Watch(ctx context.Context, auth connector.Auth, topic string) (connector.WatchResult, error) {
	var st googleconn.AuthState
	if err := json.Unmarshal(auth, &st); err != nil {
		return connector.WatchResult{}, fmt.Errorf("gmail: malformed auth state: %w", err)
	}
	access, err := c.oauth.AccessToken(ctx, st.RefreshToken)
	if err != nil {
		return connector.WatchResult{}, err
	}
	historyID, expiration, err := c.api.Watch(ctx, access, topic)
	if err != nil {
		return connector.WatchResult{}, err
	}
	return connector.WatchResult{HistoryID: historyID, ExpiresAt: expiration}, nil
}

// HealthCheck confirms the stored credential still mints a token and the
// mailbox answers. An outage degrades capture but never blocks core CRM.
func (c *Connector) HealthCheck(ctx context.Context, auth connector.Auth) error {
	_, access, err := googleconn.Session(ctx, c.oauth, auth)
	if err != nil {
		return err
	}
	if _, _, err := c.api.Profile(ctx, access); err != nil {
		return err
	}
	return nil
}

// parseCursor reads the stored watermark. An empty cursor means a genuinely
// fresh mailbox (→ initial backfill); a NON-empty but unreadable cursor is an
// error, not a silent re-anchor — the caller stops rather than backfill and
// overwrite the watermark (which would drop everything in between).
func parseCursor(cur connector.Cursor) (string, error) {
	if len(cur) == 0 {
		return "", nil
	}
	var cs cursorState
	if err := json.Unmarshal(cur, &cs); err != nil {
		return "", fmt.Errorf("gmail: unreadable sync cursor: %w", err)
	}
	return cs.HistoryID, nil
}

func marshalCursor(historyID string) connector.Cursor {
	// cursorState has only string fields, so Marshal cannot fail here.
	b, _ := json.Marshal(cursorState{HistoryID: historyID}) //nolint:errchkjson // string-only struct never errors
	return b
}
