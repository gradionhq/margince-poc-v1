// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// countingCharger is the read bound as the registry uses it: it only ever
// receives charges, which is the whole of this half of the seam.
type countingCharger struct {
	charged int
	calls   int
	err     error
}

func (c *countingCharger) Consume(_ context.Context, n int) error {
	c.calls++
	c.charged += n
	return c.err
}

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

func chargingRegistry(t *testing.T, tool mcp.Tool) (*Registry, *countingCharger, context.Context) {
	t.Helper()
	charger := &countingCharger{}
	r := NewRegistry(nil, auth.NewGate(fullSeatAuthority{}), WithReadCharger(charger))
	r.Register(tool)
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:t", OnBehalfOf: ids.NewV7(),
		PassportID: ids.NewV7(),
		Scopes:     principal.NewScopeSet(principal.ScopeRead),
	})
	return r, charger, ctx
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

	if charger.charged != 20 {
		t.Errorf("a 20-record answer charged %d records", charger.charged)
	}
}

// The same record served twice in one answer is charged twice. The envelope's
// evidence list dedupes by value — one record read twice is one thing to cite —
// and reusing that count for the bound would let a handler that reads a page
// twice pay for it once.
func TestARecordServedTwiceIsChargedTwice(t *testing.T) {
	charger := &countingCharger{}
	r := NewRegistry(nil, auth.NewGate(fullSeatAuthority{}), WithReadCharger(charger))
	repeated := ids.NewV7()
	r.Register(&repeatingTool{spec: readToolSpec("read_record"), id: repeated, times: 3})
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:t", OnBehalfOf: ids.NewV7(),
		PassportID: ids.NewV7(), Scopes: principal.NewScopeSet(principal.ScopeRead),
	})

	if _, err := r.Invoke(ctx, "read_record", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}

	if charger.charged != 3 {
		t.Errorf("one record served three times charged %d; the bound counts what was handed over, not what is citable", charger.charged)
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

	if charger.calls != 0 {
		t.Errorf("a failed answer charged the read bound %d times", charger.calls)
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

	if charger.calls != 0 {
		t.Errorf("a record-free answer charged the read bound %d times", charger.calls)
	}
}

// A charge that cannot be recorded must not fail the call: the records have
// already left the surface, and answering with an error would hide a completed
// read while still leaving it uncounted. Nothing is spent through the gap —
// the gate reads the same counter and fails closed when it is unreachable.
func TestACountingFailureDoesNotFailTheAnswer(t *testing.T) {
	charger := &countingCharger{err: errors.New("redis is unreachable")}
	r := NewRegistry(nil, auth.NewGate(fullSeatAuthority{}), WithReadCharger(charger))
	r.Register(&servingTool{spec: readToolSpec("search_records"), records: 5})
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:t", OnBehalfOf: ids.NewV7(),
		PassportID: ids.NewV7(), Scopes: principal.NewScopeSet(principal.ScopeRead),
	})

	out, err := r.Invoke(ctx, "search_records", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("an unrecordable charge failed the answer: %v", err)
	}
	if len(out) == 0 {
		t.Error("the answer came back empty")
	}
}

// A write tool that hands a record back still COUNTS toward the window, even
// though the bound never refuses one. The read bound measures records handed
// over; a surface where a read-back was free would meter one door and leave
// the other open beside it.
func TestAWriteThatReturnsARecordStillCharges(t *testing.T) {
	charger := &countingCharger{}
	r := NewRegistry(nil, auth.NewGate(fullSeatAuthority{}), WithReadCharger(charger))
	spec := readToolSpec("update_record")
	spec.RequiredScope = principal.ScopeWrite
	r.Register(&servingTool{spec: spec, records: 1})
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:t", OnBehalfOf: ids.NewV7(),
		PassportID: ids.NewV7(), Scopes: principal.NewScopeSet(principal.ScopeWrite),
	})

	if _, err := r.Invoke(ctx, "update_record", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}

	if charger.charged != 1 {
		t.Errorf("a write's read-back charged %d records", charger.charged)
	}
}

// A registry composed without a charger records nothing and does not crash.
// The Surface-B runner builds one, and it runs as the human or the system that
// started it — neither of whom this bound governs.
func TestARegistryWithNoChargerServesNormally(t *testing.T) {
	r := NewRegistry(nil, auth.NewGate(fullSeatAuthority{}))
	r.Register(&servingTool{spec: readToolSpec("search_records"), records: 3})
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:t", OnBehalfOf: ids.NewV7(),
		PassportID: ids.NewV7(), Scopes: principal.NewScopeSet(principal.ScopeRead),
	})

	if _, err := r.Invoke(ctx, "search_records", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("a registry with no read charger failed to serve: %v", err)
	}
}
