// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package graph is a read-only Microsoft 365 (Outlook) capture connector: it
// authorizes a user's mailbox over the Microsoft identity platform, pulls
// mail incrementally through the Graph inbox delta query, and normalizes each
// message into an email activity. It implements connector.Connector, so every
// captured row lands through the ONE capture Sink (audit + outbox in one
// transaction) — this package owns the provider I/O (client.go) and composes
// the pure RFC822 mapping (capture/mailmap; Graph serves each message's MIME
// via /$value), nothing about the write.
//
// Like Gmail, a Graph connection is standing: the refresh token is persisted
// (via the registry → keyvault) and the sync cursor is Graph's deltaLink, so
// each Sync resumes where the last left off and is idempotent on
// (graph, RFC822 Message-ID).
package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/capture/mailmap"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

const (
	connectorName = "graph"

	// anchorWindow bounds the first sync (before any deltaLink exists) and the
	// delta-gone re-anchor: the initial delta is filtered to messages received
	// this recently, so a large mailbox does not stream unbounded on connect.
	// The deliberate historical pull is the bounded backfill (backfill.go);
	// steady-state sync is delta-only.
	anchorWindow = 7 * 24 * time.Hour
)

// Connector authorizes and syncs Microsoft 365 mailboxes. It holds NO
// per-mailbox or per-run state: one instance is registered and shared, and
// every Sync derives the owner + counters as locals so concurrent syncs of
// different connections never race. (owner is a field only for the pure
// Normalize surface, which is test-only — Sync never touches it.)
type Connector struct {
	oauth OAuth
	api   API
	owner string // used ONLY by Normalize (the test-guarded pure mapping); never set by Sync
	// now anchors the initial-sync window; a field so tests pin the clock.
	now func() time.Time
}

// New returns a Graph connector over the given OAuth + API surfaces.
func New(oauth OAuth, api API) *Connector {
	return &Connector{oauth: oauth, api: api, now: time.Now}
}

var (
	_ connector.Connector  = (*Connector)(nil)
	_ connector.Backfiller = (*Connector)(nil)
)

// authState is the persisted credential bundle (the opaque connector.Auth).
// The refresh token is the durable secret; the short-lived access token is
// re-minted from it each Sync and never stored.
type authState struct {
	RefreshToken string   `json:"refresh_token"`
	Owner        string   `json:"owner_email"`
	Scopes       []string `json:"scopes"`
}

// cursorState is the persisted incremental watermark: Graph's deltaLink, plus
// the mailbox address the watermark belongs to — mirroring the Gmail cursor
// shape so anything that routes on sync_cursor->>'email' works unchanged.
type cursorState struct {
	DeltaLink string `json:"delta_link"`
	Email     string `json:"email"`
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
		return connector.AuthRequest{}, fmt.Errorf("graph: encoding auth payload: %w", err)
	}
	return connector.AuthRequest{Payload: payload}, nil
}

// Descriptor is the connector's static metadata: name "graph", read-only
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
		return nil, fmt.Errorf("graph: malformed auth payload: %w", err)
	}
	if p.Code == "" {
		return nil, fmt.Errorf("graph: authorization code required: %w", ErrAuthRejected)
	}
	refresh, err := c.oauth.Exchange(ctx, p.Code, p.RedirectURI)
	if err != nil {
		return nil, err
	}
	access, err := c.oauth.AccessToken(ctx, refresh)
	if err != nil {
		return nil, err
	}
	owner, err := c.api.Profile(ctx, access)
	if err != nil {
		return nil, err
	}
	state := authState{RefreshToken: refresh, Owner: owner, Scopes: scopeStrings(c.Descriptor().Scopes)}
	//nolint:gosec // G117: sealing the connector's own refresh token into the opaque Auth bundle IS the intended path — the registry stores it encrypted in the vault, never logged or returned
	auth, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("graph: encoding auth state: %w", err)
	}
	return auth, nil
}

// Sync mints a fresh access token, then pulls incrementally: with no cursor
// it starts a bounded initial delta (messages received within anchorWindow);
// with a cursor it resumes the stored deltaLink. A deltaLink Graph no longer
// honors (ErrDeltaGone, HTTP 410) degrades to the same bounded re-anchor
// rather than a full re-scan. The advanced deltaLink is returned as the new
// cursor; the registry persists it only on a fully-successful Sync.
func (c *Connector) Sync(ctx context.Context, auth connector.Auth, cursor connector.Cursor, sink connector.Sink) (connector.Cursor, error) {
	var st authState
	if err := json.Unmarshal(auth, &st); err != nil {
		return nil, fmt.Errorf("graph: malformed auth state: %w", err)
	}
	owner := st.Owner // local — never stored on the shared instance

	access, err := c.oauth.AccessToken(ctx, st.RefreshToken)
	if err != nil {
		return nil, err
	}

	start, err := parseCursor(cursor)
	if err != nil {
		// A stored cursor we can't read is a bug/corruption, NOT a fresh mailbox:
		// stop and let the next cycle retry rather than silently re-anchoring and
		// overwriting the watermark (which would drop everything in between).
		return nil, err
	}
	ids, nextDelta, err := c.selectMessages(ctx, access, start)
	if err != nil {
		return nil, err
	}

	for _, id := range ids {
		raw, err := c.api.GetMIME(ctx, access, id)
		if errors.Is(err, connector.ErrSkip) {
			// An oversized message is a per-message drop (truncated MIME is
			// not honest evidence), never a pull-stopping fault — the cursor
			// still advances past it.
			continue
		}
		if err != nil {
			// A fetch fault is transient — stop the pull without advancing the
			// cursor so the next cycle retries from the same watermark.
			return nil, err
		}
		if _, err := captureOne(ctx, raw, sink, owner); err != nil {
			return nil, err
		}
	}

	if nextDelta == "" {
		nextDelta = start // provider closed the round without a new link; keep the prior watermark
	}
	return marshalCursor(nextDelta, owner), nil
}

