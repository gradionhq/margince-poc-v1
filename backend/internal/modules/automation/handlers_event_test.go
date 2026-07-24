// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// fakeReadProvider implements only the read path stage_change_notify's
// Plan ever reaches: Read. Every other method panics — a test that
// reaches one exercises the wrong branch, and a panic says so louder
// than a wrong zero value would (the same convention fakeUpdateProvider
// uses in handlers_actions_test.go, mirrored for the read side).
type fakeReadProvider struct {
	record datasource.Record
	err    error
}

func (p *fakeReadProvider) Read(context.Context, datasource.EntityRef) (datasource.Record, error) {
	return p.record, p.err
}

func (p *fakeReadProvider) Search(context.Context, datasource.SearchQuery) (datasource.SearchResult, error) {
	panic("fakeReadProvider: Search not stubbed for this test")
}

func (p *fakeReadProvider) ListObjects(context.Context) ([]datasource.ObjectDef, error) {
	panic("fakeReadProvider: ListObjects not stubbed for this test")
}

func (p *fakeReadProvider) ListFields(context.Context, datasource.EntityType) ([]datasource.FieldDef, error) {
	panic("fakeReadProvider: ListFields not stubbed for this test")
}

func (p *fakeReadProvider) RunReport(context.Context, datasource.ReportPlan) (datasource.ReportResult, error) {
	panic("fakeReadProvider: RunReport not stubbed for this test")
}

func (p *fakeReadProvider) StageSemantic(context.Context, ids.UUID) (string, ids.UUID, error) {
	panic("fakeReadProvider: StageSemantic not stubbed for this test")
}

func (p *fakeReadProvider) Create(context.Context, datasource.CreateInput) (datasource.EntityRef, error) {
	panic("fakeReadProvider: Create not stubbed for this test")
}

func (p *fakeReadProvider) Update(context.Context, datasource.UpdateInput) (datasource.EntityRef, error) {
	panic("fakeReadProvider: Update not stubbed for this test")
}

func (p *fakeReadProvider) AdvanceDeal(context.Context, datasource.AdvanceDealInput) (datasource.EntityRef, error) {
	panic("fakeReadProvider: AdvanceDeal not stubbed for this test")
}

func (p *fakeReadProvider) Archive(context.Context, datasource.EntityRef) (datasource.EntityRef, error) {
	panic("fakeReadProvider: Archive not stubbed for this test")
}

func (p *fakeReadProvider) Merge(context.Context, datasource.MergeInput) (datasource.EntityRef, error) {
	panic("fakeReadProvider: Merge not stubbed for this test")
}

func (p *fakeReadProvider) PromoteLead(context.Context, ids.UUID, string, *string) (datasource.EntityRef, bool, error) {
	panic("fakeReadProvider: PromoteLead not stubbed for this test")
}

func (p *fakeReadProvider) Freshness(context.Context, datasource.EntityRef) (datasource.FreshnessInfo, error) {
	panic("fakeReadProvider: Freshness not stubbed for this test")
}

var _ datasource.SystemOfRecordProvider = (*fakeReadProvider)(nil)

// --- stage_change_notify ---

// TestStageChangeNotifySpecNamesTheCatalogKey pins the orphan-key trap:
// the engine dispatches instances[Spec().Name], so a Name that drifts
// from the catalog entry this PR's later slice seeds under
// "stage_change_notify" would make the handler a silent no-op forever.
func TestStageChangeNotifySpecNamesTheCatalogKey(t *testing.T) {
	spec := stageChangeNotify{}.Spec()
	if spec.Name != "stage_change_notify" {
		t.Errorf("Spec().Name = %q, want %q", spec.Name, "stage_change_notify")
	}
	if spec.Trigger.EventType != eventDealStageChanged {
		t.Errorf("Spec().Trigger.EventType = %q, want %q", spec.Trigger.EventType, eventDealStageChanged)
	}
	if spec.Tier != mcp.TierAutoExecute {
		t.Errorf("Spec().Tier = %v, want TierAutoExecute", spec.Tier)
	}
}

