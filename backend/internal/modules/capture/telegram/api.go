// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package telegram

// The Bot API I/O itself. Every call goes through one request helper and one
// status verdict, so the sentinel a caller sees cannot depend on which method
// it happened to call — the connect ordering branches on those sentinels, and a
// per-call-site classification would let the same 401 mean different things.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// OutboundChannelMessage is one message to send into a chat.
type OutboundChannelMessage struct {
	ChatID int64
	Text   string
	// ReplyToMessageID threads the message under an existing one; 0 = start
	// fresh. Telegram has no header-based threading, so the parent id IS the
	// thread identity.
	ReplyToMessageID int64
}

// Bot is what getMe reports: the bot's global numeric id (the channel_id a
// connection is keyed on) and its @username (display only — a bot's username
// is mutable and re-assignable, which is exactly why it is not the key).
type Bot struct {
	ID       int64
	Username string
}

// WebhookInfo is the part of getWebhookInfo the connect preflight reads: the
// URL Telegram currently delivers this bot's updates to. Empty means no
// webhook is registered.
type WebhookInfo struct {
	URL string
}

// API is the Telegram Bot API surface. Every call carries the bot token
// explicitly rather than binding one per instance, because a single composed
// client serves every workspace's connection and a token rotation must not
// need a new client.
type API interface {
	// GetMe validates the token and identifies the bot behind it.
	GetMe(ctx context.Context, token string) (Bot, error)
	// GetWebhookInfo reports where Telegram currently delivers this bot's
	// updates — the preflight that catches a bot already wired to another
	// installation, which no local constraint can see.
	GetWebhookInfo(ctx context.Context, token string) (WebhookInfo, error)
	// SetWebhook points the bot's updates at url, authenticated by secret
	// (echoed back in X-Telegram-Bot-Api-Secret-Token on every delivery) and
	// narrowed to the allowed update kinds.
	SetWebhook(ctx context.Context, token, url, secret string, allowed []string) error
	// DeleteWebhook stops Telegram delivering this bot's updates to us.
	DeleteWebhook(ctx context.Context, token string) error
	// SendMessage transmits one message and returns Telegram's message id for
	// it — the id a later reply threads under.
	SendMessage(ctx context.Context, token string, m OutboundChannelMessage) (messageID int64, err error)
}

// apiBase is Telegram's Bot API origin. Overridable through NewAPI so the
// tests drive a local server, never the real host.
const apiBase = "https://api.telegram.org"

// httpTimeout bounds every Bot API call. http.DefaultClient has no timeout, so
// without this a stalled Telegram request would pin the admin's connect
// request (and, on the send path, a worker slot) indefinitely.
const httpTimeout = 30 * time.Second

// maxResponseBytes bounds every response read. These are small JSON documents
// (a Bot, a WebhookInfo, a sent-message id); the cap exists so a compromised or
// misconfigured host cannot exhaust memory by answering with an endless body.
const maxResponseBytes = 1 << 20

// httpAPI is the real Bot API client.
type httpAPI struct {
	client *http.Client
	base   string
}

// NewAPI builds the Bot API client. A nil client gets one with the bounded
// timeout; an empty base gets Telegram's own origin.
//
//nolint:ireturn // returns the API seam by design — the channel store holds it as an interface so tests substitute a fake
func NewAPI(client *http.Client, base string) API {
	if client == nil {
		client = &http.Client{Timeout: httpTimeout}
	}
	if base == "" {
		base = apiBase
	}
	return &httpAPI{client: client, base: strings.TrimRight(base, "/")}
}

// GetMe identifies the bot the token belongs to.
func (a *httpAPI) GetMe(ctx context.Context, token string) (Bot, error) {
	var out struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	}
	if err := a.call(ctx, token, "getMe", nil, &out); err != nil {
		return Bot{}, err
	}
	if out.ID == 0 {
		// A 2xx getMe with no bot id is not a bot we can key a connection on;
		// treating it as success would write a row keyed on "0".
		return Bot{}, fmt.Errorf("getMe answered without a bot id: %w", ErrRequestRejected)
	}
	return Bot{ID: out.ID, Username: out.Username}, nil
}

