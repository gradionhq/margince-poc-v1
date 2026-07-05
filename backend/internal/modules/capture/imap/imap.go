// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package imap is a read-only mail-capture connector: it dials a user's
// mailbox over IMAPS, pulls the most recent messages, and normalizes each
// into an email activity for the timeline. It implements
// connector.Connector, so every captured row still lands through the ONE
// capture Sink (audit + outbox in one transaction) — this package owns the
// provider I/O and the pure RFC822→activity mapping, nothing about the write.
//
// It is a ONE-SHOT puller: credentials are supplied per call and never
// persisted (no standing connector_connection row, no cursor). The
// compose layer builds a fresh Connector per request, so its per-run
// counters carry no cross-request state.
package imap

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	imapclient "github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

const (
	connectorName = "imap"

	defaultPort        = 993
	defaultMailbox     = "INBOX"
	defaultMaxMessages = 50
	maxMessagesCap     = 200

	// dialTimeout bounds the TLS connect; commandTimeout bounds each IMAP
	// command so a wedged server cannot hang the request indefinitely.
	dialTimeout    = 15 * time.Second
	commandTimeout = 45 * time.Second

	// maxBodyLen caps the stored email body — the timeline needs a legible
	// excerpt, not the full multi-megabyte thread with quoted history.
	maxBodyLen = 8000
)

// Connector pulls recent mail from one mailbox. It is stateful for the
// span of a single authenticate→sync→close request (it holds the live
// client and the per-run counters); compose constructs a fresh one per
// call, so nothing leaks between requests.
type Connector struct {
	conn  *client.Client
	owner string
	stats Stats
}

// Stats is the outcome of one pull, surfaced to the caller's summary.
type Stats struct {
	Captured int // messages that landed as activities (new or idempotent replay)
	Skipped  int // messages intentionally dropped (automated/system mail, unparseable)
	Contacts int // distinct counterparties seen across the captured messages
}

// New returns an unauthenticated connector ready for one pull.
func New() *Connector { return &Connector{} }

var _ connector.Connector = (*Connector)(nil)

// authConfig is the non-secret handshake state Authenticate hands to Sync
// via the opaque Auth bytes. The password is NEVER placed here: it is
// consumed at login time and does not survive the handshake.
type authConfig struct {
	Owner       string `json:"owner"`
	Mailbox     string `json:"mailbox"`
	MaxMessages int    `json:"max_messages"`
}

// Credentials is the request payload the transport hands to Authenticate.
// The transport marshals it into the opaque AuthRequest.Payload; the
// password lives here only for the span of the login and is never persisted.
type Credentials struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Email       string `json:"email"`
	Password    string `json:"password"`
	Mailbox     string `json:"mailbox"`
	MaxMessages int    `json:"max_messages"`
}

// AuthRequestFrom packages credentials into the opaque connector AuthRequest.
func AuthRequestFrom(creds Credentials) (connector.AuthRequest, error) {
	payload, err := json.Marshal(creds) //nolint:gosec // transient credentials — marshaled into the opaque AuthRequest for this one call, never persisted or logged
	if err != nil {
		return connector.AuthRequest{}, fmt.Errorf("imap: encoding credentials: %w", err)
	}
	return connector.AuthRequest{Payload: payload}, nil
}

// ErrLoginRejected marks an authentication failure the server reported
// (bad user/password/host). The transport maps it to an actionable 422
// without echoing the provider's raw message.
var ErrLoginRejected = errors.New("imap: the mailbox rejected these credentials")

// ErrUnreachable marks a transport-level failure reaching the server
// (DNS, TCP, TLS, timeout). The transport maps it to a 502 without
// leaking the underlying network detail.
var ErrUnreachable = errors.New("imap: could not reach the mail server")

func (c *Connector) Descriptor() connector.Descriptor {
	return connector.Descriptor{
		Name:     connectorName,
		Version:  "1",
		Scopes:   []principal.Scope{principal.ScopeRead},
		RiskTier: mcp.TierGreen, // read-only capture
		Produces: []datasource.EntityType{datasource.EntityActivity},
	}
}

