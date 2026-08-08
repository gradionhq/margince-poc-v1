// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Both framings, end to end on the real origin, against a real database
// (ADR-0092/A141). The unit suite proves each framing's rules; only this one
// proves that the route compose actually mounts — behind its Origin guard, its
// rate limits and its per-request authenticate closure — serves them BOTH, and
// that a call in either framing lands the same effect in the same workspace.
//
// The handshake half is not decoration. C2 removes the session registry for
// modern clients, and the legacy framing is where that change can break every
// connected client; a suite that exercised only the modern path would not
// notice (ADR-0092, Consequences).

import (
	"encoding/json"
	"maps"
	"net/http"
	"strings"
	"testing"
)

// The per-request metadata a modern client sends, spelled as the specification
// writes it — this suite is a client, so it must not read the server's own
// constants for what to put on the wire.
const (
	modernRevision = "2026-07-28"
	modernMeta     = `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28",` +
		`"io.modelcontextprotocol/clientCapabilities":{}}`
)

// modernHeaders is what a conforming modern POST carries: the credential, plus
// the body members this transport mirrors so an intermediary can route without
// parsing the body.
func modernHeaders(bearer map[string]string, method, name string) map[string]string {
	headers := map[string]string{
		"Content-Type":         "application/json",
		"MCP-Protocol-Version": modernRevision,
		"Mcp-Method":           method,
	}
	if name != "" {
		headers["Mcp-Name"] = name
	}
	maps.Copy(headers, bearer)
	return headers
}

// TestBothFramingsConnectAndLandTheSameEffect is the headline claim: one
// server, two eras, and a record created through either one.
func TestBothFramingsConnectAndLandTheSameEffect(t *testing.T) {
	e := setupConnector(t)
	bearer := passportBearer(t, e.AppEnv, "dual-era client", "read", "write")

	t.Run("a modern client needs no handshake", func(t *testing.T) {
		discovered := mcpRaw(e.AppEnv, t, http.MethodPost, "/mcp",
			`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{`+modernMeta+`}}`,
			modernHeaders(bearer, "server/discover", ""))
		if discovered.StatusCode != http.StatusOK {
			t.Fatalf("server/discover → %d %s", discovered.StatusCode, discovered.Body)
		}
		// Discovery is what a client reads INSTEAD of probing, so it must name
		// the revision this client is already speaking.
		result := rpcResult(t, discovered.Body)
		versions, ok := result["supportedVersions"].([]any)
		if !ok || len(versions) == 0 || versions[0] != modernRevision {
			t.Fatalf("supportedVersions = %v, want the modern revision first", result["supportedVersions"])
		}
		if result["resultType"] != "complete" {
			t.Errorf("resultType = %v, want complete", result["resultType"])
		}

		// The tool list is filtered by this passport's scopes, so the copy it
		// hands back may never be shared with another caller.
		listed := mcpRaw(e.AppEnv, t, http.MethodPost, "/mcp",
			`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{`+modernMeta+`}}`,
			modernHeaders(bearer, "tools/list", ""))
		if listed.StatusCode != http.StatusOK {
			t.Fatalf("tools/list → %d %s", listed.StatusCode, listed.Body)
		}
		if scope := rpcResult(t, listed.Body)["cacheScope"]; scope != "private" {
			t.Errorf("tools/list cacheScope = %v, want private — a shared entry on a "+
				"scope-filtered catalog is a disclosure that never reaches the server to be audited", scope)
		}

		// And the call itself, with no session ever minted: this is what lets a
		// modern conversation land on any replica.
		called := mcpRaw(e.AppEnv, t, http.MethodPost, "/mcp",
			`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{`+modernMeta+
				`,"name":"create_record","arguments":{"record_type":"person",`+
				`"fields":{"full_name":"Modern Framing Person"}}}}`,
			modernHeaders(bearer, "tools/call", "create_record"))
		if called.StatusCode != http.StatusOK {
			t.Fatalf("tools/call → %d %s", called.StatusCode, called.Body)
		}
		if got := called.Header.Get("Mcp-Session-Id"); got != "" {
			t.Errorf("a modern exchange minted the session id %q", got)
		}
		if text := toolText(t, rpcResult(t, called.Body)); !strings.Contains(text, "Modern Framing Person") {
			t.Fatalf("tools/call answered %q, which does not carry the record it created", text)
		}
	})

	t.Run("a handshake client still opens a session", func(t *testing.T) {
		initialized := mcpRaw(e.AppEnv, t, http.MethodPost, "/mcp",
			`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`,
			withContentType(bearer))
		if initialized.StatusCode != http.StatusOK {
			t.Fatalf("initialize → %d %s", initialized.StatusCode, initialized.Body)
		}
		session := initialized.Header.Get("Mcp-Session-Id")
		if session == "" {
			t.Fatal("initialize minted no session, so the handshake era lost the state it depends on")
		}
		negotiated, _ := rpcResult(t, initialized.Body)["protocolVersion"].(string)
		if negotiated != "2025-11-25" {
			t.Fatalf("negotiated %q, want the revision this client asked for", negotiated)
		}

		called := mcpRaw(e.AppEnv, t, http.MethodPost, "/mcp",
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"create_record",`+
				`"arguments":{"record_type":"person","fields":{"full_name":"Handshake Era Person"}}}}`,
			legacyHeaders(bearer, negotiated, session))
		if called.StatusCode != http.StatusOK {
			t.Fatalf("tools/call → %d %s", called.StatusCode, called.Body)
		}
		if text := toolText(t, rpcResult(t, called.Body)); !strings.Contains(text, "Handshake Era Person") {
			t.Fatalf("tools/call answered %q, which does not carry the record it created", text)
		}
	})

	t.Run("a revision outside the window connects in neither", func(t *testing.T) {
		// The deny arm belongs beside the allow arms: the same origin, the same
		// credential, and the one revision the window dropped (ADR-0092 §3).
		headers := withContentType(bearer)
		headers["MCP-Protocol-Version"] = "2025-03-26"
		refused := mcpRaw(e.AppEnv, t, http.MethodPost, "/mcp",
			`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, headers)

		if refused.StatusCode != http.StatusBadRequest {
			t.Fatalf("the dropped revision → %d %s, want 400", refused.StatusCode, refused.Body)
		}
		if code := rpcErrorCode(t, refused.Body); code != -32022 {
			t.Fatalf("error code = %d, want -32022 UnsupportedProtocolVersion", code)
		}
		for _, served := range []string{modernRevision, "2025-11-25", "2025-06-18"} {
			if !strings.Contains(refused.Body, served) {
				t.Errorf("the refusal %s does not name %q, so the client cannot retry on it", refused.Body, served)
			}
		}
	})

	// Both eras ended in a real effect or the assertions above were satisfied
	// by a surface answering from nothing.
	for _, created := range []string{"Modern Framing Person", "Handshake Era Person"} {
		var found int
		if err := e.Owner.QueryRow(t.Context(),
			`SELECT count(*) FROM person WHERE full_name = $1`, created).Scan(&found); err != nil {
			t.Fatal(err)
		}
		if found != 1 {
			t.Errorf("%q exists %d times, want exactly one — the call answered without writing", created, found)
		}
	}
}

