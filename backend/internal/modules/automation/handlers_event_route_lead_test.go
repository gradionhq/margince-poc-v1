// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// fakeCreateRecorder is a DB-free stand-in for the Provider seam: records
// every Create call so a test can assert route_lead's Plan reached
// ApplyActions' create_task executor with the right entity/fields, and
// nothing else — every other method panics, the same convention
// fakeReadProvider (handlers_event_test.go) uses for the read
// side.
type fakeCreateRecorder struct {
	calls []datasource.CreateInput
	err   error
}

func (p *fakeCreateRecorder) Create(_ context.Context, in datasource.CreateInput) (datasource.EntityRef, error) {
	p.calls = append(p.calls, in)
	return datasource.EntityRef{}, p.err
}

func (p *fakeCreateRecorder) Read(context.Context, datasource.EntityRef) (datasource.Record, error) {
	panic("fakeCreateRecorder: Read not stubbed for this test")
}

func (p *fakeCreateRecorder) Search(context.Context, datasource.SearchQuery) (datasource.SearchResult, error) {
	panic("fakeCreateRecorder: Search not stubbed for this test")
}

func (p *fakeCreateRecorder) ListObjects(context.Context) ([]datasource.ObjectDef, error) {
	panic("fakeCreateRecorder: ListObjects not stubbed for this test")
}

func (p *fakeCreateRecorder) ListFields(context.Context, datasource.EntityType) ([]datasource.FieldDef, error) {
	panic("fakeCreateRecorder: ListFields not stubbed for this test")
}

func (p *fakeCreateRecorder) RunReport(context.Context, datasource.ReportPlan) (datasource.ReportResult, error) {
	panic("fakeCreateRecorder: RunReport not stubbed for this test")
}

func (p *fakeCreateRecorder) StageSemantic(context.Context, ids.UUID) (string, ids.UUID, error) {
	panic("fakeCreateRecorder: StageSemantic not stubbed for this test")
}

func (p *fakeCreateRecorder) Update(context.Context, datasource.UpdateInput) (datasource.EntityRef, error) {
	panic("fakeCreateRecorder: Update not stubbed for this test")
}

func (p *fakeCreateRecorder) AdvanceDeal(context.Context, datasource.AdvanceDealInput) (datasource.EntityRef, error) {
	panic("fakeCreateRecorder: AdvanceDeal not stubbed for this test")
}

func (p *fakeCreateRecorder) Archive(context.Context, datasource.EntityRef) (datasource.EntityRef, error) {
	panic("fakeCreateRecorder: Archive not stubbed for this test")
}

func (p *fakeCreateRecorder) Merge(context.Context, datasource.MergeInput) (datasource.EntityRef, error) {
	panic("fakeCreateRecorder: Merge not stubbed for this test")
}

func (p *fakeCreateRecorder) PromoteLead(context.Context, ids.UUID, string, *string) (datasource.EntityRef, bool, error) {
	panic("fakeCreateRecorder: PromoteLead not stubbed for this test")
}

func (p *fakeCreateRecorder) Freshness(context.Context, datasource.EntityRef) (datasource.FreshnessInfo, error) {
	panic("fakeCreateRecorder: Freshness not stubbed for this test")
}

var _ datasource.SystemOfRecordProvider = (*fakeCreateRecorder)(nil)

// TestRouteLeadSpecNamesTheCatalogKey pins the orphan-key trap: the
// engine dispatches instances[Spec().Name], so a Name that drifts from
// the catalog entry Catalog() seeds under "route_lead" would make this
// handler a silent no-op forever.
func TestRouteLeadSpecNamesTheCatalogKey(t *testing.T) {
	spec := routeLeadCreateTask{}.Spec()
	if spec.Name != routeLeadName {
		t.Errorf("Spec().Name = %q, want %q", spec.Name, routeLeadName)
	}
	if spec.Trigger.EventType != eventLeadCreated {
		t.Errorf("Spec().Trigger.EventType = %q, want %q", spec.Trigger.EventType, eventLeadCreated)
	}
	if spec.Tier != mcp.TierAutoExecute {
		t.Errorf("Spec().Tier = %v, want TierAutoExecute", spec.Tier)
	}
}

