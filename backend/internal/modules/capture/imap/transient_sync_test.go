// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package imap

// The transient one-shot pull against the in-memory server: the bounded
// recent window lands through the Sink, Stats reflects the pull, the live
// session answers HealthCheck, and Close is idempotent. Only the dial is
// bypassed (the session is handed to the connector the way Authenticate
// would) — Select, FETCH, and the capture discipline are production code.

import (
	"context"
	"encoding/json"
	"net"
	"testing"

	"github.com/emersion/go-imap/v2/imapclient"
)

// liveTransient wires a logged-in session onto a transient connector the
// way Authenticate leaves it.
func liveTransient(t *testing.T, addr string) *Connector {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	client := imapclient.New(conn, &imapclient.Options{})
	if err := client.Login(memUser, memPass).Wait(); err != nil {
		t.Fatal(err)
	}
	c := New()
	c.conn = client
	c.netConn = conn
	return c
}

func transientAuth(t *testing.T, maxMessages int) []byte {
	t.Helper()
	auth, err := json.Marshal(authConfig{Owner: memUser, Mailbox: "INBOX", MaxMessages: maxMessages})
	if err != nil {
		t.Fatal(err)
	}
	return auth
}

func TestTransientPullCapturesBoundedWindow(t *testing.T) {
	addr, _ := startMemServer(t, 5)
	c := liveTransient(t, addr)
	defer func() {
		if err := c.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	sink := &recordingSink{}
	cur, err := c.Sync(context.Background(), transientAuth(t, 3), nil, sink)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if cur != nil {
		t.Fatalf("a one-shot pull returns no cursor, got %q", cur)
	}
	if len(sink.records) != 3 {
		t.Fatalf("captured %d, want the bounded window of 3", len(sink.records))
	}
	stats := c.Stats()
	if stats.Captured != 3 || stats.Mailbox != "INBOX" || stats.Contacts != 1 {
		t.Fatalf("stats = %+v, want 3 captured from INBOX, 1 counterparty", stats)
	}
}

func TestTransientSessionLifecycle(t *testing.T) {
	addr, _ := startMemServer(t, 0)
	c := liveTransient(t, addr)

	if err := c.HealthCheck(context.Background(), nil); err != nil {
		t.Fatalf("a live session must answer HealthCheck: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close must be idempotent: %v", err)
	}
	if err := c.HealthCheck(context.Background(), nil); err == nil {
		t.Fatal("a closed connector must refuse HealthCheck")
	}
	if _, err := c.Sync(context.Background(), transientAuth(t, 3), nil, &recordingSink{}); err == nil {
		t.Fatal("Sync after Close must refuse — there is no session to pull from")
	}

	// An empty mailbox is the honest empty pull, not an error.
	c2 := liveTransient(t, addr)
	defer func() {
		if err := c2.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()
	sink := &recordingSink{}
	if _, err := c2.Sync(context.Background(), transientAuth(t, 3), nil, sink); err != nil {
		t.Fatalf("empty-mailbox pull: %v", err)
	}
	if len(sink.records) != 0 {
		t.Fatalf("empty mailbox captured %d records", len(sink.records))
	}
}

func TestDescriptorDeclaresReadOnlyCapture(t *testing.T) {
	d := New().Descriptor()
	if d.Name != "imap" || len(d.Scopes) != 1 {
		t.Fatalf("descriptor = %+v, want imap with exactly the read scope", d)
	}
}

func TestBoundedWindowClamps(t *testing.T) {
	cases := map[int]uint32{0: maxMessagesCap, -3: maxMessagesCap, 7: 7, maxMessagesCap + 1: maxMessagesCap}
	for in, want := range cases {
		if got := boundedWindow(in); got != want {
			t.Errorf("boundedWindow(%d) = %d, want %d", in, got, want)
		}
	}
}