// TestStageChangeNotifyMatchFiresOnEveryStageMove proves Match is
// unconditional — a won/lost close and a mid-pipeline move both notify,
// since the point of telling the owner does not depend on which way the
// deal moved.
func TestStageChangeNotifyMatchFiresOnEveryStageMove(t *testing.T) {
	cases := []json.RawMessage{
		nil,
		json.RawMessage(`{"to_status":"open"}`),
		json.RawMessage(`{"to_status":"won"}`),
		json.RawMessage(`{"to_status":"lost"}`),
	}
	for _, payload := range cases {
		matched, err := stageChangeNotify{}.Match(context.Background(), workflow.Event{Payload: payload})
		if err != nil {
			t.Fatalf("Match(%s) err = %v, want nil", payload, err)
		}
		if !matched {
			t.Errorf("Match(%s) = false, want true (notify fires on every stage move)", payload)
		}
	}
}

// TestStageChangeNotifyPlanEmitsOneNotifyToTheDealOwner proves Plan reads
// the moved deal's real owner off the record — never a fabricated or
// empty recipient — and emits exactly one notify action naming it.
func TestStageChangeNotifyPlanEmitsOneNotifyToTheDealOwner(t *testing.T) {
	owner := ids.NewV7()
	dealID := ids.NewV7()
	fields, err := json.Marshal(dealOwnerFields{OwnerID: &owner})
	if err != nil {
		t.Fatalf("marshal fixture fields: %v", err)
	}
	provider := &fakeReadProvider{record: datasource.Record{
		Ref:    datasource.EntityRef{Type: datasource.EntityDeal, ID: dealID},
		Fields: fields,
	}}
	w := stageChangeNotify{ex: Executors{Provider: provider}}
	ev := workflow.Event{Entity: datasource.EntityRef{Type: datasource.EntityDeal, ID: dealID}}

	eff, err := w.Plan(context.Background(), ev)
	if err != nil {
		t.Fatalf("Plan err = %v, want nil", err)
	}
	if len(eff.Actions) != 1 {
		t.Fatalf("Plan emitted %d actions, want exactly 1", len(eff.Actions))
	}
	action := eff.Actions[0]
	if action.Kind != workflow.ActionNotify {
		t.Errorf("action.Kind = %q, want %q", action.Kind, workflow.ActionNotify)
	}
	if action.Target != ev.Entity {
		t.Errorf("action.Target = %v, want the moved deal %v", action.Target, ev.Entity)
	}
	var args notifyArgs
	if err := json.Unmarshal(action.Args, &args); err != nil {
		t.Fatalf("action.Args do not decode as notifyArgs: %v", err)
	}
	if args.Recipient != owner {
		t.Errorf("notify recipient = %v, want the deal's real owner %v", args.Recipient, owner)
	}
	if args.Subject == "" || args.Body == "" {
		t.Error("notify subject/body is empty — a human reading the inbox needs to know why they were notified")
	}
}

// TestStageChangeNotifyPlanDeclinesWhenTheDealHasNoOwner proves Plan
// declines to fire — rather than emitting a notify with a nil/zero
// recipient — when the moved deal has no assigned owner, and that the
// decline is a SKIP (declinedFiring), not a failure: an ownerless deal is
// a legitimate state runOne records as 'skipped', never a 'failed' run that
// would re-deliver forever and poison the event's sibling handlers.
func TestStageChangeNotifyPlanDeclinesWhenTheDealHasNoOwner(t *testing.T) {
	dealID := ids.NewV7()
	fields, err := json.Marshal(dealOwnerFields{OwnerID: nil})
	if err != nil {
		t.Fatalf("marshal fixture fields: %v", err)
	}
	provider := &fakeReadProvider{record: datasource.Record{Fields: fields}}
	w := stageChangeNotify{ex: Executors{Provider: provider}}
	ev := workflow.Event{Entity: datasource.EntityRef{Type: datasource.EntityDeal, ID: dealID}}

	_, err = w.Plan(context.Background(), ev)
	if !errors.Is(err, errDealHasNoOwner) {
		t.Fatalf("Plan err = %v, want errDealHasNoOwner", err)
	}
	var declined declinedFiring
	if !errors.As(err, &declined) {
		t.Fatalf("Plan err = %v, want a declinedFiring skip (not a hard failure)", err)
	}
}

