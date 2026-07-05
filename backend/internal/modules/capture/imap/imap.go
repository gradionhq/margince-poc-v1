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
//
// It uses go-imap v2, whose FETCH exposes each body as a streamed reader: we
// read it through readCapped so the per-message allocation is bounded by
// maxRawLen, not by the size the (tenant-supplied, possibly hostile) server
// declares. v1's client buffered the whole literal up front with no reachable
// size limit — an unbounded-allocation DoS — so this connector is v2-only.
package imap

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	imapv2 "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/gradionhq/margince/backend/internal/platform/netguard"
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

	// dialTimeout bounds the TLS connect; pullDeadline bounds the whole
	// select+fetch phase so a wedged or dribbling server cannot hang the
	// request indefinitely (v2 has no per-command timeout, so we set a
	// deadline on the underlying connection ourselves).
	dialTimeout  = 15 * time.Second
	pullDeadline = 90 * time.Second

	// maxBodyLen caps the stored email body — the timeline needs a legible
	// excerpt, not the full multi-megabyte thread with quoted history.
	maxBodyLen = 8000

	// maxRawLen bounds how many bytes we read from any one message's streamed
	// body. The host is tenant-supplied and the server declares each literal's
	// size, so reading it whole would be a memory/storage-amplification vector:
	// a hostile server could stream a multi-gigabyte body. A message larger
	// than this is skipped, not truncated — a truncated MIME blob is neither
	// parseable nor honest evidence.
	maxRawLen = 2 << 20 // 2 MiB
)

// errMessageTooLarge marks a message whose body exceeds maxRawLen; Sync counts
// it as skipped rather than reading or persisting an unbounded blob.
var errMessageTooLarge = errors.New("imap: message exceeds the size cap")

// errNoBodySection marks a FETCH response that carried no BODY[] literal.
var errNoBodySection = errors.New("imap: message carried no body section")

// Connector pulls recent mail from one mailbox. It is stateful for the
// span of a single authenticate→sync→close request (it holds the live
// client and the per-run counters); compose constructs a fresh one per
// call, so nothing leaks between requests.
type Connector struct {
	conn    *imapclient.Client
	netConn net.Conn // the underlying TLS conn, kept only to refresh read deadlines
	owner   string
	stats   Stats
}

// Stats is the outcome of one pull, surfaced to the caller's summary.
type Stats struct {
	Mailbox  string // the mailbox actually selected (resolved default included)
	Captured int    // messages that landed as activities (new or idempotent replay)
	Skipped  int    // messages intentionally dropped (automated/system mail, unparseable, oversized)
	Contacts int    // distinct counterparties seen across the captured messages
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
func (c *Connector) Authenticate(_ context.Context, req connector.AuthRequest) (connector.Auth, error) {
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

	addr := net.JoinHostPort(creds.Host, strconv.Itoa(port))
	// The host is tenant-supplied, so guard egress: RefusePrivate blocks a
	// dial to any internal/reserved address at connect time (SSRF), and a
	// refusal reads as unreachable — the client never learns whether an
	// internal service answered.
	dialer := &net.Dialer{Timeout: dialTimeout, Control: netguard.RefusePrivate}
	tlsConn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: creds.Host, MinVersion: tls.VersionTLS12})
	if err != nil {
		// A dial failure is a reachability problem, not a credential one —
		// wrap the sentinel and drop the raw cause so no host/network
		// internal reaches the client.
		return nil, fmt.Errorf("imap: dial %s: %w", addr, ErrUnreachable)
	}
	// A deadline bounds every subsequent read on this connection; Sync refreshes
	// it before the pull. Without it a wedged server would hang the request
	// (v2 exposes no per-command timeout).
	//craft:ignore swallowed-errors SetDeadline only errors on a closed conn; we just dialed it, and a failure surfaces as the next read timing out
	_ = tlsConn.SetDeadline(time.Now().Add(pullDeadline))

	client := imapclient.New(tlsConn, &imapclient.Options{})
	if err := client.Login(creds.Email, creds.Password).Wait(); err != nil {
		//craft:ignore swallowed-errors best-effort close of a session whose login already failed — the rejection is the error to report
		_ = client.Close()
		return nil, ErrLoginRejected
	}

	cfg := authConfig{Owner: creds.Email, Mailbox: mailbox, MaxMessages: maxMessages}
	auth, err := json.Marshal(cfg)
	if err != nil {
		//craft:ignore swallowed-errors best-effort close after an encode failure we already surface — a close error has no recovery path here
		_ = client.Close()
		return nil, fmt.Errorf("imap: encoding run config: %w", err)
	}
	// Live session is established: hand ownership to the connector. From here
	// the caller MUST Close() on every exit path — the handler defers it right
	// after a successful Authenticate.
	c.conn = client
	c.netConn = tlsConn
	return auth, nil
}

// Close releases the live IMAP session. It is idempotent and safe on every
// exit path — including Authenticate-succeeded-but-Sync-never-reached, where
// the leaked fd and the client's background read goroutine would otherwise
// live for the life of the process. The handler defers it after Authenticate.
func (c *Connector) Close() error {
	if c.conn == nil {
		return nil
	}
	conn := c.conn
	c.conn = nil
	//craft:ignore swallowed-errors best-effort polite logout before the close that actually frees the fd + reader goroutine
	_ = conn.Logout().Wait()
	return conn.Close()
}