// GetWebhookInfo reports the bot's current webhook registration.
func (a *httpAPI) GetWebhookInfo(ctx context.Context, token string) (WebhookInfo, error) {
	var out struct {
		URL string `json:"url"`
	}
	if err := a.call(ctx, token, "getWebhookInfo", nil, &out); err != nil {
		return WebhookInfo{}, err
	}
	return WebhookInfo{URL: out.URL}, nil
}

// SetWebhook registers the delivery target, its secret, and the update kinds
// this installation consumes.
func (a *httpAPI) SetWebhook(ctx context.Context, token, webhookURL, secret string, allowed []string) error {
	body := map[string]any{"url": webhookURL, "secret_token": secret}
	if len(allowed) > 0 {
		body["allowed_updates"] = allowed
	}
	return a.call(ctx, token, "setWebhook", body, nil)
}

// DeleteWebhook unregisters the delivery target.
func (a *httpAPI) DeleteWebhook(ctx context.Context, token string) error {
	return a.call(ctx, token, "deleteWebhook", nil, nil)
}

// SendMessage transmits one message into a chat.
func (a *httpAPI) SendMessage(ctx context.Context, token string, m OutboundChannelMessage) (int64, error) {
	body := map[string]any{"chat_id": m.ChatID, "text": m.Text}
	if m.ReplyToMessageID != 0 {
		// reply_parameters is the current spelling; the deprecated
		// reply_to_message_id is not sent, so a Telegram deprecation cannot
		// silently drop the threading this system relies on.
		body["reply_parameters"] = map[string]any{"message_id": m.ReplyToMessageID}
	}
	var out struct {
		MessageID int64 `json:"message_id"`
	}
	if err := a.call(ctx, token, "sendMessage", body, &out); err != nil {
		return 0, err
	}
	if out.MessageID == 0 {
		// ok=true is Telegram ACCEPTING the message, so it may well be on its
		// way — but without the provider's id there is nothing a later reply can
		// thread under and nothing to record as a receipt. The outcome is
		// therefore unknowable rather than refused, and it takes the
		// reachability sentinel: the answer never arrived in usable form, which
		// for a send is the same fact as no answer at all. Read as a refusal it
		// would invite a retry that delivers a second copy.
		return 0, fmt.Errorf("sendMessage answered without a message id: %w", ErrUnreachable)
	}
	return out.MessageID, nil
}

// envelope is the Bot API's uniform response wrapper: `ok` plus either
// `result` or a `description` of the refusal.
type envelope struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
	// Parameters is the Bot API's structured refusal detail. Only retry_after
	// is read — see retryAfterOf, which is where a throttle's interval comes
	// from.
	Parameters struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// call performs one Bot API method and decodes its `result` into out (nil when
// the caller only needs the success/failure verdict). A nil body sends a bare
// POST — every Bot API method accepts POST, so one code path serves the reads
// and the writes alike.
//
// out is the caller-supplied decode target, so its concrete type varies per
// method.
//
//craft:ignore naked-any out is the caller's per-method JSON decode target — one shape per Bot API method
func (a *httpAPI) call(ctx context.Context, token, method string, body map[string]any, out any) error {
	req, err := a.request(ctx, token, method, body)
	if err != nil {
		return err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		// A transport failure carries no provider verdict: the token may be
		// perfect and Telegram simply unreachable.
		return fmt.Errorf("telegram: %s: %w", method, ErrUnreachable)
	}
	//craft:ignore swallowed-errors best-effort close of the response body — the decoded envelope is what matters
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		// LimitReader signals the cap with EOF, not an error, so a genuine
		// read fault here is a reachability failure and is reported as one
		// rather than as an unparseable body.
		return fmt.Errorf("telegram: reading the %s response: %w", method, ErrUnreachable)
	}
	// A non-2xx Bot API response still carries the envelope, so the status is
	// classified first and the description is folded into the error for the
	// server-side log — the transport never puts it on the wire.
	var env envelope
	decodeErr := json.Unmarshal(raw, &env)
	if err := classify(resp.StatusCode, method, env.Description, retryAfterOf(resp, env)); err != nil {
		return err
	}
	if decodeErr != nil {
		return fmt.Errorf("telegram: decoding the %s response: %w", method, ErrUnreachable)
	}
	if !env.OK {
		// A 2xx with ok=false is Telegram refusing on its own terms.
		return fmt.Errorf("telegram: %s refused: %s: %w", method, env.Description, ErrRequestRejected)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		return fmt.Errorf("telegram: decoding the %s result: %w", method, ErrUnreachable)
	}
	return nil
}

