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
	"strings"

	"github.com/gradionhq/margince/backend/internal/modules/capture/mailmap"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

const (
	connectorName = "gmail"

	// backfillWindow bounds the first sync (before any cursor exists) so a
	// large mailbox does not stream unbounded on connect; steady-state sync
	// is delta-only via the history API.
	backfillWindow = 50
)

// Connector authorizes and syncs one Gmail mailbox. owner is set from the
// persisted auth at the top of Sync/Normalize, so the pure mapping can
// classify direction without a provider handle.
type Connector struct {
	oauth OAuth
	api   API
	owner string
	stats Stats
}

// Stats is the outcome of one Sync, surfaced to the ops/summary surface.
type Stats struct {
	Captured int
	Skipped  int
	Contacts int
}

// New returns a Gmail connector over the given OAuth + API surfaces.
func New(oauth OAuth, api API) *Connector { return &Connector{oauth: oauth, api: api} }

var _ connector.Connector = (*Connector)(nil)

// authState is the persisted credential bundle (the opaque connector.Auth).
// The refresh token is the durable secret; the short-lived access token is
// re-minted from it each Sync and never stored.
type authState struct {
	RefreshToken string   `json:"refresh_token"`
	Owner        string   `json:"owner_email"`
	Scopes       []string `json:"scopes"`
}

// cursorState is the persisted incremental watermark: Gmail's historyId.
type cursorState struct {
	HistoryID string `json:"history_id"`
}

// authPayload is the connect request the transport hands to Authenticate:
// the OAuth authorization code and the redirect URI it was issued against.
type authPayload struct {
	Code        string `json:"code"`
	RedirectURI string `json:"redirect_uri"`
}

// AuthRequestFrom packages an OAuth callback's code into the opaque
// connector AuthRequest the callback handler passes to Authenticate.
func AuthRequestFrom(code, redirectURI string) (connector.AuthRequest, error) {
	payload, err := json.Marshal(authPayload{Code: code, RedirectURI: redirectURI})
	if err != nil {
		return connector.AuthRequest{}, fmt.Errorf("gmail: encoding auth payload: %w", err)
	}
	return connector.AuthRequest{Payload: payload}, nil
}

// Descriptor is the connector's static metadata: name "gmail", read-only
// (TierGreen), producing activities. Read at registration.
func (c *Connector) Descriptor() connector.Descriptor {
	return connector.Descriptor{
		Name:     connectorName,
		Version:  "1",
		Scopes:   []principal.Scope{principal.ScopeRead},
		RiskTier: mcp.TierGreen, // read-only capture
		Produces: []datasource.EntityType{datasource.EntityActivity},
	}
}

// Authenticate exchanges the authorization code for a refresh token, resolves
// the mailbox owner, and returns the opaque Auth the registry seals into the
// vault. The access token is discarded — only the refresh token persists.
func (c *Connector) Authenticate(ctx context.Context, req connector.AuthRequest) (connector.Auth, error) {
	var p authPayload
	if err := json.Unmarshal(req.Payload, &p); err != nil {
		return nil, fmt.Errorf("gmail: malformed auth payload: %w", err)
	}
	if p.Code == "" {
		return nil, fmt.Errorf("gmail: authorization code required: %w", ErrAuthRejected)
	}
	refresh, err := c.oauth.Exchange(ctx, p.Code, p.RedirectURI)
	if err != nil {
		return nil, err
	}
	access, err := c.oauth.AccessToken(ctx, refresh)
	if err != nil {
		return nil, err
	}
	owner, _, err := c.api.Profile(ctx, access)
	if err != nil {
		return nil, err
	}
	state := authState{RefreshToken: refresh, Owner: owner, Scopes: scopeStrings(c.Descriptor().Scopes)}
	//nolint:gosec // G117: sealing the connector's own refresh token into the opaque Auth bundle IS the intended path — the registry stores it encrypted in the vault, never logged or returned
	auth, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("gmail: encoding auth state: %w", err)
	}
	return auth, nil
}

