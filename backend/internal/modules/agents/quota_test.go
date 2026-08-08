// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/agentquota"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// countingCharger is the meter as the registry uses it: it only ever receives
// charges, which is the whole of this half of the seam. It keeps them PER
// COUNTER, because most of what is worth asserting here is that a call was
// charged against the right one.
type countingCharger struct {
	charged map[agentquota.Counter]int
	times   map[agentquota.Counter]int
	err     error
	// errOn narrows a failing meter to ONE counter. Redis being down fails
	// every charge, and a test about the answer-stage asymmetry needs the
	// call-ceiling charge — which runs first and refuses before the handler —
	// to succeed, or it proves the wrong refusal.
	errOn agentquota.Counter
}

func newCountingCharger() *countingCharger {
	return &countingCharger{charged: map[agentquota.Counter]int{}, times: map[agentquota.Counter]int{}}
}

func (c *countingCharger) Consume(_ context.Context, counter agentquota.Counter, n int) error {
	c.times[counter]++
	c.charged[counter] += n
	if c.err != nil && (c.errOn == "" || c.errOn == counter) {
		return c.err
	}
	return nil
}

// reads is what the counter every pre-existing spec here is about was charged.
func (c *countingCharger) reads() int { return c.charged[agentquota.Reads] }

// servingTool hands back the records it was built with, through the ONE place
// a datasource.Record becomes tool output — so it exercises the charge point
// the real tools ride rather than a parallel one written for the test.
type servingTool struct {
	spec    mcp.ToolSpec
	records int
	fail    bool
}

func (s *servingTool) Spec() mcp.ToolSpec { return s.spec }

func (s *servingTool) Handle(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	for range s.records {
		newWireRecord(ctx, datasource.Record{
			Ref: datasource.EntityRef{Type: "person", ID: ids.NewV7()},
		})
	}
	if s.fail {
		return nil, errors.New("the handler failed after reading")
	}
	return json.RawMessage(`{}`), nil
}

func readToolSpec(name string) mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: name, Title: name, Version: testToolVersion, Description: describedForRegistration,
		InputSchema:   json.RawMessage(`{"type":"object"}`),
		RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
	}
}

// chargingRegistry is the ONE setup every test here shares: a registry with a
// counting charger, the tool under test registered, and an agent context
// holding scope. Options vary only what a given test is about.
func chargingRegistry(t *testing.T, tool mcp.Tool, opts ...chargeTestOption) (*Registry, *countingCharger, context.Context) {
	t.Helper()
	cfg := chargeTest{charger: newCountingCharger(), scope: principal.ScopeRead}
	for _, opt := range opts {
		opt(&cfg)
	}
	r := NewRegistry(nil, auth.NewGate(fullSeatAuthority{}), WithQuotaCharger(cfg.charger))
	if cfg.noCharger {
		r = NewRegistry(nil, auth.NewGate(fullSeatAuthority{}))
	}
	r.Register(tool)
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:t", OnBehalfOf: ids.NewV7(),
		PassportID: ids.NewV7(),
		Scopes:     principal.NewScopeSet(cfg.scope),
	})
	return r, cfg.charger, ctx
}

type chargeTest struct {
	charger   *countingCharger
	scope     principal.Scope
	noCharger bool
}

type chargeTestOption func(*chargeTest)

// withChargerError makes the meter unreachable, so a test can say what an
// uncountable charge does.
func withChargerError(err error) chargeTestOption {
	return func(c *chargeTest) { c.charger.err = err }
}

// withChargerErrorOn makes exactly one counter unrecordable.
func withChargerErrorOn(counter agentquota.Counter, err error) chargeTestOption {
	return func(c *chargeTest) { c.charger.err, c.charger.errOn = err, counter }
}

// withScope runs the caller under a scope other than read.
func withScope(s principal.Scope) chargeTestOption {
	return func(c *chargeTest) { c.scope = s }
}

// withNoCharger composes the registry with no meter at all.
func withNoCharger() chargeTestOption {
	return func(c *chargeTest) { c.noCharger = true }
}

// The charge is PER RECORD, taken where records leave the surface. One call
// answering twenty records costs twenty, which is the whole of what
// "per-record, not per-call" means — and the reason it is charged here rather
// than in each tool is that the tool added next is the one that would forget.
func TestAnAnswerChargesForEveryRecordItServed(t *testing.T) {
	r, charger, ctx := chargingRegistry(t, &servingTool{spec: readToolSpec("search_records"), records: 20})

	if _, err := r.Invoke(ctx, "search_records", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}

	if charger.reads() != 20 {
		t.Errorf("a 20-record answer charged %d records", charger.reads())
	}
}

// The same record served twice in one answer is charged twice. The envelope's
// evidence list dedupes by value — one record read twice is one thing to cite —
// and reusing that count for the bound would let a handler that reads a page
// twice pay for it once.
func TestARecordServedTwiceIsChargedTwice(t *testing.T) {
	r, charger, ctx := chargingRegistry(t, &repeatingTool{spec: readToolSpec("read_record"), id: ids.NewV7(), times: 3})

	if _, err := r.Invoke(ctx, "read_record", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}

	if charger.reads() != 3 {
		t.Errorf("one record served three times charged %d; the bound counts what was handed over, not what is citable", charger.reads())
	}
}

