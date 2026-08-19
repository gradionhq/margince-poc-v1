// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package jobs

// WHAT THE FAULT LOG LINE CARRIES, held as a shape rather than as a substring.
//
// This line is the one place the cause is allowed to be detailed: the stored
// sentence is fixed because river_job.errors is unscoped and fleet-visible, and
// the process log is the other half of that trade — its audience and its
// retention are the operator's own. So the whole diagnosis rests here, and for
// two dispositions it rests here ALONE. An unclassified failure stores a sentence
// saying only that the diagnosis is in the process log, and a postponement makes
// River record no attempt error at all.
//
// A collector is what reads it, which is why the cases below decode the record
// instead of grepping it. "The cause reached the log" is already asserted
// elsewhere by substring; what those cases cannot see is a field that stopped
// being a field.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// faultRecord installs a JSON default handler, runs one fault through it, and
// decodes the single record it produced.
//
// JSON rather than text because the assertions are about STRUCTURE — a group
// renders as dotted keys in the text encoder, which is exactly the flattening
// that would make a broken shape look fine.
func faultRecord(ctx context.Context, t *testing.T, kind string, err error) map[string]any {
	t.Helper()
	var logged bytes.Buffer
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logged, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(restore) })

	// The return is asserted rather than discarded: a seam that logged the cause
	// and then answered nil would leave the tick reporting success, and the line
	// these cases read would be describing a failure nothing failed over.
	if stored := FaultForKind(ctx, kind, err); stored == nil {
		t.Fatal("a cause answered no fault at all")
	}

	line := strings.TrimSpace(logged.String())
	if line == "" {
		t.Fatal("the fault logged nothing, so the diagnosis it promises is nowhere")
	}
	var record map[string]any
	if decodeErr := json.Unmarshal([]byte(line), &record); decodeErr != nil {
		t.Fatalf("the fault line is not one JSON record: %v in %q", decodeErr, line)
	}
	return record
}

// group reads one nested object off a decoded record, failing rather than
// answering an empty map — an absent group and a present-but-empty one are
// different defects and must not read alike.
func group(t *testing.T, record map[string]any, name string) map[string]any {
	t.Helper()
	nested, ok := record[name].(map[string]any)
	if !ok {
		t.Fatalf("the record carries no %q group: %v", name, record)
	}
	return nested
}

// TestAnUnclassifiedFaultLogsTheCauseAsAStructuredObject.
//
// This is the branch whose stored sentence tells an operator the diagnosis is in
// the process log, so it is the branch that owes the most detail — and the TYPE
// CHAIN is the field that branch specifically needs. The message says what the
// provider said; the chain says which code said it, which is what somebody
// giving this failure a class has to know and cannot get from the text.
func TestAnUnclassifiedFaultLogsTheCauseAsAStructuredObject(t *testing.T) {
	inner := errors.New("dial tcp: lookup provider.example: no such host")
	wrapped := fmt.Errorf("polling the fleet: %w", inner)

	failure := group(t, faultRecord(t.Context(), t, unitKind, wrapped), "error")

	if got, want := failure["message"], wrapped.Error(); got != want {
		t.Fatalf("error.message = %v, want %q", got, want)
	}
	if got, want := failure["type"], fmt.Sprintf("%T", wrapped); got != want {
		t.Fatalf("error.type = %v, want %q", got, want)
	}
	chain, ok := failure["chain"].([]any)
	if !ok {
		t.Fatalf("error.chain is not a list, so the wrapping is not readable: %v", failure)
	}
	// OUTERMOST FIRST, matching the order errors.As resolves in: a reader walking
	// the chain to find which layer to classify on reads it in the order the
	// classification would.
	want := []string{fmt.Sprintf("%T", wrapped), fmt.Sprintf("%T", inner)}
	if len(chain) != len(want) {
		t.Fatalf("error.chain has %d layers, want %d: %v", len(chain), len(want), chain)
	}
	for at, layer := range want {
		if chain[at] != layer {
			t.Fatalf("error.chain[%d] = %v, want %q", at, chain[at], layer)
		}
	}
}

// TestAFaultLineNamesTheWorkspaceButLeavesTheCorrelationIDToTheHandler.
//
// The workspace is attached by this seam because nothing else knows it — the
// correlation handler reads the correlation id off the context and only that.
//
// The correlation id is NOT attached here, and that is the half worth a test.
// This seam used to attach it by hand because no process role installed its own
// handler as the default, so a package-level call reached a bare one that
// enriched nothing. Every serving role now installs a correlation-aware default
// (httpserver.InstallProcessLogger), which makes attaching it here not redundant
// but WRONG: both halves would stamp the same key. So the assertion is absence
// under a handler that is deliberately not correlation-aware — one stamper, and
// it is the one every other package-level call in the tree already goes through.
func TestAFaultLineNamesTheWorkspaceButLeavesTheCorrelationIDToTheHandler(t *testing.T) {
	workspace := ids.NewV7()
	ctx := principal.WithCorrelationID(principal.WithWorkspaceID(t.Context(), workspace), ids.NewV7())

	record := faultRecord(ctx, t, unitKind, errors.New("the socket closed early"))

	if got := record["workspace_id"]; got != workspace.String() {
		t.Fatalf("workspace_id = %v, want %q — nothing but this seam can put it on the line", got, workspace)
	}
	if _, stamped := record["correlation_id"]; stamped {
		t.Fatal("this seam stamped correlation_id itself; the correlation handler owns that key, and two stampers write it twice")
	}
	if got := record["kind"]; got != unitKind {
		t.Fatalf("kind = %v, want %q — a line nobody can tie to a job row is a line nobody can use", got, unitKind)
	}
}
