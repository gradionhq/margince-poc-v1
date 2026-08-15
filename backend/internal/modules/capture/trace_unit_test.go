// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The parts of the trace that need no database: what it refuses to record, and
// what it tells an operator's scrape.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

func TestATraceEntryMustNameWhatItDescribes(t *testing.T) {
	// Each of these would write a row nothing can read back or dedupe. They are
	// programming errors at a call site, so they fail loudly and name the field
	// rather than writing a row somebody later has to explain.
	for _, tc := range []struct {
		name  string
		entry TraceEntry
		want  string
	}{
		{"no connector", TraceEntry{SourceSystem: "gmail", SourceID: "m-1", Outcome: TraceCaptured}, "connector"},
		{"no source system", TraceEntry{Connector: "gmail", SourceID: "m-1", Outcome: TraceCaptured}, "natural key"},
		{"no source id", TraceEntry{Connector: "gmail", SourceSystem: "gmail", Outcome: TraceCaptured}, "natural key"},
		{"no outcome", TraceEntry{Connector: "gmail", SourceSystem: "gmail", SourceID: "m-1"}, "outcome"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.entry.validate()
			if err == nil {
				t.Fatalf("validate() = nil for %s, want a refusal", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to name %q", err, tc.want)
			}
		})
	}
}

func TestAValidEntryPasses(t *testing.T) {
	entry := TraceEntry{
		Connector: "gmail", SourceSystem: "gmail", SourceID: "m-1", Outcome: TraceCaptured,
	}
	if err := entry.validate(); err != nil {
		t.Errorf("validate() = %v for a complete entry, want nil", err)
	}
}

func TestTheProcessCounterReportsWhatItCounted(t *testing.T) {
	// The counter is process-wide by design (the metrics endpoint reads it
	// without holding a Sink), so this asserts a delta rather than an absolute:
	// another test in this binary may have traced something first.
	before := TraceOutcomeTotals()[string(TraceInternal)]
	countTraced(TraceInternal)
	countTraced(TraceInternal)

	if got := TraceOutcomeTotals()[string(TraceInternal)]; got != before+2 {
		t.Errorf("counter = %d, want %d", got, before+2)
	}
}

func TestAChannelSourceIdHashesToItselfEveryTime(t *testing.T) {
	// Dedupe is equality, so the hash has to be stable across calls — otherwise
	// the unique index stops recognising a replay and the funnel counts polls.
	const account = "chat-77:9001"
	first, second := traceSourceID(account, true), traceSourceID(account, true)
	if first != second {
		t.Errorf("hash is not stable: %q then %q", first, second)
	}
	if strings.Contains(first, account) {
		t.Errorf("hash %q still contains the account id", first)
	}
	if plain := traceSourceID(account, false); plain != account {
		t.Errorf("mail id = %q, want it kept verbatim", plain)
	}
}

func TestTheWorkspaceReadRefusesAnUncomposedDeployment(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/capture/activity/workspace", nil)
	TraceHandlers{}.ListWorkspaceCaptureActivity(w, r, crmcontracts.ListWorkspaceCaptureActivityParams{})
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

func TestAReadWithNoMemberBehindItSaysSo(t *testing.T) {
	// Not a permission refusal: nobody is being denied their own traffic, there
	// is simply no member on this invocation to have any.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/capture/activity", nil)
	TraceHandlers{store: &TraceStore{}}.writeTraceErr(w, r, errNoCallingMember)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(w.Body.String(), "calling member") {
		t.Errorf("body = %q, want it to say what is missing", w.Body.String())
	}
}