type repeatingTool struct {
	spec  mcp.ToolSpec
	id    ids.UUID
	times int
}

func (rt *repeatingTool) Spec() mcp.ToolSpec { return rt.spec }

func (rt *repeatingTool) Handle(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	for range rt.times {
		newWireRecord(ctx, datasource.Record{Ref: datasource.EntityRef{Type: "person", ID: rt.id}})
	}
	return json.RawMessage(`{}`), nil
}

// A failed handler served nothing, so it costs nothing. Charging it would step
// an agent up for records it never received — and an agent could be locked out
// by a fault on our side rather than by its own reading.
func TestAFailedAnswerChargesNothing(t *testing.T) {
	r, charger, ctx := chargingRegistry(t, &servingTool{spec: readToolSpec("search_records"), records: 20, fail: true})

	if _, err := r.Invoke(ctx, "search_records", json.RawMessage(`{}`)); err == nil {
		t.Fatal("the failing handler reported success")
	}

	if charger.times[agentquota.Reads] != 0 {
		t.Errorf("a failed answer charged the read bound %d times", charger.times[agentquota.Reads])
	}
}

// An answer that carries no records at all does not touch the meter. Worth
// pinning because the alternative — charging one per CALL as a floor — is the
// per-call metering A139 rejects, and it would arrive by accident.
func TestAnAnswerWithNoRecordsChargesNothing(t *testing.T) {
	r, charger, ctx := chargingRegistry(t, &servingTool{spec: readToolSpec("list_pipelines")})

	if _, err := r.Invoke(ctx, "list_pipelines", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}

	if charger.times[agentquota.Reads] != 0 {
		t.Errorf("a record-free answer charged the read bound %d times", charger.times[agentquota.Reads])
	}
}

// A read that cannot be COUNTED is not served. Logging and answering anyway
// looks contained — the gate fails closed while the counter is down — but a
// charge lost to a transient write error is lost for good: the counter comes
// back short and those records are read again for free. Every blip would
// quietly raise the ceiling.
func TestAnAnswerThatCannotBeCountedIsWithheld(t *testing.T) {
	r, _, ctx := chargingRegistry(t,
		&servingTool{spec: readToolSpec("search_records"), records: 5},
		withChargerError(errors.New("redis is unreachable")))

	out, err := r.Invoke(ctx, "search_records", json.RawMessage(`{}`))

	if !errors.Is(err, apperrors.ErrBudgetExceeded) {
		t.Fatalf("an uncountable read → %v, want ErrBudgetExceeded", err)
	}
	if out != nil {
		t.Error("the answer was served anyway; a read that cannot be counted must not be handed over")
	}
}

// A write tool that hands a record back still COUNTS toward the window, even
// though the bound never refuses one. The read bound measures records handed
// over; a surface where a read-back was free would meter one door and leave
// the other open beside it.
func TestAWriteThatReturnsARecordStillCharges(t *testing.T) {
	spec := readToolSpec("update_record")
	spec.RequiredScope = principal.ScopeWrite
	r, charger, ctx := chargingRegistry(t, &servingTool{spec: spec, records: 1}, withScope(principal.ScopeWrite))

	if _, err := r.Invoke(ctx, "update_record", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}

	if charger.reads() != 1 {
		t.Errorf("a write's read-back charged %d records", charger.reads())
	}
}

// A registry composed without a charger records nothing and does not crash.
// The Surface-B runner builds one, and it runs as the human or the system that
// started it — neither of whom this bound governs.
func TestARegistryWithNoChargerServesNormally(t *testing.T) {
	r, _, ctx := chargingRegistry(t, &servingTool{spec: readToolSpec("search_records"), records: 3}, withNoCharger())

	if _, err := r.Invoke(ctx, "search_records", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("a registry with no read charger failed to serve: %v", err)
	}
}

// namingTool answers the way the intent tools do — with ids and derived prose
// rather than rows, through noteEvidence.
type namingTool struct {
	spec  mcp.ToolSpec
	names int
}

func (n *namingTool) Spec() mcp.ToolSpec { return n.spec }

func (n *namingTool) Handle(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	noteDerivedContent(ctx)
	for range n.names {
		noteEvidence(ctx, "deal", ids.NewV7())
	}
	return json.RawMessage(`{}`), nil
}

// A tool that NAMES records without holding their rows still charges for them.
// The intent family — the slipping sweep, the coverage reads, the catch-up —
// answers this way, and each surfaces many records per call. If only the tools
// that hold rows charged, the ones that surface the most records would be the
// cheapest reads on the surface: A139's failure, one tool family over.
func TestAToolThatNamesRecordsChargesForThem(t *testing.T) {
	r, charger, ctx := chargingRegistry(t, &namingTool{spec: readToolSpec("whats_slipping_this_week"), names: 50})

	if _, err := r.Invoke(ctx, "whats_slipping_this_week", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}

	if charger.reads() != 50 {
		t.Errorf("a 50-record answer built from named records charged %d", charger.reads())
	}
}