// selectMessages resolves which message ids to pull and the deltaLink to
// advance to, choosing the initial-anchor or the incremental path and folding
// the stale-cursor fallback into one place.
func (c *Connector) selectMessages(ctx context.Context, access, start string) ([]string, string, error) {
	if start == "" {
		return c.api.DeltaInit(ctx, access, c.now().Add(-anchorWindow))
	}
	ids, next, err := c.api.Delta(ctx, access, start)
	if errors.Is(err, ErrDeltaGone) {
		return c.api.DeltaInit(ctx, access, c.now().Add(-anchorWindow))
	}
	if err != nil {
		return nil, "", err
	}
	return ids, next, nil
}

// captureOne parses, drops, or upserts one raw message — the same discipline
// the Gmail and IMAP connectors use. A parse failure or a deliberate skip is
// a no-op; only a real Sink write fault returns a non-nil error (which stops
// the pull). It is a package function (no receiver) so a pull holds no shared
// state.
func captureOne(ctx context.Context, raw []byte, sink connector.Sink, owner string) (captured bool, err error) {
	msg, err := mailmap.Parse(raw, owner)
	if err != nil {
		return false, nil //nolint:nilerr // a single unparseable message is a skip, not a fatal pull error (mirrors the Gmail connector)
	}
	if _, drop := msg.SkipReason(); drop {
		return false, nil
	}
	if _, err := sink.Upsert(ctx, msg.ToRecord(connectorName, raw)); err != nil {
		if errors.Is(err, connector.ErrSkip) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Normalize maps ONE raw RFC822 message (a Graph /$value payload) to its
// activity record. Pure — no I/O — so the mapping is the test-guarded
// surface; it returns an ErrSkip-wrapped error for mail this connector
// intentionally drops.
func (c *Connector) Normalize(_ context.Context, raw connector.RawRecord) ([]connector.NormalizedRecord, error) {
	msg, err := mailmap.Parse(raw, c.owner)
	if err != nil {
		return nil, err
	}
	if reason, drop := msg.SkipReason(); drop {
		return nil, fmt.Errorf("graph: dropping %s (%s): %w", msg.ID(), reason, connector.ErrSkip)
	}
	return []connector.NormalizedRecord{msg.ToRecord(connectorName, raw)}, nil
}

// HealthCheck confirms the stored credential still mints a token and the
// mailbox answers. An outage degrades capture but never blocks core CRM.
func (c *Connector) HealthCheck(ctx context.Context, auth connector.Auth) error {
	var st authState
	if err := json.Unmarshal(auth, &st); err != nil {
		return fmt.Errorf("graph: malformed auth state: %w", err)
	}
	access, err := c.oauth.AccessToken(ctx, st.RefreshToken)
	if err != nil {
		return err
	}
	if _, err := c.api.Profile(ctx, access); err != nil {
		return err
	}
	return nil
}

// parseCursor reads the stored watermark. An empty cursor means a genuinely
// fresh mailbox (→ bounded initial delta); a NON-empty but unreadable cursor
// is an error, not a silent re-anchor — the caller stops rather than
// re-anchor and overwrite the watermark (which would drop everything in
// between).
func parseCursor(cur connector.Cursor) (string, error) {
	if len(cur) == 0 {
		return "", nil
	}
	var cs cursorState
	if err := json.Unmarshal(cur, &cs); err != nil {
		return "", fmt.Errorf("graph: unreadable sync cursor: %w", err)
	}
	return cs.DeltaLink, nil
}

func marshalCursor(deltaLink, email string) connector.Cursor {
	// cursorState has only string fields, so Marshal cannot fail here.
	b, _ := json.Marshal(cursorState{DeltaLink: deltaLink, Email: email}) //nolint:errchkjson // string-only struct never errors
	return b
}

func scopeStrings(scopes []principal.Scope) []string {
	out := make([]string, 0, len(scopes))
	for _, s := range scopes {
		out = append(out, string(s))
	}
	return out
}