// Sync mints a fresh access token, then pulls incrementally: with no cursor
// it anchors at the mailbox's current historyId and backfills a bounded
// window; with a cursor it walks the history API for messages added since.
// A cursor Gmail no longer holds (ErrHistoryGone) degrades to a bounded
// re-list rather than a full re-scan. The advanced historyId is returned as
// the new cursor; the registry persists it only on a fully-successful Sync.
func (c *Connector) Sync(ctx context.Context, auth connector.Auth, cursor connector.Cursor, sink connector.Sink) (connector.Cursor, error) {
	var st authState
	if err := json.Unmarshal(auth, &st); err != nil {
		return nil, fmt.Errorf("gmail: malformed auth state: %w", err)
	}
	c.owner = st.Owner

	access, err := c.oauth.AccessToken(ctx, st.RefreshToken)
	if err != nil {
		return nil, err
	}

	start := parseCursor(cursor)
	ids, nextHistory, err := c.selectMessages(ctx, access, start)
	if err != nil {
		return nil, err
	}

	contacts := map[string]struct{}{}
	for _, id := range ids {
		raw, err := c.api.GetRaw(ctx, access, id)
		if err != nil {
			// A fetch fault is transient — stop the pull without advancing the
			// cursor so the next cycle retries from the same watermark.
			return nil, err
		}
		if err := c.captureOne(ctx, raw, sink, contacts); err != nil {
			return nil, err
		}
	}
	c.stats.Contacts = len(contacts)

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

// captureOne parses, drops, or upserts one raw message, tallying the outcome
// — the same accounting the IMAP connector uses. A parse failure or a
// deliberate skip is counted, never fatal; only a real Sink write fault
// returns a non-nil error (which stops the pull).
func (c *Connector) captureOne(ctx context.Context, raw []byte, sink connector.Sink, contacts map[string]struct{}) error {
	msg, err := mailmap.Parse(raw, c.owner)
	if err != nil {
		c.stats.Skipped++
		return nil //nolint:nilerr // a single unparseable message is a counted skip, not a fatal pull error (mirrors the IMAP connector)
	}
	if _, drop := msg.SkipReason(); drop {
		c.stats.Skipped++
		return nil
	}
	if _, err := sink.Upsert(ctx, msg.ToRecord(connectorName, raw)); err != nil {
		if errors.Is(err, connector.ErrSkip) {
			c.stats.Skipped++
			return nil
		}
		return err
	}
	c.stats.Captured++
	if cp := msg.Counterparty(); cp != "" {
		contacts[strings.ToLower(cp)] = struct{}{}
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

// HealthCheck confirms the stored credential still mints a token and the
// mailbox answers. An outage degrades capture but never blocks core CRM.
func (c *Connector) HealthCheck(ctx context.Context, auth connector.Auth) error {
	var st authState
	if err := json.Unmarshal(auth, &st); err != nil {
		return fmt.Errorf("gmail: malformed auth state: %w", err)
	}
	access, err := c.oauth.AccessToken(ctx, st.RefreshToken)
	if err != nil {
		return err
	}
	if _, _, err := c.api.Profile(ctx, access); err != nil {
		return err
	}
	return nil
}

// Stats returns the outcome of the last Sync on this connector.
func (c *Connector) Stats() Stats { return c.stats }

func parseCursor(cur connector.Cursor) string {
	if len(cur) == 0 {
		return ""
	}
	var cs cursorState
	if err := json.Unmarshal(cur, &cs); err != nil {
		// An unreadable cursor is treated as absent — the next Sync re-anchors
		// via a bounded backfill rather than failing.
		return ""
	}
	return cs.HistoryID
}

func marshalCursor(historyID string) connector.Cursor {
	// cursorState has only string fields, so Marshal cannot fail here.
	b, _ := json.Marshal(cursorState{HistoryID: historyID}) //nolint:errchkjson // string-only struct never errors
	return b
}

func scopeStrings(scopes []principal.Scope) []string {
	out := make([]string, 0, len(scopes))
	for _, s := range scopes {
		out = append(out, string(s))
	}
	return out
}
