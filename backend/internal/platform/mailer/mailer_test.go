// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package mailer

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync"
	"testing"
)

// fakeRelay is a minimal in-process SMTP server on the loopback — the one
// posture where the mailer permits a cleartext exchange, so the protocol
// walk is testable without TLS plumbing. It records the DATA payload.
type fakeRelay struct {
	ln         net.Listener
	rejectRcpt bool

	mu   sync.Mutex
	data string
}

func startFakeRelay(t *testing.T, rejectRcpt bool) *fakeRelay {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &fakeRelay{ln: ln, rejectRcpt: rejectRcpt}
	go r.serveOne()
	t.Cleanup(func() {
		//craft:ignore swallowed-errors test-fixture teardown; the assertions ran already
		_ = ln.Close()
	})
	return r
}

func (r *fakeRelay) addrParts(t *testing.T) (host string, port int) {
	t.Helper()
	tcp, ok := r.ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener addr %T is not TCP", r.ln.Addr())
	}
	return tcp.IP.String(), tcp.Port
}

func (r *fakeRelay) serveOne() {
	conn, err := r.ln.Accept()
	if err != nil {
		return
	}
	//craft:ignore swallowed-errors test-fixture teardown; the client side asserts the outcome
	defer func() { _ = conn.Close() }()
	w := bufio.NewWriter(conn)
	sc := bufio.NewScanner(conn)
	say := func(line string) {
		//craft:ignore swallowed-errors a broken test pipe surfaces as the client's protocol error
		_, _ = w.WriteString(line + "\r\n")
		//craft:ignore swallowed-errors same pipe
		_ = w.Flush()
	}
	say("220 fake ESMTP")
	inData := false
	var data strings.Builder
	for sc.Scan() {
		line := sc.Text()
		if inData {
			if line == "." {
				inData = false
				r.mu.Lock()
				r.data = data.String()
				r.mu.Unlock()
				say("250 queued")
				continue
			}
			data.WriteString(line + "\n")
			continue
		}
		switch {
		case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
			say("250-fake")
			say("250 8BITMIME") // deliberately NO STARTTLS: loopback path
		case strings.HasPrefix(line, "MAIL FROM"):
			say("250 ok")
		case strings.HasPrefix(line, "RCPT TO"):
			if r.rejectRcpt {
				say("550 no such user")
				continue
			}
			say("250 ok")
		case line == "DATA":
			inData = true
			say("354 go ahead")
		case line == "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

func (r *fakeRelay) received() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.data
}

func TestSendWalksTheFullExchangeOnALoopbackRelay(t *testing.T) {
	relay := startFakeRelay(t, false)
	host, port := relay.addrParts(t)
	s := SMTP{Host: host, Port: port, FromAddress: "crm@example.test"}

	err := s.Send(context.Background(), "ada@example.test", "Reset your password", "the link body")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	got := relay.received()
	if !strings.Contains(got, "To: ada@example.test") ||
		!strings.Contains(got, "Subject: Reset your password") ||
		!strings.Contains(got, "the link body") {
		t.Fatalf("relay received %q, want headers + body", got)
	}
}

func TestSendRefusesHeaderInjection(t *testing.T) {
	s := SMTP{Host: "127.0.0.1", Port: 25, FromAddress: "crm@example.test"}
	if err := s.Send(context.Background(), "a@b.test\r\nBcc: evil@x.test", "s", "b"); err == nil {
		t.Fatal("a CRLF recipient was accepted (header injection)")
	}
	if err := s.Send(context.Background(), "a@b.test", "s\r\nX-Evil: 1", "b"); err == nil {
		t.Fatal("a CRLF subject was accepted (header injection)")
	}
}

func TestSendSurfacesARefusedRecipient(t *testing.T) {
	relay := startFakeRelay(t, true)
	host, port := relay.addrParts(t)
	s := SMTP{Host: host, Port: port, FromAddress: "crm@example.test"}

	err := s.Send(context.Background(), "nobody@example.test", "s", "b")
	if err == nil || !strings.Contains(err.Error(), "refused the recipient") {
		t.Fatalf("err = %v, want the refused-recipient wrap", err)
	}
}

func TestSendRefusesAnUnreachableRelay(t *testing.T) {
	// A closed port answers immediately — no timeout flakiness.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tcp, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener addr %T is not TCP", ln.Addr())
	}
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	s := SMTP{Host: "127.0.0.1", Port: tcp.Port, FromAddress: "crm@example.test"}
	if err := s.Send(context.Background(), "a@b.test", "s", "b"); err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("err = %v, want the unreachable wrap", err)
	}
}

func TestIsLoopbackRecognizesLocalRelaysOnly(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost":        true,
		"127.0.0.1":        true,
		"::1":              true,
		"smtp.example.com": false,
		"10.0.0.5":         false,
	} {
		if got := isLoopback(host); got != want {
			t.Errorf("isLoopback(%q) = %v, want %v", host, got, want)
		}
	}
}