// TestRouteLeadMatchFiresOnEveryNewLead proves Match is unconditional —
// there is no "wrong kind" of lead.created that should skip the
// follow-up, unlike stage_change_create_task's open-only narrowing.
func TestRouteLeadMatchFiresOnEveryNewLead(t *testing.T) {
	cases := []struct {
		name    string
		payload json.RawMessage
	}{
		{"no payload", nil},
		{"arbitrary payload", json.RawMessage(`{"source":"webinar"}`)},
	}
	for _, tc := range cases {
		matched, err := routeLeadCreateTask{}.Match(context.Background(), workflow.Event{Payload: tc.payload})
		if err != nil {
			t.Fatalf("%s: Match err = %v, want nil", tc.name, err)
		}
		if !matched {
			t.Errorf("%s: Match = false, want true (every new lead gets a follow-up)", tc.name)
		}
	}
}

// TestRouteLeadPlanEmitsOneCreateTaskOnTheLead proves Plan emits exactly
// one create_task action anchored on the fired lead, due the default
// number of days out, when the instance carries no params override.
func TestRouteLeadPlanEmitsOneCreateTaskOnTheLead(t *testing.T) {
	leadID := ids.NewV7()
	occurredAt := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
	ev := workflow.Event{
		Entity:     datasource.EntityRef{Type: datasource.EntityLead, ID: leadID},
		OccurredAt: occurredAt,
	}

	eff, err := routeLeadCreateTask{}.Plan(context.Background(), ev)
	if err != nil {
		t.Fatalf("Plan err = %v, want nil", err)
	}
	if len(eff.Actions) != 1 {
		t.Fatalf("Plan emitted %d actions, want exactly 1", len(eff.Actions))
	}
	action := eff.Actions[0]
	if action.Kind != workflow.ActionCreateTask {
		t.Errorf("action.Kind = %q, want %q", action.Kind, workflow.ActionCreateTask)
	}
	if action.Target != ev.Entity {
		t.Errorf("action.Target = %v, want the new lead %v", action.Target, ev.Entity)
	}
	var args struct {
		Kind    string    `json:"kind"`
		Subject string    `json:"subject"`
		DueAt   time.Time `json:"due_at"`
		Links   []struct {
			EntityType string `json:"entity_type"`
			EntityID   string `json:"entity_id"`
		} `json:"links"`
	}
	if err := json.Unmarshal(action.Args, &args); err != nil {
		t.Fatalf("action.Args do not decode: %v", err)
	}
	if args.Kind != "task" {
		t.Errorf("args.Kind = %q, want %q", args.Kind, "task")
	}
	if args.Subject == "" {
		t.Error("args.Subject is empty — a rep reading the task needs to know why it exists")
	}
	wantDue := occurredAt.AddDate(0, 0, defaultRouteLeadDueInDays)
	if !args.DueAt.Equal(wantDue) {
		t.Errorf("args.DueAt = %v, want the default %d days out (%v)", args.DueAt, defaultRouteLeadDueInDays, wantDue)
	}
	if len(args.Links) != 1 || args.Links[0].EntityType != "lead" || args.Links[0].EntityID != leadID.String() {
		t.Errorf("args.Links = %+v, want exactly one link to the new lead %v", args.Links, leadID)
	}
}