// request builds one authorized Bot API request. The token rides the PATH,
// which is Telegram's scheme — hence url.PathEscape, so a pasted token
// containing a slash cannot reach a method the caller did not name.
func (a *httpAPI) request(ctx context.Context, token, method string, body map[string]any) (*http.Request, error) {
	endpoint := a.base + "/bot" + url.PathEscape(token) + "/" + method
	if body == nil {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("telegram: building the %s request: %w", method, err)
		}
		return req, nil
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("telegram: encoding the %s request: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("telegram: building the %s request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// maxRetryAfter caps a throttle interval this package will report. Telegram's
// real values are seconds to about a minute; the cap exists because the send
// path SNOOZES a delivery for whatever interval comes back, so one malformed or
// hostile response must not be able to take a message out of circulation for
// longer than the retry ladder that bounds it.
const maxRetryAfter = 5 * time.Minute

// retryAfterOf reads how long Telegram says to wait before the next request.
// The Bot API states it in the envelope's `parameters.retry_after` (seconds);
// the standard Retry-After header is read only as a fallback, for a 429
// answered by something in front of the Bot API that carries no envelope.
//
// Zero means neither named an interval, and the caller falls back to its own
// backoff — which is the one case where guessing is better than waiting
// forever.
func retryAfterOf(resp *http.Response, env envelope) time.Duration {
	seconds := env.Parameters.RetryAfter
	if seconds <= 0 {
		header, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After")))
		if err != nil {
			return 0
		}
		seconds = header
	}
	if seconds <= 0 {
		return 0
	}
	return min(time.Duration(seconds)*time.Second, maxRetryAfter)
}

// classify is the ONE status verdict for every Bot API call, so the connect
// path's branching cannot disagree with the send path's about what the same
// status means. It returns nil for a 2xx.
//
// description is Telegram's own refusal text: carried into the wrapped error
// for the server-side log, never onto the wire. retryAfter is the interval
// Telegram stated for a throttle, zero when it stated none.
func classify(status int, method, description string, retryAfter time.Duration) error {
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == http.StatusUnauthorized, status == http.StatusNotFound:
		// 404 belongs here, not with the server faults: Telegram answers it
		// for a token that does not name a bot, because the token is part of
		// the path.
		return fmt.Errorf("telegram: %s: %s: %w", method, description, ErrTokenRejected)
	case status == http.StatusForbidden:
		// 403 is Telegram refusing a CHAT, never a credential: "bot was blocked
		// by the user", "user is deactivated", a bot removed from a group. A
		// revoked or malformed token answers 401/404 instead, so the two are
		// cleanly separable — and separating them is what stops the commonest
		// send failure a channel has from being reported as a token to rotate.
		//
		// It ALSO answers ErrRequestRejected, joined rather than replaced, for
		// the same reason a 429 does: to the connect transport a 403 is one more
		// request Telegram understood and refused on its own terms, and that
		// reader must keep classifying it without being taught about recipients.
		return errors.Join(
			fmt.Errorf("telegram: %s: %s: %w", method, description, ErrRecipientUnreachable),
			ErrRequestRejected,
		)
	case status == http.StatusTooManyRequests:
		// A throttle is a DEFINITE answer — Telegram refused this request, so
		// nothing was transmitted — and the interval it names is the one the
		// caller must wait. Backing off on a schedule of our own earns a harder
		// limit, so the interval travels in the shared RateLimitedError the send
		// ladder reads with errors.As.
		//
		// It ALSO answers ErrRequestRejected, joined rather than replaced,
		// because that is what a 429 has always been to the connect transport:
		// a request Telegram understood and refused on its own terms. Two
		// readers, one error, neither taught about the other.
		return errors.Join(
			fmt.Errorf("telegram: %s: %s: %w", method, description, ErrRequestRejected),
			&connector.RateLimitedError{RetryAfter: retryAfter},
		)
	case status >= 500:
		return fmt.Errorf("telegram: %s: upstream status %d: %w", method, status, ErrUnreachable)
	default:
		return fmt.Errorf("telegram: %s: %s: %w", method, description, ErrRequestRejected)
	}
}