// A WRITE whose charge fails is still served. By the time the charge runs the
// mutation has committed and any approval it redeemed is consumed — send_email
// has SENT. Reporting that as a failure invites the caller to retry an
// irreversible act, and a second email costs more than an uncounted read.
func TestAWriteIsServedEvenWhenItsChargeCannotBeRecorded(t *testing.T) {
	spec := readToolSpec("send_email")
	spec.RequiredScope = principal.ScopeWrite
	r, _, ctx := chargingRegistry(t, &servingTool{spec: spec, records: 1},
		withScope(principal.ScopeWrite),
		// The ANSWER-stage charge is the one that fails here: by then the send
		// has happened. A meter that failed the call-ceiling charge too would
		// refuse before the handler ran, which is the opposite property.
		withChargerErrorOn(agentquota.Reads, errors.New("redis is unreachable")))

	out, err := r.Invoke(ctx, "send_email", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("a committed write was reported as failed because it could not be counted: %v", err)
	}
	if len(out) == 0 {
		t.Error("the write's result was withheld")
	}
}

// Every admitted call spends the ceiling every other quota sits under, and it
// spends exactly one however many records the answer carried. Charging it per
// record would make MCP-SESS-CALLS a second read bound with a different number.
func TestEveryAdmittedCallSpendsOneOfTheCallCeiling(t *testing.T) {
	r, charger, ctx := chargingRegistry(t, &servingTool{spec: readToolSpec("search_records"), records: 20})

	if _, err := r.Invoke(ctx, "search_records", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}

	if got := charger.charged[agentquota.Calls]; got != 1 {
		t.Errorf("one call answering 20 records spent %d of the call ceiling, want 1", got)
	}
}

// A call the meter cannot COUNT does not run. This is the one charge point at
// which nothing has happened yet, which is exactly what lets it refuse — and
// the ceiling it defends is the one every other quota sits under, so leaving it
// uncounted while Redis blinks would let a flood through the gap.
func TestACallThatCannotBeCountedIsNotRun(t *testing.T) {
	tool := &servingTool{spec: readToolSpec("search_records"), records: 5}
	r, charger, ctx := chargingRegistry(t, tool,
		withChargerErrorOn(agentquota.Calls, errors.New("redis is unreachable")))

	out, err := r.Invoke(ctx, "search_records", json.RawMessage(`{}`))

	if !errors.Is(err, apperrors.ErrBudgetExceeded) {
		t.Fatalf("an uncountable call → %v, want ErrBudgetExceeded", err)
	}
	if out != nil {
		t.Error("the answer was served anyway")
	}
	if charger.times[agentquota.Reads] != 0 {
		t.Error("the handler ran: records were charged for a call that was never counted")
	}
}

// The act a call performs is charged against the counter its own KIND names,
// derived by the same function the gate refuses on. The two halves must agree,
// and the test that would pass without them agreeing is one that calls the
// derivation directly — so this one goes through the registry.
func TestTheActIsChargedAgainstTheCounterItsKindNames(t *testing.T) {
	cases := []struct {
		name    string
		scope   principal.Scope
		egress  bool
		counter agentquota.Counter
	}{
		{"update_record", principal.ScopeWrite, false, agentquota.Writes},
		{"send_email", principal.ScopeSend, true, agentquota.Egress},
	}
	for _, c := range cases {
		spec := readToolSpec(c.name)
		spec.RequiredScope, spec.Egress = c.scope, c.egress
		r, charger, ctx := chargingRegistry(t, &servingTool{spec: spec, records: 1}, withScope(c.scope))

		if _, err := r.Invoke(ctx, c.name, json.RawMessage(`{}`)); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}

		if got := charger.charged[c.counter]; got != 1 {
			t.Errorf("%s spent %d of %s, want 1", c.name, got, c.counter)
		}
		// And nothing of the OTHER act counter: a send that also spent the write
		// quota would exhaust the loose bound while the tight one guarded nothing.
		other := agentquota.Writes
		if c.counter == agentquota.Writes {
			other = agentquota.Egress
		}
		if got := charger.charged[other]; got != 0 {
			t.Errorf("%s also spent %d of %s", c.name, got, other)
		}
	}
}

// A read-only tool is charged ONCE, per record, and never a second time for the
// act. Its act IS its records; a separate call-shaped charge on top would meter
// the same answer twice against the same counter.
func TestAReadOnlyToolIsChargedForItsRecordsAndNotAlsoForTheAct(t *testing.T) {
	r, charger, ctx := chargingRegistry(t, &servingTool{spec: readToolSpec("search_records"), records: 4})

	if _, err := r.Invoke(ctx, "search_records", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}

	if charger.reads() != 4 {
		t.Errorf("a 4-record read charged %d", charger.reads())
	}
	if charger.times[agentquota.Reads] != 1 {
		t.Errorf("a read-only answer touched the read counter %d times, want one", charger.times[agentquota.Reads])
	}
}