// TestStageChangeNotifyPlanNeverSwallowsAReadFailure proves a failing
// Provider.Read surfaces honestly rather than Plan silently emitting an
// action built off zero-value fields.
func TestStageChangeNotifyPlanNeverSwallowsAReadFailure(t *testing.T) {
	readErr := errors.New("datasource: deal not found")
	provider := &fakeReadProvider{err: readErr}
	w := stageChangeNotify{ex: Executors{Provider: provider}}

	_, err := w.Plan(context.Background(), workflow.Event{Entity: datasource.EntityRef{Type: datasource.EntityDeal, ID: ids.NewV7()}})
	if !errors.Is(err, readErr) {
		t.Fatalf("Plan err = %v, want it to wrap %v", err, readErr)
	}
}

// TestStageChangeNotifyApplyDelegatesToApplyActions proves Apply threads
// the planned effect through the shared executor rather than reimplementing
// dispatch — the same shape stageChangeCreateTask's own Apply already proves.
func TestStageChangeNotifyApplyDelegatesToApplyActions(t *testing.T) {
	notifier := &fakeNotifier{}
	w := stageChangeNotify{ex: Executors{Notifier: notifier}}
	action := workflow.Action{Kind: workflow.ActionNotify, Args: json.RawMessage(`{"recipient":"` + ids.NewV7().String() + `"}`)}

	result, err := w.Apply(context.Background(), workflow.Event{}, workflow.Effect{Actions: []workflow.Action{action}}, nil)
	if err != nil {
		t.Fatalf("Apply err = %v, want nil", err)
	}
	if len(result.Applied) != 1 {
		t.Fatalf("Apply result.Applied = %v, want the one notify action", result.Applied)
	}
	if len(notifier.calls) != 1 {
		t.Fatalf("Notify called %d times, want exactly 1 (Apply must delegate through ApplyActions)", len(notifier.calls))
	}
}

// TestStageChangeNotifyIdempotencyKeyIsStablePerEvent proves the key is
// derived from the event id, not fabricated per call, so a redelivery of
// the same event claims the same run row instead of double-firing.
func TestStageChangeNotifyIdempotencyKeyIsStablePerEvent(t *testing.T) {
	evID := ids.NewV7()
	key1 := stageChangeNotify{}.IdempotencyKey(workflow.Event{ID: evID})
	key2 := stageChangeNotify{}.IdempotencyKey(workflow.Event{ID: evID})
	if key1 != key2 {
		t.Errorf("IdempotencyKey is not stable across calls for the same event: %q != %q", key1, key2)
	}
	other := stageChangeNotify{}.IdempotencyKey(workflow.Event{ID: ids.NewV7()})
	if key1 == other {
		t.Error("IdempotencyKey did not vary across distinct events — replays of different events would collide")
	}
}

// --- post_meeting_recap ---

// TestPostMeetingRecapSpecNamesTheCatalogKey pins the orphan-key trap: the
// engine dispatches instances[Spec().Name], so a Name drifting from the
// catalog entry this PR's later slice seeds under "post_meeting_recap"
// would make the handler a silent no-op forever.
func TestPostMeetingRecapSpecNamesTheCatalogKey(t *testing.T) {
	spec := postMeetingRecap{}.Spec()
	if spec.Name != "post_meeting_recap" {
		t.Errorf("Spec().Name = %q, want %q", spec.Name, "post_meeting_recap")
	}
	if spec.Trigger.EventType != eventActivityCaptured {
		t.Errorf("Spec().Trigger.EventType = %q, want %q", spec.Trigger.EventType, eventActivityCaptured)
	}
	if spec.Tier != mcp.TierAutoExecute {
		t.Errorf("Spec().Tier = %v, want TierAutoExecute", spec.Tier)
	}
}

