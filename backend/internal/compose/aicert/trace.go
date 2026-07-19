// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert

// The opt-in payload trace: a dev/tuning dump of every candidate and judge
// call's post-SecretStripper request and response — the SAME content the
// production router writes to ai_call_payload — to a JSONL file under a
// caller-named directory (MARGINCE_AICERT_TRACE). Off unless a directory is
// named: the lane's durable output is the verdict record, never the prompts.
// This exists so an author tuning a scenario can read exactly what the model
// saw and said without a database.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

// tracedCall is one logical call as it would read joined across ai_call and
// ai_call_payload: the identifying metadata plus the two jsonb payload
// columns under their table names (request_payload / response_payload), so a
// trace line is the same shape a `SELECT` over those two tables would give.
// role and scenario/run are the trace-only context that tells two otherwise
// identical lines apart (which router, which repeat) — the DB carries the
// same distinction across ai_call.task and the correlation/agent ids.
type tracedCall struct {
	Task            string          `json:"task"`
	Role            string          `json:"role"` // candidate | judge
	Scenario        string          `json:"scenario"`
	Run             int             `json:"run"`
	Tier            string          `json:"tier"`
	Provider        string          `json:"provider"`
	ModelID         string          `json:"model_id"`
	ServedModel     string          `json:"served_model"`
	TokensIn        int             `json:"tokens_in"`
	TokensOut       int             `json:"tokens_out"`
	ReasoningTokens int             `json:"reasoning_tokens"`
	LatencyMS       int64           `json:"latency_ms"`
	RequestPayload  json.RawMessage `json:"request_payload"`
	ResponsePayload json.RawMessage `json:"response_payload"`
}

// payloadTrace serializes tracedCall lines to one JSONL file for a whole
// run. A nil *payloadTrace is the disabled state — every method is a no-op
// on it — so the runner threads one value whether tracing is on or off.
type payloadTrace struct {
	mu   sync.Mutex
	w    io.WriteCloser
	enc  *json.Encoder
	Path string // absolute, printed to stdout when the trace opens
}

// openPayloadTrace creates the trace file under dir (named by the run's
// timestamp so repeated runs never clobber each other) and prints its
// absolute path to stdout for the tuning loop. The caller gates on an empty
// dir (tracing off ⇒ a nil *payloadTrace whose methods no-op); dir here is
// always a real directory to write into.
func openPayloadTrace(dir string, stamp string) (*payloadTrace, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("aicert: trace dir %s: %w", dir, err)
	}
	path := filepath.Join(dir, "aicert-trace-"+stamp+".jsonl")
	f, err := os.Create(path) // #nosec G304 -- path is the operator-named trace dir (MARGINCE_AICERT_TRACE) + a fixed filename; a dev/CI lane, no request input
	if err != nil {
		return nil, fmt.Errorf("aicert: create trace %s: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		// A resolvable path failing to absolutize is a real filesystem
		// fault, not a caller input — fail the run rather than print a
		// path the tuning loop cannot trust.
		//craft:ignore swallowed-errors close-then-report on the error path
		_ = f.Close()
		return nil, fmt.Errorf("aicert: absolute trace path for %s: %w", path, err)
	}
	if _, err := fmt.Fprintf(os.Stdout, "aicert: payload trace → %s\n", abs); err != nil {
		//craft:ignore swallowed-errors close-then-report on the error path
		_ = f.Close()
		return nil, fmt.Errorf("aicert: announce trace path %s: %w", abs, err)
	}
	return &payloadTrace{w: f, enc: json.NewEncoder(f), Path: abs}, nil
}

// record appends one call's payloads to the trace. A call whose terminal
// attempt carries no Payload (capture off, or an error/cache-hit path that
// captures nothing) is skipped rather than written as a hollow line — the
// trace shows only calls it can actually explain.
func (t *payloadTrace) record(role string, task ai.Task, sc Scenario, run int, c ai.Call) error {
	if t == nil || c.Payload == nil {
		return nil
	}
	line := tracedCall{
		Task:            string(task),
		Role:            role,
		Scenario:        sc.Name,
		Run:             run,
		Tier:            string(c.Tier),
		Provider:        c.Provider,
		ModelID:         c.ModelID,
		ServedModel:     c.ServedModel,
		TokensIn:        c.TokensIn,
		TokensOut:       c.TokensOut,
		ReasoningTokens: c.ReasoningTokens,
		LatencyMS:       c.LatencyMS,
		RequestPayload:  c.Payload.Request,
		ResponsePayload: c.Payload.Response,
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.enc.Encode(line); err != nil {
		return fmt.Errorf("aicert: write trace line (%s %s): %w", task, role, err)
	}
	return nil
}

// traceCall writes one call to the trace best-effort: a write failure is
// logged and swallowed here, never returned to the caller. The trace is an
// opt-in diagnostic side-channel; it must not become a new way for a
// completed, paid model call to fail — the same posture the production
// router already holds for this exact post-stripper content (ai/tracing.go:
// "payload capture must not become a new way for a working model call to
// fail"). The error is heard (logged with what and where), not ignored.
func traceCall(ctx context.Context, t *payloadTrace, role string, task ai.Task, sc Scenario, run int, c ai.Call, log *slog.Logger) {
	if err := t.record(role, task, sc, run, c); err != nil {
		log.WarnContext(ctx, "aicert: payload trace write failed — run continues",
			"task", string(task), "scenario", sc.Name, "role", role, "run", run, "err", err)
	}
}

// close flushes and closes the underlying file. Nil-safe so the runner can
// defer it unconditionally.
func (t *payloadTrace) close() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.w.Close()
}
