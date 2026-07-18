// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The standing-connection flavor: IMAP as a persisted capture_connection at
// parity with gmail. IMAP has no refresh token, so the sealed auth bundle IS
// the credential set — the vault is its custodian (the row never carries it),
// and every sync dials a fresh TLS session from the bundle and closes it.
// Incremental sync rides a UID cursor: UIDVALIDITY names the mailbox
// generation (a change invalidates every UID — re-anchor with the bounded
// window, exactly gmail's cursor-gone discipline), and last_uid is the
// watermark within it.

package imap

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	imapv2 "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/gradionhq/margince/backend/internal/platform/netguard"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// NewStanding returns the persisted-connection flavor: Authenticate probes
// and seals the credentials; every Sync dials fresh and advances a UID
// cursor. Register this one on the capture registry; the transient one-shot
// pull keeps using New().
func NewStanding() *Connector { return &Connector{standing: true, dial: dialLogin} }

// withDialer overrides the session dialer — the testable seam (the real
// dialer's TLS and SSRF properties are its own concern; the sync logic is
// this package's).
func (c *Connector) withDialer(d func(context.Context, Credentials) (*imapclient.Client, net.Conn, error)) *Connector {
	c.dial = d
	return c
}

// imapCursor is the persisted watermark. Email carries the mailbox identity
// the same way gmail's cursor does (the provider-owned routing key).
type imapCursor struct {
	UIDValidity uint32 `json:"uidvalidity"`
	LastUID     uint32 `json:"last_uid"`
	Email       string `json:"email"`
}

func parseIMAPCursor(cur connector.Cursor) (imapCursor, error) {
	if len(cur) == 0 {
		return imapCursor{}, nil
	}
	var c imapCursor
	if err := json.Unmarshal(cur, &c); err != nil {
		// A non-empty cursor we cannot read is corruption, not a fresh
		// mailbox: stop rather than silently re-anchor (gmail's discipline).
		return imapCursor{}, fmt.Errorf("imap: unreadable sync cursor: %w", err)
	}
	return c, nil
}

func marshalIMAPCursor(c imapCursor) connector.Cursor {
	b, _ := json.Marshal(c) //nolint:errchkjson // fixed-shape struct never errors
	return b
}

// normalizeCredentials applies the defaults and refuses the unusable.
func normalizeCredentials(creds Credentials) (Credentials, error) {
	creds.Host = strings.TrimSpace(creds.Host)
	creds.Email = strings.TrimSpace(creds.Email)
	if creds.Host == "" || creds.Email == "" || creds.Password == "" {
		return Credentials{}, fmt.Errorf("imap: host, email and password are all required: %w", ErrLoginRejected)
	}
	if creds.Port == 0 {
		creds.Port = defaultPort
	}
	if strings.TrimSpace(creds.Mailbox) == "" {
		creds.Mailbox = defaultMailbox
	}
	switch {
	case creds.MaxMessages <= 0:
		creds.MaxMessages = defaultMaxMessages
	case creds.MaxMessages > maxMessagesCap:
		creds.MaxMessages = maxMessagesCap
	}
	return creds, nil
}

// dialLogin establishes one TLS+login session. The caller owns the returned
// client and MUST close it on every path.
func dialLogin(ctx context.Context, creds Credentials) (*imapclient.Client, net.Conn, error) {
	addr := net.JoinHostPort(creds.Host, strconv.Itoa(creds.Port))
	tlsDialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: dialTimeout, Control: netguard.RefusePrivate},
		Config:    &tls.Config{ServerName: creds.Host, MinVersion: tls.VersionTLS12},
	}
	tlsConn, err := tlsDialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("imap: dial %s: %w", addr, ErrUnreachable)
	}
	//craft:ignore swallowed-errors SetDeadline only errors on a closed conn; we just dialed it, and a failure surfaces as the next read timing out
	_ = tlsConn.SetDeadline(time.Now().Add(pullDeadline))
	client := imapclient.New(tlsConn, &imapclient.Options{})
	if err := client.Login(creds.Email, creds.Password).Wait(); err != nil {
		//craft:ignore swallowed-errors best-effort close of a session whose login already failed — the rejection is the error to report
		_ = client.Close()
		return nil, nil, ErrLoginRejected
	}
	return client, tlsConn, nil
}

// authenticateStanding probes the credentials end to end (dial, login,
// close) and returns the normalized bundle as the sealed Auth — the vault
// stores it; this method keeps no session.
func (c *Connector) authenticateStanding(ctx context.Context, req connector.AuthRequest) (connector.Auth, error) {
	var creds Credentials
	if err := json.Unmarshal(req.Payload, &creds); err != nil {
		return nil, fmt.Errorf("imap: malformed credentials payload: %w", err)
	}
	creds, err := normalizeCredentials(creds)
	if err != nil {
		return nil, err
	}
	client, _, err := c.dial(ctx, creds)
	if err != nil {
		return nil, err
	}
	//craft:ignore swallowed-errors best-effort logout of the probe session — the login already proved the credentials
	_ = client.Logout().Wait()
	//craft:ignore swallowed-errors best-effort close of the probe session on the success path — nothing left to read from it
	_ = client.Close()
	bundle, err := json.Marshal(creds) //nolint:gosec // the sealed credential bundle — the registry vaults it, the row never sees it
	if err != nil {
		return nil, fmt.Errorf("imap: encoding credential bundle: %w", err)
	}
	return bundle, nil
}