// TestPostMeetingRecapMatchFiresOnACapturedMeeting is the guard against the
// silent-Match bug: Match must be TRUE for a captured meeting and FALSE for
// every other captured kind, read straight off the activity.captured payload.
func TestPostMeetingRecapMatchFiresOnACapturedMeeting(t *testing.T) {
	matched, err := postMeetingRecap{}.Match(context.Background(), workflow.Event{
		Payload: json.RawMessage(`{"kind":"meeting"}`),
	})
	if err != nil {
		t.Fatalf("Match(meeting) err = %v, want nil", err)
	}
	if !matched {
		t.Error("Match(kind=meeting) = false, want true — a captured meeting is the post-meeting signal")
	}
}

// TestPostMeetingRecapMatchIgnoresNonMeetingKinds proves Match declines
// every other captured kind and an empty payload — a recap is drafted only
// when a meeting was logged, never on a note/call/email capture.
func TestPostMeetingRecapMatchIgnoresNonMeetingKinds(t *testing.T) {
	cases := []json.RawMessage{
		json.RawMessage(`{"kind":"note"}`),
		json.RawMessage(`{"kind":"call"}`),
		json.RawMessage(`{"kind":"email"}`),
		json.RawMessage(`{"kind":"task"}`),
		json.RawMessage(`{}`),
		nil,
	}
	for _, payload := range cases {
		matched, err := postMeetingRecap{}.Match(context.Background(), workflow.Event{Payload: payload})
		if err != nil {
			t.Fatalf("Match(%s) err = %v, want nil", payload, err)
		}
		if matched {
			t.Errorf("Match(%s) = true, want false — only a captured meeting draws a recap", payload)
		}
	}
}

// TestPostMeetingRecapMatchNeverSwallowsAMalformedPayload proves an
// undecodable payload surfaces as an error rather than a silent false — a
// swallowed decode failure would hide a broken upstream emitter.
func TestPostMeetingRecapMatchNeverSwallowsAMalformedPayload(t *testing.T) {
	_, err := postMeetingRecap{}.Match(context.Background(), workflow.Event{
		Payload: json.RawMessage(`{"kind":`),
	})
	if err == nil {
		t.Fatal("Match err = nil for a malformed payload, want a decode error")
	}
}

// TestPostMeetingRecapPlanEmitsOneDraftEmailWithTheRecapIntent proves Plan
// emits exactly one draft_email anchored on the captured meeting, carrying
// the intent shape applyDraftEmail's decodeActionArgs reads.
func TestPostMeetingRecapPlanEmitsOneDraftEmailWithTheRecapIntent(t *testing.T) {
	meeting := datasource.EntityRef{Type: datasource.EntityActivity, ID: ids.NewV7()}
	eff, err := postMeetingRecap{}.Plan(context.Background(), workflow.Event{Entity: meeting})
	if err != nil {
		t.Fatalf("Plan err = %v, want nil", err)
	}
	if len(eff.Actions) != 1 {
		t.Fatalf("Plan emitted %d actions, want exactly 1", len(eff.Actions))
	}
	action := eff.Actions[0]
	if action.Kind != workflow.ActionDraftEmail {
		t.Errorf("action.Kind = %q, want %q", action.Kind, workflow.ActionDraftEmail)
	}
	if action.Target != meeting {
		t.Errorf("action.Target = %v, want the captured meeting %v", action.Target, meeting)
	}
	var args draftEmailArgs
	if err := json.Unmarshal(action.Args, &args); err != nil {
		t.Fatalf("action.Args do not decode as draftEmailArgs: %v", err)
	}
	if args.Intent != recapIntent {
		t.Errorf("draft_email intent = %q, want %q", args.Intent, recapIntent)
	}
}