// Sync selects the mailbox, fetches the most recent MaxMessages, and emits
// each as an email activity through the Sink. The cursor is unused — this
// is a bounded one-shot pull, not an incremental history walk — so the
// returned cursor is always empty. Per-message parse failures, oversized
// bodies and intentionally-dropped mail are counted, never fatal; a Sink
// error (a real write fault) stops the pull.
func (c *Connector) Sync(ctx context.Context, auth connector.Auth, _ connector.Cursor, sink connector.Sink) (connector.Cursor, error) {
	if c.conn == nil {
		return nil, errors.New("imap: Sync called before Authenticate")
	}
	// The session is closed by the caller's deferred Close() (the handler),
	// which runs on every exit path — not just the ones that reach here.

	var cfg authConfig
	if err := json.Unmarshal(auth, &cfg); err != nil {
		return nil, fmt.Errorf("imap: malformed auth state: %w", err)
	}
	c.owner = cfg.Owner
	c.stats.Mailbox = cfg.Mailbox
	if c.netConn != nil {
		//craft:ignore swallowed-errors refreshing the read deadline for the pull; a closed conn surfaces as the next read failing
		_ = c.netConn.SetDeadline(time.Now().Add(pullDeadline))
	}

	selData, err := c.conn.Select(cfg.Mailbox, &imapv2.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return nil, fmt.Errorf("imap: selecting mailbox %q: %w", cfg.Mailbox, ErrUnreachable)
	}
	if selData.NumMessages == 0 {
		return nil, nil
	}

	window := uint32(maxMessagesCap)
	if cfg.MaxMessages > 0 && cfg.MaxMessages <= maxMessagesCap {
		window = uint32(cfg.MaxMessages) //nolint:gosec // bounded 1..200 by the guard above — no overflow
	}
	from := uint32(1)
	if selData.NumMessages > window {
		from = selData.NumMessages - window + 1
	}
	seqset := imapv2.SeqSet{}
	seqset.AddRange(from, selData.NumMessages)

	fetchOpts := &imapv2.FetchOptions{BodySection: []*imapv2.FetchItemBodySection{{}}}
	fetchCmd := c.conn.Fetch(seqset, fetchOpts)

	contacts := map[string]struct{}{}
	var writeErr error
	for {
		msg := fetchCmd.Next()
		if msg == nil {
			break
		}
		raw, err := readMessageBody(msg)
		if err != nil {
			// Oversized or bodyless — dropped, not fatal.
			c.stats.Skipped++
			continue
		}
		if err := c.capture(ctx, raw, sink, contacts); err != nil {
			writeErr = err
			break
		}
	}
	// Close finalizes the command, discarding any messages left unread when the
	// loop broke early (v2's iteration is synchronous, so there is no producer
	// goroutine to deadlock — unlike v1).
	if err := fetchCmd.Close(); err != nil && writeErr == nil {
		return nil, fmt.Errorf("imap: fetching messages: %w", ErrUnreachable)
	}
	if writeErr != nil {
		return nil, writeErr
	}
	c.stats.Contacts = len(contacts)
	return nil, nil
}

// capture processes one message's raw bytes: parse, drop if
// automated/unparseable/suppressed, else upsert through the Sink and tally the
// counterparty. A dropped message is counted as skipped and returns nil; only
// a real Sink write fault returns a non-nil error (which stops the pull).
func (c *Connector) capture(ctx context.Context, raw []byte, sink connector.Sink, contacts map[string]struct{}) error {
	parsed, err := parseMessage(raw, c.owner)
	if err != nil {
		// A single unparseable message is dropped, not fatal — one bad MIME
		// structure must not abort the whole pull.
		c.stats.Skipped++
		return nil
	}
	if _, drop := parsed.skipReason(); drop {
		c.stats.Skipped++
		return nil
	}
	if _, err := sink.Upsert(ctx, parsed.toRecord(raw)); err != nil {
		if errors.Is(err, connector.ErrSkip) {
			// The Sink dropped it (e.g. an erased subject's suppression list) —
			// a deliberate skip, counted like any other.
			c.stats.Skipped++
			return nil
		}
		return err
	}
	c.stats.Captured++
	if parsed.counterparty != "" {
		contacts[strings.ToLower(parsed.counterparty)] = struct{}{}
	}
	return nil
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
	if err := c.conn.Noop().Wait(); err != nil {
		return fmt.Errorf("imap: session unhealthy: %w", ErrUnreachable)
	}
	return nil
}

// Stats returns the outcome of the last Sync on this connector.
func (c *Connector) Stats() Stats { return c.stats }

// readMessageBody reads the message's BODY[] literal, bounded to maxRawLen. It
// walks the FETCH data items to the end so the streamed literal is fully
// consumed (or discarded, past the cap) before the next message — leaving it
// half-read would desync the connection.
func readMessageBody(msg *imapclient.FetchMessageData) ([]byte, error) {
	var raw []byte
	var readErr error
	found := false
	for {
		item := msg.Next()
		if item == nil {
			break
		}
		bs, ok := item.(imapclient.FetchItemDataBodySection)
		if !ok || bs.Literal == nil || found {
			continue
		}
		raw, readErr = readCapped(bs.Literal)
		found = true
	}
	if !found {
		return nil, errNoBodySection
	}
	return raw, readErr
}

// readCapped reads at most maxRawLen bytes from r; a source larger than the cap
// is rejected as too-large rather than truncated (a truncated MIME blob is not
// parseable, honest evidence). It reads one byte past the cap to distinguish
// "exactly at the cap" from "over".
func readCapped(r io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxRawLen+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxRawLen {
		return nil, errMessageTooLarge
	}
	return raw, nil
}
