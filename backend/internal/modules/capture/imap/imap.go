// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package imap is a read-only mail-capture connector: it dials a user's
// mailbox over IMAPS, pulls the most recent messages, and normalizes each
// into an email activity for the timeline. It implements
// connector.Connector, so every captured row still lands through the ONE
// capture Sink (audit + outbox in one transaction) — this package owns the
// provider I/O and the pure RFC822→activity mapping, nothing about the write.
//
// It is a STANDING connector, at parity with gmail: NewStanding builds one,
// Authenticate probes and seals the credentials to the vault, and every Sync
// dials a fresh session and advances a persisted UID cursor
// (capture_connection row + CAP-DDL-5 sidecar). See standing.go for the
// sync mechanics; this file holds the provider-agnostic pieces both share:
// Descriptor, the RFC822→activity mapping (Normalize), and the streamed-body
// reader.
//
// It uses go-imap v2, whose FETCH exposes each body as a streamed reader: we
// read it through readCapped so the per-message allocation is bounded by
// maxRawLen, not by the size the (tenant-supplied, possibly hostile) server
// declares. v1's client buffered the whole literal up front with no reachable
// size limit — an unbounded-allocation DoS — so this connector is v2-only.
package imap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/gradionhq/margince/backend/internal/modules/capture/mailmap"
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

// Connector pulls recent mail from one persisted connection. The registry
// holds one instance as the compiled-in provider (like every other
// connector); every Authenticate/Sync call is self-contained (dial, act,
// close) so nothing leaks or races between the connections it serves — see
// syncStanding's own note on why per-pull state never lives on c.
type Connector struct {
	// owner is the mailbox address Normalize maps From/To against to decide
	// inbound vs outbound. It is a direct constructor field for that pure,
	// no-I/O mapping (see Normalize) — the standing Authenticate/Sync path
	// carries the owner through the sealed Credentials bundle instead.
	owner string

	// dial establishes one session; injectable so the sync logic tests
	// against an in-memory server (the production dialer's TLS + SSRF guard
	// are its own tested properties).
	dial func(context.Context, Credentials) (*imapclient.Client, net.Conn, error)
}

// syncState is one pull's mutable state — owner identity, counters, and the
// counterparty tally. It is always a local of the sync in progress: the
// standing Connector is a registry singleton shared by every IMAP
// connection, so per-pull state on the struct would race across mailboxes.
type syncState struct {
	owner    string
	stats    Stats
	contacts map[string]struct{}
}

// Stats is one pull's outcome tally — internal bookkeeping accumulated on
// syncState as capture/captureFetched process each message. No caller reads
// it today; it exists so a future health/ops surface has something to log
// without threading new counters through the sync loop.
type Stats struct {
	Captured int // messages that landed as activities (new or idempotent replay)
	Skipped  int // messages intentionally dropped (automated/system mail, unparseable, oversized)
	Contacts int // distinct counterparties seen across the captured messages
}

var (
	_ connector.Connector      = (*Connector)(nil)
	_ connector.AccountLabeler = (*Connector)(nil)
)

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
// without echoing the provider's raw message. Wraps the shared connector
// vocabulary so the registry parks, not retries (ADR-0063).
var ErrLoginRejected = fmt.Errorf("imap: the mailbox rejected these credentials: %w", connector.ErrAuthRejected)

// ErrUnreachable marks a transport-level failure reaching the server
// (DNS, TCP, TLS, timeout). The transport maps it to a 502 without
// leaking the underlying network detail; the registry backs off and retries.
var ErrUnreachable = fmt.Errorf("imap: could not reach the mail server: %w", connector.ErrUnreachable)

func (c *Connector) Descriptor() connector.Descriptor {
	return connector.Descriptor{
		Name:     connectorName,
		Version:  "1",
		Scopes:   []principal.Scope{principal.ScopeRead},
		RiskTier: mcp.TierAutoExecute, // read-only capture
		Produces: []datasource.EntityType{datasource.EntityActivity},
	}
}

// Authenticate probes the credentials end to end and returns the sealed
// bundle the registry vaults; see authenticateStanding (standing.go).
func (c *Connector) Authenticate(ctx context.Context, req connector.AuthRequest) (connector.Auth, error) {
	return c.authenticateStanding(ctx, req)
}

