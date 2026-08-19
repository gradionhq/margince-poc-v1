// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The role's logger is the PROCESS DEFAULT, not only the value passed around.
//
// This is the worker, so every job in the installation fails here, and a job's
// failure reaches an operator through jobs.Fault — which logs through the
// package-level slog functions and therefore through slog.Default() and nothing
// else. A role that built a correlation-aware logger and left the default bare
// sent exactly those lines to the stdlib handler: text, on stderr, while every
// other line went to the operator's configured sink in the configured format. A
// collector parsing this role's JSON got an unstructured line for the events it
// most wants, and nothing anywhere said so.
//
// It is asserted from THIS role's own boot path rather than from a hand-built
// logger, because what is under test is the wiring: a test that installed its
// own default would prove slog works and nothing about whether the worker calls
// it.

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// TestAJobFaultReachesThisRolesConfiguredSinkAndFormat.
//
// The one line a postponed or unclassified tick leaves in the process has to be
// findable by the tooling that reads this role's output. So the assertion is
// that it PARSES as JSON when the operator asked for JSON — a text line in a
// JSON stream is the failure this test exists to catch, and it is invisible to
// any assertion that only looks for a substring.
func TestAJobFaultReachesThisRolesConfiguredSinkAndFormat(t *testing.T) {
	var logged bytes.Buffer
	restore := slog.Default()
	t.Cleanup(func() { slog.SetDefault(restore) })

	if _, err := newWorkerLogger(workerConfig{logLevel: "info", logFormat: "json"}, &logged); err != nil {
		t.Fatalf("building this role's logger: %v", err)
	}

	correlation := ids.NewV7()
	ctx := principal.WithCorrelationID(t.Context(), correlation)
	cause := errors.New("dial tcp: lookup provider.example: no such host")

	if stored := jobs.FaultContext(ctx, cause); stored == nil {
		t.Fatal("a cause answered no fault at all")
	}

	line := logged.String()
	if line == "" {
		t.Fatal("the fault line reached no sink this role configured — it went wherever the stdlib default points")
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &record); err != nil {
		t.Fatalf("the fault line is not JSON, so a collector reading this role's output cannot parse it: %q", line)
	}
	if got := record["correlation_id"]; got != correlation.String() {
		t.Fatalf("correlation_id = %v, want %q — the line cannot be joined to the audit rows and events of the same request",
			got, correlation)
	}
	// STAMPED ONCE. The id used to be attached by hand here AND by the handler,
	// which under a JSON encoder is one key written twice — so this counts
	// occurrences rather than trusting that the decoded map has one entry, since
	// decoding a duplicated key silently keeps the last.
	if n := strings.Count(line, "correlation_id"); n != 1 {
		t.Fatalf("correlation_id appears %d times, want once: %q", n, line)
	}
}