// withContentType is the handshake era's opening request: a credential and
// nothing else, because a client that has not negotiated has no revision to
// declare.
func withContentType(bearer map[string]string) map[string]string {
	headers := map[string]string{"Content-Type": "application/json"}
	maps.Copy(headers, bearer)
	return headers
}

// legacyHeaders is what a connected handshake client carries on every request
// after initialize.
func legacyHeaders(bearer map[string]string, negotiated, session string) map[string]string {
	headers := withContentType(bearer)
	headers["MCP-Protocol-Version"] = negotiated
	headers["Mcp-Session-Id"] = session
	return headers
}

// A gateway routes on the header and this server executes the body. When the
// two disagree the request is refused at the edge — before the tool the header
// named, and before the tool the body named, runs at all.
//
// The status and the body are one answer: a dual-era client reads a 400 and
// looks for a recognized modern error inside it, so a bare 400 would send a
// working client back to the handshake.
func TestAModernRequestWhoseHeaderContradictsItsBodyRunsNothing(t *testing.T) {
	e := setupConnector(t)
	bearer := passportBearer(t, e.AppEnv, "mismatching client", "read", "write")

	refused := mcpRaw(e.AppEnv, t, http.MethodPost, "/mcp",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{`+modernMeta+
			`,"name":"create_record","arguments":{"record_type":"person",`+
			`"fields":{"full_name":"Never Created"}}}}`,
		// The header names a read, the body calls a write.
		modernHeaders(bearer, "tools/call", "read_record"))

	if refused.StatusCode != http.StatusBadRequest {
		t.Fatalf("mismatched header → %d %s, want 400", refused.StatusCode, refused.Body)
	}
	if code := rpcErrorCode(t, refused.Body); code != -32020 {
		t.Errorf("error code = %d, want -32020 HeaderMismatch", code)
	}
	var created int
	if err := e.Owner.QueryRow(t.Context(),
		`SELECT count(*) FROM person WHERE full_name = $1`, "Never Created").Scan(&created); err != nil {
		t.Fatal(err)
	}
	if created != 0 {
		t.Errorf("the refused call created %d record(s) — it was refused after it ran", created)
	}
}

// rpcErrorCode reads the JSON-RPC error code off a refusal. It is the mirror
// of rpcResult, which fails ON an error member — here the error IS the answer
// under test.
func rpcErrorCode(t *testing.T, body string) int {
	t.Helper()
	var envelope struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("refusal does not decode as JSON-RPC: %v (%s)", err, body)
	}
	if envelope.Error == nil {
		t.Fatalf("refusal carries no JSON-RPC error member, so a dual-era client reads it as a legacy server: %s", body)
	}
	return envelope.Error.Code
}