// TestRouteLeadPlanHonorsTheInstancesOwnDueInDays proves the instance's
// own due_in_days param overrides the default — the same shared
// DueInDays reader stage_change_create_task's Plan uses, so an author's
// configured cadence actually reaches the run.
func TestRouteLeadPlanHonorsTheInstancesOwnDueInDays(t *testing.T) {
	occurredAt := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
	ev := workflow.Event{
		Entity:     datasource.EntityRef{Type: datasource.EntityLead, ID: ids.NewV7()},
		OccurredAt: occurredAt,
		Params:     json.RawMessage(`{"due_in_days": 5}`),
	}
	eff, err := routeLeadCreateTask{}.Plan(context.Background(), ev)
	if err != nil {
		t.Fatalf("Plan err = %v, want nil", err)
	}
	var args struct {
		DueAt time.Time `json:"due_at"`
	}
	if err := json.Unmarshal(eff.Actions[0].Args, &args); err != nil {
		t.Fatalf("action.Args do not decode: %v", err)
	}
	want := occurredAt.AddDate(0, 0, 5)
	if !args.DueAt.Equal(want) {
		t.Errorf("args.DueAt = %v, want the configured 5 days out (%v)", args.DueAt, want)
	}
}

// TestRouteLeadPlanNeverSwallowsAMalformedParamsBlob proves an
// undecodable params blob surfaces as an error rather than Plan silently
// falling back to the default — a swallowed decode failure would hide a
// corrupt instance row.
func TestRouteLeadPlanNeverSwallowsAMalformedParamsBlob(t *testing.T) {
	ev := workflow.Event{
		Entity: datasource.EntityRef{Type: datasource.EntityLead, ID: ids.NewV7()},
		Params: json.RawMessage(`{"due_in_days":`),
	}
	w := routeLeadCreateTask{}
	if _, err := w.Plan(context.Background(), ev); err == nil {
		t.Fatal("Plan err = nil for a malformed params blob, want a decode error")
	}
}

// TestRouteLeadApplyDelegatesToApplyActions proves Apply threads the
// planned effect through the shared executor (ApplyActions) rather than
// reimplementing dispatch — the same shape every other starter's Apply
// proves.
func TestRouteLeadApplyDelegatesToApplyActions(t *testing.T) {
	provider := &fakeCreateRecorder{}
	w := routeLeadCreateTask{ex: Executors{Provider: provider}}
	leadID := ids.NewV7()
	ev := workflow.Event{
		Entity:     datasource.EntityRef{Type: datasource.EntityLead, ID: leadID},
		OccurredAt: time.Now().UTC(),
	}

	eff, err := w.Plan(context.Background(), ev)
	if err != nil {
		t.Fatalf("Plan err = %v, want nil", err)
	}
	result, err := w.Apply(context.Background(), ev, eff, nil)
	if err != nil {
		t.Fatalf("Apply err = %v, want nil", err)
	}
	if len(result.Applied) != 1 {
		t.Fatalf("Apply result.Applied = %v, want the one create_task action", result.Applied)
	}
	if len(provider.calls) != 1 {
		t.Fatalf("Provider.Create called %d times, want exactly 1 (Apply must delegate through ApplyActions)", len(provider.calls))
	}
	if provider.calls[0].EntityType != datasource.EntityActivity {
		t.Errorf("Create entity type = %q, want %q (create_task always mints an activity)", provider.calls[0].EntityType, datasource.EntityActivity)
	}
}

// TestRouteLeadIdempotencyKeyIsStablePerEvent proves the key is derived
// from the event id, so a redelivered lead.created claims the same run
// row instead of minting a second follow-up task.
func TestRouteLeadIdempotencyKeyIsStablePerEvent(t *testing.T) {
	evID := ids.NewV7()
	key1 := routeLeadCreateTask{}.IdempotencyKey(workflow.Event{ID: evID})
	key2 := routeLeadCreateTask{}.IdempotencyKey(workflow.Event{ID: evID})
	if key1 != key2 {
		t.Errorf("IdempotencyKey is not stable across calls for the same event: %q != %q", key1, key2)
	}
	other := routeLeadCreateTask{}.IdempotencyKey(workflow.Event{ID: ids.NewV7()})
	if key1 == other {
		t.Error("IdempotencyKey did not vary across distinct events — replays of different events would collide")
	}
}
