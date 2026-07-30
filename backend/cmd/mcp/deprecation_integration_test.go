// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package main

// The Phase 1 sunset of this binary (DESIGN §5.1, D5): the exact warning
// strings every boot prints, and the proof that printing them changed
// nothing — both transports still serve. A deprecation notice that broke the
// thing it deprecates would strand every operator who has not migrated yet.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose"
)

// The exact Phase 1 sunset strings (DESIGN §5.1, D5) every boot must
// print: the general "the api now serves the same governed tool
// surface" notice on both modes, and — only when --listen is set — the
// extra warning that its split origin cannot host OAuth discovery.
const (
	mcpReplacementWarning = `cmd/mcp is deprecated and will be removed: the api now serves the same governed ` +
		`tool surface at <public-base-url>/mcp, where OAuth discovery also resolves. ` +
		`Migrate: claude mcp add --transport http margince <base>/mcp --header "Authorization: Bearer mgp_…"`
	mcpListenOnlyWarning = `--listen serves /mcp on an origin that hosts neither /oauth nor the ` +
		`.well-known documents, so client discovery cannot resolve unless a proxy serves them here. ` +
		`Use the api's /mcp instead.`
)

// warnMessages decodes a JSON-lines log stream and returns the "msg"
// field of every WARN record, exactly as passed to logger.Warn — no
// quoting artifacts, so a caller can assert equality against the literal
// warning strings rather than a text-handler-escaped rendering of them.
func warnMessages(t *testing.T, output []byte) []string {
	t.Helper()
	var msgs []string
	sc := bufio.NewScanner(bytes.NewReader(output))
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec struct {
			Level string `json:"level"`
			Msg   string `json:"msg"`
		}
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("log line is not JSON (want --log-format json): %s", line)
		}
		if rec.Level == "WARN" {
			msgs = append(msgs, rec.Msg)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return msgs
}

// TestCmdMCPDeprecationWarnings pins the two strings themselves, in
// isolation from any process boot: stdio gets only the general notice,
// --listen gets both, and the messages match the literal warning text
// verbatim.
func TestCmdMCPDeprecationWarnings(t *testing.T) {
	var stdio bytes.Buffer
	logDeprecationWarnings(slog.New(slog.NewJSONHandler(&stdio, nil)), false)
	if got := warnMessages(t, stdio.Bytes()); len(got) != 1 || got[0] != mcpReplacementWarning {
		t.Fatalf("stdio mode warnings = %v, want exactly [%q]", got, mcpReplacementWarning)
	}

	var listen bytes.Buffer
	logDeprecationWarnings(slog.New(slog.NewJSONHandler(&listen, nil)), true)
	if got := warnMessages(t, listen.Bytes()); len(got) != 2 || got[0] != mcpReplacementWarning || got[1] != mcpListenOnlyWarning {
		t.Fatalf("--listen mode warnings = %v, want exactly [%q, %q]", got, mcpReplacementWarning, mcpListenOnlyWarning)
	}
}

// TestCmdMCPRunStillServesOverStdioDespiteTheDeprecationWarning proves
// deprecation is a diagnostic, not a behavior change: the SAME run()
// that now warns on stderr still completes a real initialize +
// tools/list handshake over stdio against a live workspace. A test that
// only checked for the warning would pass equally on a binary that had
// stopped serving.
func TestCmdMCPRunStillServesOverStdioDespiteTheDeprecationWarning(t *testing.T) {
	e := setupMCPEnv(t)
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	t.Setenv("MARGINCE_PASSPORT_TOKEN", e.rw.Token)

	clientIn, serverOut := io.Pipe()
	serverIn, clientOut := io.Pipe()
	var stderr bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--dsn", appDSN, "--log-format", "json"}, serverIn, serverOut, &stderr)
	}()

	sc := bufio.NewScanner(clientIn)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	assertHandshakeAndSurface(t, &mcpClient{t: t, enc: json.NewEncoder(clientOut), sc: sc})

	if err := clientOut.Close(); err != nil {
		t.Errorf("closing the client pipe: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not exit after stdin closed")
	}

	if got := warnMessages(t, stderr.Bytes()); len(got) != 1 || got[0] != mcpReplacementWarning {
		t.Fatalf("stdio run's warnings = %v, want exactly [%q]", got, mcpReplacementWarning)
	}
}

// TestCmdMCPServeHostedStillServesDespiteTheDeprecationWarning is the
// --listen counterpart. It calls logDeprecationWarnings and serveHosted
// directly — the exact two functions run() delegates to on that branch —
// rather than run() itself: composed extension registration
// (ADR-0069 EXT-P4) is guarded against a duplicate declaration, and the
// stdio test above already drives that registration once through a real
// run() call; a second `run()` in the same test binary would trip that
// guard on an extension the composed build actually declares, which is
// a boot-safety property worth keeping rather than working around.
// serveHosted and logDeprecationWarnings are exactly what --listen boots,
// so this still proves both the warnings and the live transport.
func TestCmdMCPServeHostedStillServesDespiteTheDeprecationWarning(t *testing.T) {
	e := setupMCPEnv(t)

	var stderr bytes.Buffer
	logger, err := newLogger(&stderr, "info", "json")
	if err != nil {
		t.Fatal(err)
	}
	logDeprecationWarnings(logger, true)
	registry := compose.NewRegistry(e.pool, compose.SendPath{})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serveHosted(ctx, addr, e.svc, registry, e.admin.WorkspaceID, logger) }()

	awaitListener(t, addr, done, &stderr)

	req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+e.rw.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing response body: %v", err)
		}
	}()
	var parsed struct {
		Result struct {
			//nolint:tagliatelle // protocolVersion is the MCP wire member, camelCase by the protocol
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Error != nil || parsed.Result.ProtocolVersion == "" {
		t.Fatalf("initialize over the hosted transport failed: status=%d body=%+v", resp.StatusCode, parsed)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveHosted: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveHosted did not exit after cancel")
	}

	if got := warnMessages(t, stderr.Bytes()); len(got) != 2 || got[0] != mcpReplacementWarning || got[1] != mcpListenOnlyWarning {
		t.Fatalf("--listen warnings = %v, want exactly [%q, %q]", got, mcpReplacementWarning, mcpListenOnlyWarning)
	}
}

// awaitListener blocks until addr accepts a connection. The listener comes up
// inside serveHosted's own goroutine, so this dials in a tight retry loop
// rather than sleeping — it waits on the condition (the port accepting), not
// on the clock. A serveHosted that died first is reported as that, with its
// diagnostics, instead of as a timeout that says nothing about why.
func awaitListener(t *testing.T, addr string, done <-chan error, diagnostics *bytes.Buffer) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, dialErr := net.Dial("tcp", addr)
		if dialErr == nil {
			if err := conn.Close(); err != nil {
				t.Fatal(err)
			}
			return
		}
		select {
		case hostedErr := <-done:
			t.Fatalf("serveHosted exited before the listener came up: %v (stderr: %s)", hostedErr, diagnostics.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("--listen never came up on %s: %v", addr, dialErr)
		}
	}
}
