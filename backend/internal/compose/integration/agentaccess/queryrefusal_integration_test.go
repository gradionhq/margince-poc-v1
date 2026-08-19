// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package agentaccess

// What a refused query plan TELLS the caller, over the composed /mcp mount and
// through the real refusal type. The unit suites prove the validator authors
// the sentence and the renderer carries it; this proves the two meet — that a
// module's per-field remedy survives the trip to an agent, which is the half
// that was missing while both halves were separately correct.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/search"
)

// A plan with no `version` is refused for its version, and the answer names the
// version this server takes. It is the reported defect, kept as a spec: the
// caller was told WHICH member was wrong and never what to put in it, so the
// only way forward was to guess or to give up.
func TestARefusedPlanTellsTheAgentWhatToPutInTheMemberItRefused(t *testing.T) {
	env := setupConnector(t)
	bearer := readPassport(t, env.AppEnv, "query plan caller")

	refused := refusedToolText(t, env, bearer,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"query_workspace",`+
			`"arguments":{"plan":{"target":"person","where":[]}}}}`)

	if !strings.Contains(refused, search.CodeUnknownPlanVersion) {
		t.Errorf("refusal = %q, want the machine code an agent branches on", refused)
	}
	// The version itself, quoted from the validator rather than spelled again
	// here — a test carrying its own copy would pass over a server that had
	// stopped saying it.
	if !strings.Contains(refused, search.PlanVersion) {
		t.Errorf("refusal = %q, want it to name the plan version this server validates (%q)", refused, search.PlanVersion)
	}
	// And the summary the refusal writes for its own logs stays out of it: it
	// is a package prefix and a re-spelling of the field=code pair beside it.
	if strings.Contains(refused, "search: query plan refused") {
		t.Errorf("refusal = %q, want the log summary kept out of the agent's answer", refused)
	}
}

// The plural form is the one that was losing everything, so it is asserted as a
// plural: a plan wrong in two predicates comes back with a remedy for each,
// which is what makes one round trip enough to fix it.
func TestARefusedPlanCarriesARemedyForEveryBadPredicate(t *testing.T) {
	env := setupConnector(t)
	bearer := readPassport(t, env.AppEnv, "multi-fault query caller")

	refused := refusedToolText(t, env, bearer,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"query_workspace",`+
			`"arguments":{"plan":{"version":"`+search.PlanVersion+`","target":"person","where":[`+
			`{"field":"invented_field","op":"eq","value":"x"},`+
			`{"field":"full_name","op":"invented_op","value":"x"}]}}}}`)

	for _, want := range []string{search.CodeUnknownField, search.CodeUnknownOperator} {
		if !strings.Contains(refused, want) {
			t.Errorf("refusal = %q, want it to name %q — one round trip has to be enough to fix both", refused, want)
		}
	}
	// Two entries, and each one says something beyond its own code: the codes
	// alone would have the caller re-reading the grammar to learn which of the
	// two names it invented.
	for _, want := range []string{"invented_field", "invented_op"} {
		if !strings.Contains(refused, want) {
			t.Errorf("refusal = %q, want it to quote the token it refused (%q)", refused, want)
		}
	}
}

// The tools/call envelope as a CLIENT decodes it — declared here rather than
// borrowed from the SDK, for the reason vocabularyDoc in this package is: a
// renamed wire member then fails this test instead of travelling through it
// unnoticed. It is also why this reads the body itself rather than through
// rpcResult, whose open map cannot say which member went missing.
type toolCallEnvelope struct {
	Result struct {
		//nolint:tagliatelle // isError is the MCP wire member, camelCase by the protocol
		IsError bool `json:"isError"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// refusedToolText reads the in-band refusal a tools/call answers with. It is
// the mirror of toolText, which fatals on isError: a refusal is a 200 whose
// failure travels in the result, so a test that wants the refusal has to say
// it wanted one — and fail if the call actually ran.
func refusedToolText(t *testing.T, env *connectorEnv, bearer, payload string) string {
	t.Helper()
	called := mcpRaw(env.AppEnv, t, http.MethodPost, "/mcp", payload,
		map[string]string{"Content-Type": "application/json", "Authorization": "Bearer " + bearer})
	if called.StatusCode != http.StatusOK {
		t.Fatalf("tools/call → %d %s", called.StatusCode, called.Body)
	}

	var envelope toolCallEnvelope
	if err := json.Unmarshal([]byte(called.Body), &envelope); err != nil {
		t.Fatalf("tools/call response does not decode: %v (%s)", err, called.Body)
	}
	// A protocol-level error is not the refusal under test: this suite asserts
	// on what a tool ANSWERS, and a transport that refused the call never
	// reached the renderer these tests are about.
	if envelope.Error != nil {
		t.Fatalf("JSON-RPC error %d: %s", envelope.Error.Code, envelope.Error.Message)
	}
	if !envelope.Result.IsError {
		t.Fatalf("tools/call was answered rather than refused: %s", called.Body)
	}
	if len(envelope.Result.Content) == 0 || envelope.Result.Content[0].Text == "" {
		t.Fatalf("a refused tools/call carries no text to read: %s", called.Body)
	}
	return envelope.Result.Content[0].Text
}