// syncStanding is one incremental pull: fresh session, UID watermark.
func (c *Connector) syncStanding(ctx context.Context, auth connector.Auth, cursor connector.Cursor, sink connector.Sink) (connector.Cursor, error) {
	var creds Credentials
	if err := json.Unmarshal(auth, &creds); err != nil {
		return nil, fmt.Errorf("imap: malformed auth bundle: %w", err)
	}
	creds, err := normalizeCredentials(creds)
	if err != nil {
		return nil, err
	}
	cur, err := parseIMAPCursor(cursor)
	if err != nil {
		return nil, err
	}

	client, netConn, err := c.dial(ctx, creds)
	if err != nil {
		return nil, err
	}
	defer func() {
		//craft:ignore swallowed-errors best-effort logout+close at the end of a completed pull; the sync result is already decided
		_ = client.Logout().Wait()
		//craft:ignore swallowed-errors best-effort close — see above
		_ = client.Close()
	}()
	c.owner = creds.Email
	c.stats = Stats{Mailbox: creds.Mailbox}
	//craft:ignore swallowed-errors refreshing the read deadline for the pull; a closed conn surfaces as the next read failing
	_ = netConn.SetDeadline(time.Now().Add(pullDeadline))

	selData, err := client.Select(creds.Mailbox, &imapv2.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return nil, fmt.Errorf("imap: selecting mailbox %q: %w", creds.Mailbox, ErrUnreachable)
	}

	// A generation change (or a first sync) re-anchors with the bounded
	// recent window — never a full mailbox walk (the steady-state rule).
	contactsAnchor := map[string]struct{}{}
	if cur.UIDValidity != selData.UIDValidity || cur.LastUID == 0 {
		c.syncContacts = contactsAnchor
		defer func() { c.stats.Contacts = len(contactsAnchor) }()
		maxUID, err := c.pullWindow(ctx, client, selData, creds.MaxMessages, sink)
		if err != nil {
			return nil, err
		}
		return marshalIMAPCursor(imapCursor{
			UIDValidity: selData.UIDValidity, LastUID: maxUID, Email: creds.Email,
		}), nil
	}

	contacts := map[string]struct{}{}
	defer func() { c.stats.Contacts = len(contacts) }()
	c.syncContacts = contacts

	// Incremental: everything above the watermark, capped per pull — a
	// burst beyond the cap simply continues next sync from the new
	// watermark (the capture key makes any overlap a no-op).
	var uids imapv2.UIDSet
	uids.AddRange(imapv2.UID(cur.LastUID+1), 0) // 0 = *
	maxUID, err := c.pullSet(ctx, client, uids, maxMessagesCap, sink)
	if err != nil {
		return nil, err
	}
	if maxUID > cur.LastUID {
		cur.LastUID = maxUID
	}
	cur.UIDValidity = selData.UIDValidity
	cur.Email = creds.Email
	return marshalIMAPCursor(cur), nil
}

// pullWindow captures the most recent window by sequence numbers (the
// anchor pull), returning the highest UID seen; an empty mailbox anchors at
// UIDNext-1 so the next sync starts incremental.
func (c *Connector) pullWindow(ctx context.Context, client *imapclient.Client, selData *imapv2.SelectData, window int, sink connector.Sink) (uint32, error) {
	if selData.NumMessages == 0 {
		if selData.UIDNext > 1 {
			return uint32(selData.UIDNext) - 1, nil
		}
		return 0, nil
	}
	w := boundedWindow(window)
	from := uint32(1)
	if selData.NumMessages > w {
		from = selData.NumMessages - w + 1
	}
	seqset := imapv2.SeqSet{}
	seqset.AddRange(from, selData.NumMessages)
	return c.pullSet(ctx, client, seqset, int(w), sink)
}

// pullSet fetches one message set (sequence or UID addressed), captures
// each through the Sink with the same skip/size discipline as the
// transient pull, and returns the highest UID seen.
func (c *Connector) pullSet(ctx context.Context, client *imapclient.Client, set imapv2.NumSet, capMessages int, sink connector.Sink) (uint32, error) {
	fetchOpts := &imapv2.FetchOptions{
		UID:         true,
		BodySection: []*imapv2.FetchItemBodySection{{}},
	}
	fetchCmd := client.Fetch(set, fetchOpts)
	var maxUID uint32
	seen := 0
	var writeErr error
	for {
		msg := fetchCmd.Next()
		if msg == nil {
			break
		}
		if writeErr != nil || (capMessages > 0 && seen >= capMessages) {
			// Drain without processing: the command must be consumed, but a
			// write fault or the per-pull cap already decided this pull.
			continue
		}
		uid, err := c.captureFetched(ctx, msg, sink)
		if err != nil {
			writeErr = err
			continue
		}
		seen++
		if uid > maxUID {
			maxUID = uid
		}
	}
	if err := fetchCmd.Close(); err != nil && writeErr == nil {
		return maxUID, fmt.Errorf("imap: fetch: %w", errors.Join(ErrUnreachable, err))
	}
	return maxUID, writeErr
}

// captureFetched processes one fetched message: read (bounded), parse,
// drop-or-upsert — the same discipline as the transient pull — returning
// the message UID for the watermark.
func (c *Connector) captureFetched(ctx context.Context, msg *imapclient.FetchMessageData, sink connector.Sink) (uint32, error) {
	raw, uid, err := readMessageBodyUID(msg)
	if err != nil {
		// Oversized or bodyless is a per-message drop, never a
		// pull-stopping fault — the same discipline as the transient pull.
		c.stats.Skipped++
		return uid, nil //nolint:nilerr // deliberate: the drop is counted, the pull continues
	}
	if err := c.capture(ctx, raw, sink, c.syncContacts); err != nil {
		return uid, err
	}
	return uid, nil
}
