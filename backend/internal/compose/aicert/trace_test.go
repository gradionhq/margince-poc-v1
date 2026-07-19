// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

const traceStamp = "20260101T000000Z"

// payloadCall is one ai.Call carrying a Payload, the shape the router's
// terminal attempt hands the recorder when capture is on.
func payloadCall() ai.Call {
	return ai.Call{
		Task:            ai.TaskSummarize,
		Tier:            ai.TierCheapCloud,
		Provider:        ai.ProviderFake,
		ModelID:         ai.ProviderFake,
		ServedModel:     ai.ProviderFake,
		TokensIn:        11,
		TokensOut:       7,
		ReasoningTokens: 3,
		LatencyMS:       42,
		Payload:         &ai.Payload{Request: json.RawMessage(`{"system":"s"}`), Response: json.RawMessage(`"answer"`)},
	}
}

// readTrace decodes every JSONL line of a closed trace file.
func readTrace(t *testing.T, path string) []tracedCall {
	t.Helper()
	raw, err := os.ReadFile(path) // #nosec G304 -- test-owned temp path
	if err != nil {
		t.Fatalf("read trace %s: %v", path, err)
	}
	var out []tracedCall
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var tc tracedCall
		if err := json.Unmarshal([]byte(line), &tc); err != nil {
			t.Fatalf("decode trace line %q: %v", line, err)
		}
		out = append(out, tc)
	}
	return out
}

// A cert run with a trace persists both the candidate and the judge call as
// JSONL lines carrying the post-stripper request/response — the same content
// and column names (request_payload / response_payload) the ai_call_payload
// table holds. Drives the real router pipeline over the offline fake, so it
// also exercises certifyTask's WithPayloadCapture wiring and runOnce's two
// traceCall sites.
func TestCertifyTaskWithTraceWritesCandidateAndJudgePayloads(t *testing.T) {
	dir := t.TempDir()
	trace, err := openPayloadTrace(dir, traceStamp)
	if err != nil {
		t.Fatalf("openPayloadTrace: %v", err)
	}

	candidateFake := ai.NewFakeClient().Script("the widget is blue and durable")
	judgeFake := ai.NewFakeClient().Script(scoreJSON(90))
	sc := testScenario("basic", wideBands, widgetChecks())

	if _, err := certifyTask(wsContext(t), ai.TaskSummarize, []Scenario{sc}, ai.FakeRoutingConfig(), "", 1, quietLogger(), &certifyHooks{
		candidateOpts: []ai.LocalOption{ai.WithFakeClient(candidateFake)},
		judgeOpts:     []ai.LocalOption{ai.WithFakeClient(judgeFake)},
		trace:         trace,
	}); err != nil {
		t.Fatalf("certifyTask: %v", err)
	}
	if err := trace.close(); err != nil {
		t.Fatalf("close trace: %v", err)
	}

	lines := readTrace(t, trace.Path)
	if len(lines) != 2 {
		t.Fatalf("trace lines = %d, want 2 (candidate + judge): %+v", len(lines), lines)
	}
	byRole := map[string]tracedCall{}
	for _, l := range lines {
		byRole[l.Role] = l
	}
	cand, ok := byRole["candidate"]
	if !ok {
		t.Fatalf("no candidate line in trace: %+v", lines)
	}
	if cand.Task != string(ai.TaskSummarize) || cand.Scenario != "basic" || cand.Run != 1 {
		t.Fatalf("candidate line context wrong: %+v", cand)
	}
	if cand.ServedModel != ai.ProviderFake {
		t.Fatalf("candidate served_model = %q, want %q", cand.ServedModel, ai.ProviderFake)
	}
	if !strings.Contains(string(cand.ResponsePayload), "widget") {
		t.Fatalf("candidate response_payload missing the answer: %s", cand.ResponsePayload)
	}
	// The request payload carries the scenario's system prompt + messages,
	// the same {system, messages} document ai_call_payload stores.
	if !strings.Contains(string(cand.RequestPayload), "system") {
		t.Fatalf("candidate request_payload not the {system,messages} shape: %s", cand.RequestPayload)
	}
	judge, ok := byRole["judge"]
	if !ok {
		t.Fatalf("no judge line in trace: %+v", lines)
	}
	if !strings.Contains(string(judge.ResponsePayload), "score") {
		t.Fatalf("judge response_payload missing the score: %s", judge.ResponsePayload)
	}
}

// A call whose terminal attempt built no Payload (capture off, or an
// error/cache-hit path) is skipped, not written as a hollow line.
func TestPayloadTraceSkipsCallWithoutPayload(t *testing.T) {
	trace, err := openPayloadTrace(t.TempDir(), traceStamp)
	if err != nil {
		t.Fatalf("openPayloadTrace: %v", err)
	}
	if err := trace.record("candidate", ai.TaskSummarize, testScenario("s", wideBands, nil), 1, ai.Call{}); err != nil {
		t.Fatalf("record with nil Payload should be a no-op, got: %v", err)
	}
	if err := trace.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if lines := readTrace(t, trace.Path); len(lines) != 0 {
		t.Fatalf("trace wrote %d lines for a payload-less call, want 0", len(lines))
	}
}

// A nil *payloadTrace is the tracing-off state: every method no-ops rather
// than panicking, so the runner threads one value whether tracing is on or
// off.
func TestPayloadTraceNilReceiverIsSafe(t *testing.T) {
	var trace *payloadTrace
	if err := trace.record("candidate", ai.TaskSummarize, testScenario("s", wideBands, nil), 1, payloadCall()); err != nil {
		t.Fatalf("nil.record = %v, want nil", err)
	}
	if err := trace.close(); err != nil {
		t.Fatalf("nil.close = %v, want nil", err)
	}
	// traceCall reaches the receiver only through record, so it inherits the
	// nil-safety — must not panic.
	traceCall(wsContext(t), trace, "candidate", ai.TaskSummarize, testScenario("s", wideBands, nil), 1, payloadCall(), quietLogger())
}

// traceCall is best-effort: a write failure is logged and swallowed so a
// completed, paid model call is never failed by a diagnostic side-channel.
func TestTraceCallLogsAndContinuesOnWriteFailure(t *testing.T) {
	trace, err := openPayloadTrace(t.TempDir(), traceStamp)
	if err != nil {
		t.Fatalf("openPayloadTrace: %v", err)
	}
	// Close the underlying file, then a write must fail.
	if err := trace.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := trace.record("candidate", ai.TaskSummarize, testScenario("s", wideBands, nil), 1, payloadCall()); err == nil {
		t.Fatal("record on a closed trace should error (the write-failure case under test)")
	}

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	// Must not panic and must not surface the error to the caller.
	traceCall(wsContext(t), trace, "candidate", ai.TaskSummarize, testScenario("s", wideBands, nil), 1, payloadCall(), log)
	if !strings.Contains(buf.String(), "payload trace write failed") {
		t.Fatalf("expected a warning about the failed trace write, got: %q", buf.String())
	}
}

// A directory that cannot be created is a real filesystem fault surfaced at
// open time, before any paid model call.
func TestOpenPayloadTraceFailsWhenDirIsAFile(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed blocker file: %v", err)
	}
	// MkdirAll under a path whose parent is a regular file must fail.
	if _, err := openPayloadTrace(filepath.Join(blocker, "sub"), traceStamp); err == nil {
		t.Fatal("openPayloadTrace under a file-as-dir should error")
	}
}