// Authenticate parses the credentials, dials the mailbox over TLS, and
// verifies the login. It returns the opaque Auth carrying only the
// non-secret run configuration — the password is used here and discarded.
// On failure it returns a sentinel (login vs unreachable) so the transport
// can answer with the right status and never leaks the raw provider error.
func (c *Connector) Authenticate(ctx context.Context, req connector.AuthRequest) (connector.Auth, error) {
	var creds Credentials
	if err := json.Unmarshal(req.Payload, &creds); err != nil {
		return nil, fmt.Errorf("imap: malformed credentials payload: %w", err)
	}
	creds.Host = strings.TrimSpace(creds.Host)
	creds.Email = strings.TrimSpace(creds.Email)
	if creds.Host == "" || creds.Email == "" || creds.Password == "" {
		return nil, fmt.Errorf("imap: host, email and password are all required: %w", ErrLoginRejected)
	}
	port := creds.Port
	if port == 0 {
		port = defaultPort
	}
	mailbox := strings.TrimSpace(creds.Mailbox)
	if mailbox == "" {
		mailbox = defaultMailbox
	}
	maxMessages := creds.MaxMessages
	switch {
	case maxMessages <= 0:
		maxMessages = defaultMaxMessages
	case maxMessages > maxMessagesCap:
		maxMessages = maxMessagesCap
	}

	addr := net.JoinHostPort(creds.Host, fmt.Sprintf("%d", port))
	conn, err := client.DialWithDialerTLS(&net.Dialer{Timeout: dialTimeout}, addr, &tls.Config{ServerName: creds.Host, MinVersion: tls.VersionTLS12})
	if err != nil {
		// A dial failure is a reachability problem, not a credential one —
		// wrap the sentinel and drop the raw cause so no host/network
		// internal reaches the client.
		return nil, fmt.Errorf("imap: dial %s: %w", addr, ErrUnreachable)
	}
	conn.Timeout = commandTimeout
	if err := conn.Login(creds.Email, creds.Password); err != nil {
		//craft:ignore swallowed-errors best-effort close of a session whose login already failed — the rejection is the error to report
		_ = conn.Logout()
		return nil, ErrLoginRejected
	}
	c.conn = conn

	cfg := authConfig{Owner: creds.Email, Mailbox: mailbox, MaxMessages: maxMessages}
	auth, err := json.Marshal(cfg)
	if err != nil {
		//craft:ignore swallowed-errors best-effort close after an encode failure we already surface — a logout error has no recovery path here
		_ = conn.Logout()
		return nil, fmt.Errorf("imap: encoding run config: %w", err)
	}
	return auth, nil
}

