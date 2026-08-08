// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Reading a tool result the way a client does.
//
// Every answer this surface returns is sealed in the BYO-RES-1 envelope, so a
// suite that asserts on what a tool ANSWERED reads through it — and the reading
// lives here once rather than in each suite, because a second spelling of "the
// payload" is a second definition of the result contract.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// sealedResult is what a client reads: the six envelope fields, and the tool's
// own answer under `data`.
type sealedResult struct {
	SchemaVersion string           `json:"schema_version"`
	TraceID       string           `json:"trace_id"`
	Freshness     *sealedFreshness `json:"freshness"`
	Trust         string           `json:"trust"`
	Evidence      []sealedEvidence `json:"evidence"`
	Warnings      []sealedWarning  `json:"warnings"`
	Data          json.RawMessage  `json:"data"`
}

type sealedFreshness struct {
	LastSyncedAt  *time.Time `json:"last_synced_at"`
	Authoritative bool       `json:"authoritative"`
}

type sealedEvidence struct {
	RecordType string   `json:"record_type"`
	RecordID   ids.UUID `json:"record_id"`
	Source     string   `json:"source,omitempty"`
	CapturedBy string   `json:"captured_by,omitempty"`
}

type sealedWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// assertEnvelopePopulated is AC-MCP-7 over one real answer. Every field is
// checked for a VALUE rather than for presence: an empty schema_version and an
// unparseable trace_id both satisfy a schema and tell a client nothing.
func assertEnvelopePopulated(t *testing.T, tool string, out json.RawMessage) {
	t.Helper()
	var sealed sealedResult
	if err := json.Unmarshal(out, &sealed); err != nil {
		t.Fatalf("%s: the result is not an envelope: %v (%s)", tool, err, out)
	}
	if sealed.SchemaVersion == "" {
		t.Errorf("%s: schema_version is empty, so a client cannot tell a changed shape from changed data", tool)
	}
	if _, err := ids.Parse(sealed.TraceID); err != nil {
		t.Errorf("%s: trace_id %q is not an id the audit log can be searched by: %v", tool, sealed.TraceID, err)
	}
	if sealed.Freshness == nil {
		t.Errorf("%s: freshness is absent, so mirror staleness cannot be read off the answer", tool)
	}
	switch sealed.Trust {
	case "t0", "t1", "t2":
	default:
		t.Errorf("%s: trust = %q, which is not a tier the threat model defines", tool, sealed.Trust)
	}
	if sealed.Evidence == nil {
		t.Errorf("%s: evidence is null rather than a list — a client cannot tell 'none' from 'not computed'", tool)
	}
	if sealed.Warnings == nil {
		t.Errorf("%s: warnings is null rather than a list, for the same reason", tool)
	}
	if len(sealed.Data) == 0 {
		t.Errorf("%s: the envelope carries no data", tool)
	}
}

// ToolPayload is a tool's own answer, out of the envelope that carries it — what
// a suite asserting on a handler's result wants.
//
// It holds the WHOLE envelope on the way past, not only the member it answers
// with. A suite reading a payload is not testing the envelope — but the tools the
// registry-wide sweep cannot reach (an outbound provider, a live calendar) are
// reached by suites that stand those seams up themselves, and this helper is how
// they read the answer. Checking here is what stops those tools from being the
// ones whose envelope nothing ever looks at.
func ToolPayload(t *testing.T, out json.RawMessage) json.RawMessage {
	t.Helper()
	assertEnvelopePopulated(t, "the call under test", out)
	var sealed sealedResult
	if err := json.Unmarshal(out, &sealed); err != nil {
		t.Fatalf("the result is not an envelope: %v (%s)", err, out)
	}
	return sealed.Data
}
