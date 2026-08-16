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
	"github.com/gradionhq/margince/backend/internal/shared/kernel/pipelinetrace"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
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
		{"no connector", TraceEntry{
			Stage:        pipelinetrace.StageTierLadder,
			SourceSystem: "gmail", SourceID: "m-1", Outcome: TraceCaptured,
		}, "connector"},
		{"no source system", TraceEntry{
			Stage:     pipelinetrace.StageTierLadder,
			Connector: "gmail", SourceID: "m-1", Outcome: TraceCaptured,
		}, "natural key"},
		{"no source id", TraceEntry{
			Stage:     pipelinetrace.StageTierLadder,
			Connector: "gmail", SourceSystem: "gmail", Outcome: TraceCaptured,
		}, "natural key"},
		{"no outcome", TraceEntry{
			Stage:     pipelinetrace.StageTierLadder,
			Connector: "gmail", SourceSystem: "gmail", SourceID: "m-1",
		}, "outcome"},
		{"no stage", TraceEntry{
			Connector: "gmail", SourceSystem: "gmail", SourceID: "m-1", Outcome: TraceCaptured,
		}, "pipeline stage"},
		// A stage the registry answers by DERIVING would violate the column's
		// CHECK and fail the whole capture. Refusing it here names which stage
		// was wrong; a constraint violation names nothing a caller can act on.
		{"a derived stage", TraceEntry{
			Stage:     pipelinetrace.StageAttentionLabel,
			Connector: "gmail", SourceSystem: "gmail", SourceID: "m-1", Outcome: TraceCaptured,
		}, "not a stage this pipeline stores"},
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
		Stage:     pipelinetrace.StageTierLadder,
		Connector: "gmail", SourceSystem: "gmail", SourceID: "m-1", Outcome: TraceCaptured,
	}
	if err := entry.validate(); err != nil {
		t.Errorf("validate() = %v for a complete entry, want nil", err)
	}
}

func TestEveryStageCaptureWritesIsOneTheColumnAccepts(t *testing.T) {
	// The three writers name their stage as a constant, and the column's CHECK
	// admits exactly the registry's stored set. This walks the registry rather
	// than listing the three, so a stage added to one side without the other
	// fails here instead of at an INSERT that takes a capture down with it.
	for _, stage := range pipelinetrace.StoredStages() {
		entry := TraceEntry{
			Stage:     stage,
			Connector: "gmail", SourceSystem: "gmail", SourceID: "m-1", Outcome: TraceCaptured,
		}
		if err := entry.validate(); err != nil {
			t.Errorf("validate() refused stored stage %q: %v", stage, err)
		}
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

// One transport, one spelling. The connector column is what the screen groups
// by and what /v1/channel-providers resolves to a label, so a connector that
// answers two ways appears as two connectors and only one of them has a name.
//
// The two records below differ only in how they name their human — an account
// on one, an address on the other — which is the axis that must NOT reach this
// answer. They arrived on the same transport.
func TestOneTransportIsSpelledOneWay(t *testing.T) {
	channelRecord := func(cp connector.Counterparty) connector.NormalizedRecord {
		return connector.NormalizedRecord{
			NaturalKey:   connector.NaturalKey{SourceSystem: "ext:dispact-connector:dispact", SourceID: "m-1"},
			Counterparty: cp,
			Fields:       ActivityFields{Kind: "message", ChannelProvider: "dispact"},
		}
	}
	named := channelRecord(connector.Counterparty{
		ChannelIdentity: connector.ChannelIdentity{Provider: "dispact", ChannelUserID: "u-1"},
	})
	mentioned := channelRecord(connector.Counterparty{Email: "someone@client.io"})
	if got, want := traceConnector(named), "dispact"; got != want {
		t.Errorf("a direct message names connector %q, want %q", got, want)
	}
	if got, want := traceConnector(mentioned), "dispact"; got != want {
		t.Errorf("a mention names connector %q, want %q — the transport carried both", got, want)
	}

	// Mail arrived on no channel, so its source system IS the transport.
	mail := connector.NormalizedRecord{
		NaturalKey:   connector.NaturalKey{SourceSystem: "gmail", SourceID: "m-2"},
		Counterparty: connector.Counterparty{Email: "someone@client.io"},
		Fields:       ActivityFields{Kind: "email"},
	}
	if got, want := traceConnector(mail), "gmail"; got != want {
		t.Errorf("mail names connector %q, want %q", got, want)
	}
}

// The join's outcome list and LadderDispositionOutcomes are one list.
//
// The SQL spells its literals so both queries stay compile-time constants, so
// nothing but this makes the two agree. A tier that starts recording a
// disposition joins the Go list and is then invisible to the read — its
// messages would report no verdict, which is the bug the list exists to
// prevent, in a new place.
func TestTheResolutionJoinSpellsEveryLadderDispositionOutcome(t *testing.T) {
	for _, outcome := range LadderDispositionOutcomes {
		if !strings.Contains(resolutionJoin, "'"+string(outcome)+"'") {
			t.Errorf("the resolution join does not name %q — a message with that outcome would report no verdict", outcome)
		}
	}
	// And nothing BEYOND the list: an outcome added to the SQL alone widens what
	// the join discloses with no Go declaration saying it should.
	for _, outcome := range []TraceOutcome{TraceCaptured, TraceInternal, TraceFault} {
		if strings.Contains(resolutionJoin, "'"+string(outcome)+"'") {
			t.Errorf("the resolution join names %q, which records no disposition", outcome)
		}
	}
}

// The widened arm stays on the personal side of the read.
//
// A ledger row's owner is an individual member, and the workspace scope is the
// one a manager holds a grant for. The mail arm is structurally personal
// already; this one has to say so.
func TestTheWidenedResolutionArmRequiresAMember(t *testing.T) {
	if !strings.Contains(resolutionJoin, "t.user_id IS NOT NULL") {
		t.Error("the widened arm does not require a member — a workspace-owned row could report a verdict raised by one member's own correspondence")
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
	WriteTraceErr(w, r, errNoCallingMember)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(w.Body.String(), "calling member") {
		t.Errorf("body = %q, want it to say what is missing", w.Body.String())
	}
}