// Sync selects the mailbox, fetches the most recent MaxMessages, and emits
// each as an email activity through the Sink. The cursor is unused — this
// is a bounded one-shot pull, not an incremental history walk — so the
// returned cursor is always empty. Per-message parse failures and
// intentionally-dropped mail are counted, never fatal; a Sink error (a real
// write fault) stops the pull.
func (c *Connector) Sync(ctx context.Context, auth connector.Auth, _ connector.Cursor, sink connector.Sink) (connector.Cursor, error) {
	if c.conn == nil {
		return nil, errors.New("imap: Sync called before Authenticate")
	}
	//craft:ignore swallowed-errors deferred best-effort close of the read-only session — the pull's own result (records or error) is the outcome, a logout failure changes nothing
	defer func() { _ = c.conn.Logout() }()

	var cfg authConfig
	if err := json.Unmarshal(auth, &cfg); err != nil {
		return nil, fmt.Errorf("imap: malformed auth state: %w", err)
	}
	c.owner = cfg.Owner

	mbox, err := c.conn.Select(cfg.Mailbox, true)
	if err != nil {
		return nil, fmt.Errorf("imap: selecting mailbox %q: %w", cfg.Mailbox, ErrUnreachable)
	}
	if mbox.Messages == 0 {
		return nil, nil
	}

	// Re-clamp defensively before the conversion: MaxMessages was bounded
	// 1..200 at Authenticate, so this is a safe, non-overflowing narrowing.
	maxN := cfg.MaxMessages
	if maxN <= 0 || maxN > maxMessagesCap {
		maxN = defaultMaxMessages
	}
	window := uint32(maxN) //nolint:gosec // bounded 1..200 just above — no overflow
	from := uint32(1)
	if mbox.Messages > window {
		from = mbox.Messages - window + 1
	}
	seqset := new(imapclient.SeqSet)
	seqset.AddRange(from, mbox.Messages)

	section := &imapclient.BodySectionName{}
	messages := make(chan *imapclient.Message, 16)
	fetchErr := make(chan error, 1)
	go func() {
		fetchErr <- c.conn.Fetch(seqset, []imapclient.FetchItem{section.FetchItem()}, messages)
	}()

	contacts := map[string]struct{}{}
	for msg := range messages {
		raw, err := readSection(msg, section)
		if err != nil {
			c.stats.Skipped++
			continue
		}
		parsed, err := parseMessage(raw, c.owner)
		if err != nil {
			// A single unparseable message is dropped, not fatal — one bad
			// MIME structure must not abort the whole pull.
			c.stats.Skipped++
			continue
		}
		if _, drop := parsed.skipReason(); drop {
			c.stats.Skipped++
			continue
		}
		rec := parsed.toRecord(raw)
		if _, err := sink.Upsert(ctx, rec); err != nil {
			if errors.Is(err, connector.ErrSkip) {
				// The Sink dropped it (e.g. an erased subject's suppression
				// list) — a deliberate skip, counted like any other.
				c.stats.Skipped++
				continue
			}
			return nil, err
		}
		c.stats.Captured++
		if parsed.counterparty != "" {
			contacts[strings.ToLower(parsed.counterparty)] = struct{}{}
		}
	}
	if err := <-fetchErr; err != nil {
		return nil, fmt.Errorf("imap: fetching messages: %w", ErrUnreachable)
	}
	c.stats.Contacts = len(contacts)
	return nil, nil
}

// Normalize maps ONE raw RFC822 message to its activity record. It is pure
// (no I/O) so the mapping is the test-guarded surface; it returns an
// ErrSkip-wrapped error for mail this connector intentionally drops.
func (c *Connector) Normalize(_ context.Context, raw connector.RawRecord) ([]connector.NormalizedRecord, error) {
	parsed, err := parseMessage(raw, c.owner)
	if err != nil {
		return nil, err
	}
	if reason, drop := parsed.skipReason(); drop {
		return nil, fmt.Errorf("imap: dropping %s (%s): %w", parsed.messageID, reason, connector.ErrSkip)
	}
	return []connector.NormalizedRecord{parsed.toRecord(raw)}, nil
}

// HealthCheck confirms the live session still answers. A one-shot pull
// never persists a connection, so this is only meaningful between
// Authenticate and Sync within a single request.
func (c *Connector) HealthCheck(_ context.Context, _ connector.Auth) error {
	if c.conn == nil {
		return errors.New("imap: no live session")
	}
	if err := c.conn.Noop(); err != nil {
		return fmt.Errorf("imap: session unhealthy: %w", ErrUnreachable)
	}
	return nil
}

// Stats returns the outcome of the last Sync on this connector.
func (c *Connector) Stats() Stats { return c.stats }

// readSection copies the fetched body literal into memory; the fetch
// channel reuses buffers, so the bytes must be read before the next message.
func readSection(msg *imapclient.Message, section *imapclient.BodySectionName) ([]byte, error) {
	literal := msg.GetBody(section)
	if literal == nil {
		return nil, errors.New("imap: message carried no body section")
	}
	return io.ReadAll(literal)
}