// TestPostMeetingRecapApplyComposesTheDraftDurably proves Apply threads the
// planned draft_email through ApplyActions and the composed draft lands on
// the applied action — the same durable-artifact contract 11a's executor
// guarantees, exercised end-to-end through the handler.
func TestPostMeetingRecapApplyComposesTheDraftDurably(t *testing.T) {
	comms := &fakeComms{subject: "Recap: kickoff", body: "here's what we agreed"}
	w := postMeetingRecap{ex: Executors{Comms: comms}}
	meeting := datasource.EntityRef{Type: datasource.EntityActivity, ID: ids.NewV7()}

	eff, err := w.Plan(context.Background(), workflow.Event{Entity: meeting})
	if err != nil {
		t.Fatalf("Plan err = %v, want nil", err)
	}
	result, err := w.Apply(context.Background(), workflow.Event{Entity: meeting}, eff, nil)
	if err != nil {
		t.Fatalf("Apply err = %v, want nil", err)
	}
	if len(result.Applied) != 1 {
		t.Fatalf("Apply result.Applied = %v, want the one draft_email action", result.Applied)
	}
	if len(comms.calls) != 1 || comms.calls[0].anchor != meeting.ID || comms.calls[0].intent != recapIntent {
		t.Fatalf("DraftEmail called with %+v, want anchor=%v intent=%q", comms.calls, meeting.ID, recapIntent)
	}
	var rec struct {
		Subject string `json:"draft_subject"`
		Body    string `json:"draft_body"`
	}
	if err := json.Unmarshal(result.Applied[0].Args, &rec); err != nil {
		t.Fatalf("applied draft_email Args do not decode: %v", err)
	}
	if rec.Subject != "Recap: kickoff" || rec.Body != "here's what we agreed" {
		t.Errorf("applied draft = (subject=%q, body=%q), want the composed recap durably captured", rec.Subject, rec.Body)
	}
}

// TestPostMeetingRecapIdempotencyKeyIsStablePerEvent proves the key is
// derived from the event id, so a redelivered activity.captured claims the
// same run row instead of drafting a second recap.
func TestPostMeetingRecapIdempotencyKeyIsStablePerEvent(t *testing.T) {
	evID := ids.NewV7()
	key1 := postMeetingRecap{}.IdempotencyKey(workflow.Event{ID: evID})
	key2 := postMeetingRecap{}.IdempotencyKey(workflow.Event{ID: evID})
	if key1 != key2 {
		t.Errorf("IdempotencyKey is not stable across calls for the same event: %q != %q", key1, key2)
	}
	other := postMeetingRecap{}.IdempotencyKey(workflow.Event{ID: ids.NewV7()})
	if key1 == other {
		t.Error("IdempotencyKey did not vary across distinct events — replays of different events would collide")
	}
}

// TestStageChangeCreateTaskMatchOnlyFiresOnOpenMoves proves the follow-up
// cadence stops at a close: an open move (or a defensively-empty payload)
// fires, but a won/lost close does not. Guards the field-name contract
// with deals' deal.stage_changed payload (to_status, not to_semantic) —
// reading the wrong key made Match silently always-true, minting a
// follow-up task on closed deals.
func TestStageChangeCreateTaskMatchOnlyFiresOnOpenMoves(t *testing.T) {
	cases := []struct {
		name    string
		payload json.RawMessage
		want    bool
	}{
		{"absent payload defaults to open", nil, true},
		{"open move fires", json.RawMessage(`{"to_status":"open"}`), true},
		{"won close does not fire", json.RawMessage(`{"to_status":"won"}`), false},
		{"lost close does not fire", json.RawMessage(`{"to_status":"lost"}`), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			matched, err := stageChangeCreateTask{}.Match(context.Background(), workflow.Event{Payload: tc.payload})
			if err != nil {
				t.Fatalf("Match(%s) err = %v, want nil", tc.payload, err)
			}
			if matched != tc.want {
				t.Errorf("Match(%s) = %v, want %v", tc.payload, matched, tc.want)
			}
		})
	}
}
