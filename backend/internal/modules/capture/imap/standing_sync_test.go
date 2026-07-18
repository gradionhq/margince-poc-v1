// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package imap

// The standing sync against a real (in-memory) IMAP server: the anchor pull
// captures the recent window and plants the UID watermark; the next sync
// pulls only what arrived above it; a replayed sync captures nothing new.
// The in-test dialer replaces only the transport (plain pipe instead of
// TLS+SSRF-guarded dial) — everything from LOGIN onward is the production
// path.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"testing"

	imapv2 "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// recordingSink collects what the pull emitted.
type recordingSink struct {
	records []connector.NormalizedRecord
}

func (r *recordingSink) Upsert(_ context.Context, rec connector.NormalizedRecord) (datasource.EntityRef, error) {
	r.records = append(r.records, rec)
	return datasource.EntityRef{}, nil
}

const (
	memUser = "owner@ws.example"
	memPass = "pw"
)

func rfc822(n int) string {
	return fmt.Sprintf("From: Alice <alice@acme.test>\r\n"+
		"To: %s\r\n"+
		"Subject: hello %d\r\n"+
		"Date: Wed, 04 Jun 2026 08:0%d:00 +0000\r\n"+
		"Message-ID: <m%d@acme.test>\r\n"+
		"Content-Type: text/plain\r\n\r\n"+
		"body %d", memUser, n, n%10, n, n)
}

// startMemServer boots an in-memory IMAP server with n messages in INBOX and
// returns its address plus the append handle for later arrivals.
func startMemServer(t *testing.T, n int) (addr string, user *imapmemserver.User) {
	t.Helper()
	mem := imapmemserver.New()
	user = imapmemserver.NewUser(memUser, memPass)
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		appendMessage(t, user, rfc822(i))
	}
	mem.AddUser(user)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		InsecureAuth: true,
	})
	go func() {
		//craft:ignore swallowed-errors the listener closes at test end; Serve's shutdown error is the expected exit
		_ = srv.Serve(ln)
	}()
	t.Cleanup(func() {
		//craft:ignore swallowed-errors test-server shutdown; the assertions already ran
		_ = srv.Close()
	})
	return ln.Addr().String(), user
}

func appendMessage(t *testing.T, user *imapmemserver.User, raw string) {
	t.Helper()
	buf := []byte(raw)
	if _, err := user.Append("INBOX", &memLiteral{b: buf}, &imapv2.AppendOptions{}); err != nil {
		t.Fatal(err)
	}
}

// memLiteral adapts bytes onto the append literal contract.
type memLiteral struct{ b []byte }

func (l *memLiteral) Read(p []byte) (int, error) {
	if len(l.b) == 0 {
		return 0, io.EOF
	}
	n := copy(p, l.b)
	l.b = l.b[n:]
	return n, nil
}
func (l *memLiteral) Size() int64 { return int64(len(l.b)) }

// plainDialer dials the in-memory server without TLS — the transport is the
// only substitution; LOGIN onward is production code.
func plainDialer(addr string) func(context.Context, Credentials) (*imapclient.Client, net.Conn, error) {
	return func(_ context.Context, creds Credentials) (*imapclient.Client, net.Conn, error) {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			return nil, nil, err
		}
		client := imapclient.New(conn, &imapclient.Options{})
		if err := client.Login(creds.Email, creds.Password).Wait(); err != nil {
			//craft:ignore swallowed-errors best-effort close of a session whose login already failed
			_ = client.Close()
			return nil, nil, ErrLoginRejected
		}
		return client, conn, nil
	}
}

func standingAuth(t *testing.T) connector.Auth {
	t.Helper()
	creds, err := normalizeCredentials(Credentials{Host: "mem", Email: memUser, Password: memPass, MaxMessages: 3})
	if err != nil {
		t.Fatal(err)
	}
	req, err := AuthRequestFrom(creds)
	if err != nil {
		t.Fatal(err)
	}
	return connector.Auth(req.Payload)
}

func TestStandingSyncAnchorsThenIncrements(t *testing.T) {
	addr, user := startMemServer(t, 5)
	c := NewStanding().withDialer(plainDialer(addr))
	auth := standingAuth(t)

	// Anchor pull: the bounded window (3), not the whole mailbox — never a
	// full walk. The cursor plants the UID watermark + the mailbox identity.
	sink := &recordingSink{}
	cur, err := c.Sync(context.Background(), auth, nil, sink)
	if err != nil {
		t.Fatalf("anchor sync: %v", err)
	}
	if len(sink.records) != 3 {
		t.Fatalf("anchor captured %d, want the bounded window of 3", len(sink.records))
	}
	parsed, err := parseIMAPCursor(cur)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.LastUID == 0 || parsed.Email != memUser {
		t.Fatalf("cursor = %+v, want a planted watermark carrying the mailbox identity", parsed)
	}
	for _, rec := range sink.records {
		if rec.EntityType != datasource.EntityActivity || rec.NaturalKey.SourceSystem != "imap" {
			t.Fatalf("record shape wrong: %+v", rec)
		}
	}

	// Nothing new: the incremental pull is empty and the watermark holds.
	sink2 := &recordingSink{}
	cur2, err := c.Sync(context.Background(), auth, cur, sink2)
	if err != nil {
		t.Fatalf("idle sync: %v", err)
	}
	if len(sink2.records) != 0 {
		t.Fatalf("idle sync captured %d, want 0", len(sink2.records))
	}
	parsed2, err := parseIMAPCursor(cur2)
	if err != nil {
		t.Fatal(err)
	}
	if parsed2.LastUID != parsed.LastUID {
		t.Fatalf("idle sync moved the watermark %d → %d", parsed.LastUID, parsed2.LastUID)
	}

	// A new arrival: exactly it, nothing rewound.
	appendMessage(t, user, rfc822(6))
	sink3 := &recordingSink{}
	cur3, err := c.Sync(context.Background(), auth, cur2, sink3)
	if err != nil {
		t.Fatalf("incremental sync: %v", err)
	}
	if len(sink3.records) != 1 {
		t.Fatalf("incremental captured %d, want exactly the new arrival", len(sink3.records))
	}
	parsed3, err := parseIMAPCursor(cur3)
	if err != nil {
		t.Fatal(err)
	}
	if parsed3.LastUID <= parsed2.LastUID {
		t.Fatalf("watermark did not advance: %d → %d", parsed2.LastUID, parsed3.LastUID)
	}
}

func TestStandingAuthenticateProbesAndSeals(t *testing.T) {
	addr, _ := startMemServer(t, 0)
	c := NewStanding().withDialer(plainDialer(addr))

	req, err := AuthRequestFrom(Credentials{Host: "mem", Email: memUser, Password: memPass})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := c.Authenticate(context.Background(), req)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	var creds Credentials
	if err := json.Unmarshal(bundle, &creds); err != nil {
		t.Fatal(err)
	}
	if creds.Password != memPass || creds.Port != defaultPort || creds.Mailbox != defaultMailbox {
		t.Fatalf("sealed bundle not normalized: %+v", creds)
	}

	// A bad login parks as auth — the probe is honest.
	badReq, err := AuthRequestFrom(Credentials{Host: "mem", Email: memUser, Password: "wrong"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Authenticate(context.Background(), badReq); err == nil {
		t.Fatal("a rejected login must fail the probe")
	}
}