// Sync dials fresh and advances the persisted UID cursor; see syncStanding
// (standing.go).
func (c *Connector) Sync(ctx context.Context, auth connector.Auth, cursor connector.Cursor, sink connector.Sink) (connector.Cursor, error) {
	return c.syncStanding(ctx, auth, cursor, sink)
}

// capture processes one message's raw bytes: parse, drop if
// automated/unparseable/suppressed, else upsert through the Sink and tally the
// counterparty. A dropped message is counted as skipped and returns nil; only
// a real Sink write fault returns a non-nil error (which stops the pull).
func (c *Connector) capture(ctx context.Context, raw []byte, sink connector.Sink, st *syncState) error {
	parsed, err := mailmap.Parse(raw, st.owner)
	if err != nil {
		// A single unparseable message is dropped, not fatal — one bad MIME
		// structure must not abort the whole pull.
		st.stats.Skipped++
		return nil
	}
	if _, drop := parsed.SkipReason(); drop {
		st.stats.Skipped++
		return nil
	}
	if _, err := sink.Upsert(ctx, parsed.ToRecord(connectorName, raw)); err != nil {
		if errors.Is(err, connector.ErrSkip) {
			// The Sink dropped it (e.g. an erased subject's suppression list) —
			// a deliberate skip, counted like any other.
			st.stats.Skipped++
			return nil
		}
		return err
	}
	st.stats.Captured++
	if cp := parsed.Counterparty(); cp != "" {
		st.contacts[strings.ToLower(cp)] = struct{}{}
	}
	return nil
}

// Normalize maps ONE raw RFC822 message to its activity record. It is pure
// (no I/O) so the mapping is the test-guarded surface; it returns an
// ErrSkip-wrapped error for mail this connector intentionally drops.
func (c *Connector) Normalize(_ context.Context, raw connector.RawRecord) ([]connector.NormalizedRecord, error) {
	parsed, err := mailmap.Parse(raw, c.owner)
	if err != nil {
		return nil, err
	}
	if reason, drop := parsed.SkipReason(); drop {
		return nil, fmt.Errorf("imap: dropping %s (%s): %w", parsed.ID(), reason, connector.ErrSkip)
	}
	return []connector.NormalizedRecord{parsed.ToRecord(connectorName, raw)}, nil
}

// HealthCheck dials and logs in with the sealed bundle to confirm the
// mailbox is still reachable with these credentials — a fresh probe, since
// the persisted connector keeps no live session between calls.
func (c *Connector) HealthCheck(ctx context.Context, auth connector.Auth) error {
	var creds Credentials
	if err := json.Unmarshal(auth, &creds); err != nil {
		return fmt.Errorf("imap: malformed auth bundle: %w", err)
	}
	client, _, err := c.dial(ctx, creds)
	if err != nil {
		return err
	}
	//craft:ignore swallowed-errors best-effort close of the health probe session — the login answered the question
	_ = client.Close()
	return nil
}

// AccountLabel reports the mailbox login this connection authenticates as.
func (c *Connector) AccountLabel(auth connector.Auth) (string, error) {
	var creds Credentials
	if err := json.Unmarshal(auth, &creds); err != nil {
		return "", fmt.Errorf("imap: malformed auth bundle: %w", err)
	}
	return creds.Email, nil
}

// readMessageBodyUID reads the message's BODY[] literal (bounded to
// maxRawLen) and captures its UID (the sync watermark). It walks the FETCH
// data items to the end so the streamed literal is fully consumed (or
// discarded, past the cap) before the next message — leaving it half-read
// would desync the connection.
func readMessageBodyUID(msg *imapclient.FetchMessageData) ([]byte, uint32, error) {
	var raw []byte
	var readErr error
	var uid uint32
	found := false
	for {
		item := msg.Next()
		if item == nil {
			break
		}
		switch it := item.(type) {
		case imapclient.FetchItemDataUID:
			uid = uint32(it.UID)
		case imapclient.FetchItemDataBodySection:
			if it.Literal == nil || found {
				continue
			}
			raw, readErr = readCapped(it.Literal)
			found = true
		}
	}
	if !found {
		return nil, uid, errNoBodySection
	}
	return raw, uid, readErr
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

// boundedWindow clamps a requested pull size onto [1, maxMessagesCap].
func boundedWindow(requested int) uint32 {
	if requested > 0 && requested <= maxMessagesCap {
		return uint32(requested)
	}
	return uint32(maxMessagesCap)
}
