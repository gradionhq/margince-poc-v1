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
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// fakeLists is a DB-free stand-in for the Lists seam: records every
// AddMember call so a test can assert add_to_list actually reaches it.
type fakeLists struct {
	err   error
	calls []struct {
		listID     ids.ListID
		entityType string
		entityID   ids.UUID
	}
}

func (f *fakeLists) AddMember(_ context.Context, listID ids.ListID, entityType string, entityID ids.UUID) error {
	f.calls = append(f.calls, struct {
		listID     ids.ListID
		entityType string
		entityID   ids.UUID
	}{listID, entityType, entityID})
	return f.err
}

// fakeComms is a DB-free stand-in for the Comms seam: records every
// DraftEmail call. The seam declares no send verb at all, so "draft_email
// never sends" is a structural property of this interface, not merely a
// behavioral one — there is nothing here a test could call to send.
type fakeComms struct {
	subject, body string
	err           error
	calls         []struct {
		anchor ids.UUID
		intent string
	}
}

func (f *fakeComms) DraftEmail(_ context.Context, anchor ids.UUID, intent string) (string, string, error) {
	f.calls = append(f.calls, struct {
		anchor ids.UUID
		intent string
	}{anchor, intent})
	return f.subject, f.body, f.err
}

// fakeNotifier is a DB-free stand-in for the Notifier seam: this repo
// wires none in compose, but the seam must still work once something
// does, so its wired path gets its own test.
type fakeNotifier struct {
	err   error
	calls []struct {
		recipient     ids.UUID
		subject, body string
	}
}

func (f *fakeNotifier) Notify(_ context.Context, recipient ids.UUID, subject, body string) error {
	f.calls = append(f.calls, struct {
		recipient     ids.UUID
		subject, body string
	}{recipient, subject, body})
	return f.err
}

// fakeUpdateProvider implements only the write path
// datasource.SystemOfRecordProvider tests here ever reach: Update.
// Every other method panics — a test that reaches one is exercising the
// wrong branch, and a panic says so louder than a wrong zero value would.
type fakeUpdateProvider struct {
	err   error
	calls []datasource.UpdateInput
}

func (p *fakeUpdateProvider) Update(_ context.Context, in datasource.UpdateInput) (datasource.EntityRef, error) {
	p.calls = append(p.calls, in)
	return in.Ref, p.err
}
func (p *fakeUpdateProvider) Read(context.Context, datasource.EntityRef) (datasource.Record, error) {
	panic("fakeUpdateProvider: Read not stubbed for this test")
}
func (p *fakeUpdateProvider) Search(context.Context, datasource.SearchQuery) (datasource.SearchResult, error) {
	panic("fakeUpdateProvider: Search not stubbed for this test")
}
func (p *fakeUpdateProvider) ListObjects(context.Context) ([]datasource.ObjectDef, error) {
	panic("fakeUpdateProvider: ListObjects not stubbed for this test")
}
func (p *fakeUpdateProvider) ListFields(context.Context, datasource.EntityType) ([]datasource.FieldDef, error) {
	panic("fakeUpdateProvider: ListFields not stubbed for this test")
}
func (p *fakeUpdateProvider) RunReport(context.Context, datasource.ReportPlan) (datasource.ReportResult, error) {
	panic("fakeUpdateProvider: RunReport not stubbed for this test")
}
func (p *fakeUpdateProvider) StageSemantic(context.Context, ids.UUID) (string, ids.UUID, error) {
	panic("fakeUpdateProvider: StageSemantic not stubbed for this test")
}
func (p *fakeUpdateProvider) Create(context.Context, datasource.CreateInput) (datasource.EntityRef, error) {
	panic("fakeUpdateProvider: Create not stubbed for this test")
}
func (p *fakeUpdateProvider) AdvanceDeal(context.Context, datasource.AdvanceDealInput) (datasource.EntityRef, error) {
	panic("fakeUpdateProvider: AdvanceDeal not stubbed for this test")
}
func (p *fakeUpdateProvider) Archive(context.Context, datasource.EntityRef) (datasource.EntityRef, error) {
	panic("fakeUpdateProvider: Archive not stubbed for this test")
}
func (p *fakeUpdateProvider) Merge(context.Context, datasource.MergeInput) (datasource.EntityRef, error) {
	panic("fakeUpdateProvider: Merge not stubbed for this test")
}
func (p *fakeUpdateProvider) PromoteLead(context.Context, ids.UUID, string, *string) (datasource.EntityRef, bool, error) {
	panic("fakeUpdateProvider: PromoteLead not stubbed for this test")
}
func (p *fakeUpdateProvider) Freshness(context.Context, datasource.EntityRef) (datasource.FreshnessInfo, error) {
	panic("fakeUpdateProvider: Freshness not stubbed for this test")
}

var _ datasource.SystemOfRecordProvider = (*fakeUpdateProvider)(nil)

// --- notify ---

func TestApplyActionsNotifyWithNoTransportReturnsTheHonestSentinel(t *testing.T) {
	action := workflow.Action{Kind: workflow.ActionNotify, Args: json.RawMessage(`{}`)}

	_, err := ApplyActions(context.Background(), Executors{}, workflow.Effect{Actions: []workflow.Action{action}})

	if !errors.Is(err, ErrNoNotificationTransport) {
		t.Fatalf("ApplyActions err = %v, want ErrNoNotificationTransport (no Notifier wired)", err)
	}
}

func TestApplyActionsNotifyWithAWiredTransportDelivers(t *testing.T) {
	recipient := ids.NewV7()
	notifier := &fakeNotifier{}
	action := workflow.Action{
		Kind: workflow.ActionNotify,
		Args: json.RawMessage(`{"recipient":"` + recipient.String() + `","subject":"Heads up","body":"a deal moved"}`),
	}

	applied, err := ApplyActions(context.Background(), Executors{Notifier: notifier}, workflow.Effect{Actions: []workflow.Action{action}})
	if err != nil {
		t.Fatalf("ApplyActions err = %v, want nil", err)
	}
	if len(applied) != 1 {
		t.Fatalf("applied = %v, want the one notify action", applied)
	}
	if len(notifier.calls) != 1 {
		t.Fatalf("Notify called %d times, want exactly 1", len(notifier.calls))
	}
	got := notifier.calls[0]
	if got.recipient != recipient || got.subject != "Heads up" || got.body != "a deal moved" {
		t.Errorf("Notify called with %+v, want recipient=%v subject=%q body=%q", got, recipient, "Heads up", "a deal moved")
	}
}

func TestApplyActionsNotifyNeverSwallowsADeliveryFailure(t *testing.T) {
	notifyErr := errors.New("smtp: connection refused")
	notifier := &fakeNotifier{err: notifyErr}
	action := workflow.Action{Kind: workflow.ActionNotify, Args: json.RawMessage(`{}`)}

	_, err := ApplyActions(context.Background(), Executors{Notifier: notifier}, workflow.Effect{Actions: []workflow.Action{action}})

	if !errors.Is(err, notifyErr) {
		t.Fatalf("ApplyActions err = %v, want it to wrap %v", err, notifyErr)
	}
}

// --- add_to_list ---

func TestApplyActionsAddToListCallsListsAddMember(t *testing.T) {
	lists := &fakeLists{}
	listID := ids.New[ids.ListKind]()
	target := datasource.EntityRef{Type: datasource.EntityPerson, ID: ids.NewV7()}
	action := workflow.Action{
		Kind:   workflow.ActionAddToList,
		Target: target,
		Args:   json.RawMessage(`{"list_id":"` + listID.String() + `"}`),
	}

	applied, err := ApplyActions(context.Background(), Executors{Lists: lists}, workflow.Effect{Actions: []workflow.Action{action}})
	if err != nil {
		t.Fatalf("ApplyActions err = %v, want nil", err)
	}
	if len(applied) != 1 {
		t.Fatalf("applied = %v, want the one add_to_list action", applied)
	}
	if len(lists.calls) != 1 {
		t.Fatalf("AddMember called %d times, want exactly 1", len(lists.calls))
	}
	got := lists.calls[0]
	if got.listID.String() != listID.String() || got.entityType != string(target.Type) || got.entityID != target.ID {
		t.Errorf("AddMember called with %+v, want list=%v type=%q id=%v", got, listID, target.Type, target.ID)
	}
}

func TestApplyActionsAddToListNeverSwallowsAnAddMemberFailure(t *testing.T) {
	addErr := errors.New("collections: list is archived")
	lists := &fakeLists{err: addErr}
	action := workflow.Action{
		Kind:   workflow.ActionAddToList,
		Target: datasource.EntityRef{Type: datasource.EntityPerson, ID: ids.NewV7()},
		Args:   json.RawMessage(`{"list_id":"` + ids.New[ids.ListKind]().String() + `"}`),
	}

	_, err := ApplyActions(context.Background(), Executors{Lists: lists}, workflow.Effect{Actions: []workflow.Action{action}})

	if !errors.Is(err, addErr) {
		t.Fatalf("ApplyActions err = %v, want it to wrap %v", err, addErr)
	}
}

// --- draft_email ---

func TestApplyActionsDraftEmailComputesADraftAndNeverSends(t *testing.T) {
	comms := &fakeComms{subject: "Re: hello", body: "following up"}
	anchor := ids.NewV7()
	action := workflow.Action{
		Kind:   workflow.ActionDraftEmail,
		Target: datasource.EntityRef{Type: datasource.EntityActivity, ID: anchor},
		Args:   json.RawMessage(`{"intent":"nudge toward a decision"}`),
	}

	applied, err := ApplyActions(context.Background(), Executors{Comms: comms}, workflow.Effect{Actions: []workflow.Action{action}})
	if err != nil {
		t.Fatalf("ApplyActions err = %v, want nil", err)
	}
	if len(applied) != 1 {
		t.Fatalf("applied = %v, want the one draft_email action", applied)
	}
	if len(comms.calls) != 1 {
		t.Fatalf("DraftEmail called %d times, want exactly 1", len(comms.calls))
	}
	got := comms.calls[0]
	if got.anchor != anchor || got.intent != "nudge toward a decision" {
		t.Errorf("DraftEmail called with %+v, want anchor=%v intent=%q", got, anchor, "nudge toward a decision")
	}
}

func TestApplyActionsDraftEmailNeverSwallowsADraftFailure(t *testing.T) {
	draftErr := errors.New("activities: anchor not found")
	comms := &fakeComms{err: draftErr}
	action := workflow.Action{
		Kind:   workflow.ActionDraftEmail,
		Target: datasource.EntityRef{Type: datasource.EntityActivity, ID: ids.NewV7()},
	}

	_, err := ApplyActions(context.Background(), Executors{Comms: comms}, workflow.Effect{Actions: []workflow.Action{action}})

	if !errors.Is(err, draftErr) {
		t.Fatalf("ApplyActions err = %v, want it to wrap %v", err, draftErr)
	}
}

// --- assign_owner's dynamic tier ---

// TestApplyAssignOwnerSingleEntityWritesThroughProviderUpdate proves the
// 🟢 branch: the honest single-entity scope every real firing carries
// today writes straight through, never touching Approvals.
func TestApplyAssignOwnerSingleEntityWritesThroughProviderUpdate(t *testing.T) {
	provider := &fakeUpdateProvider{}
	target := datasource.EntityRef{Type: datasource.EntityDeal, ID: ids.NewV7()}
	action := workflow.Action{Kind: workflow.ActionAssignOwner, Target: target, Args: json.RawMessage(`{"owner_id":"` + ids.NewV7().String() + `"}`)}

	err := applyAssignOwner(context.Background(), Executors{Provider: provider}, action, AssignOwnerScope{Bulk: false})

	if err != nil {
		t.Fatalf("applyAssignOwner err = %v, want nil", err)
	}
	if len(provider.calls) != 1 {
		t.Fatalf("provider.Update called %d times, want exactly 1", len(provider.calls))
	}
	if provider.calls[0].Ref != target {
		t.Errorf("provider.Update Ref = %v, want %v", provider.calls[0].Ref, target)
	}
}

// TestApplyAssignOwnerAtScaleStagesInsteadOfWriting proves the 🟡
// branch against a SYNTHETIC scaled scope (AUTO-T07): no shipped
// automation produces Bulk == true today (AssignOwnerScope's doc), so
// this is the resolver's escalation path exercised the only honest way
// available — a fake provider that panics on Update proves the write
// side is never reached, exactly like the 🟡 kinds' own staging tests.
func TestApplyAssignOwnerAtScaleStagesInsteadOfWriting(t *testing.T) {
	fake := &fakeApprovals{id: ids.New[ids.ApprovalKind]()}
	target := datasource.EntityRef{Type: datasource.EntityDeal, ID: ids.NewV7()}
	action := workflow.Action{Kind: workflow.ActionAssignOwner, Target: target, Args: json.RawMessage(`{"owner_id":"` + ids.NewV7().String() + `"}`)}

	err := applyAssignOwner(context.Background(), Executors{Approvals: fake}, action, AssignOwnerScope{Bulk: true})

	var staged *workflow.StagedApprovalError
	if !errors.As(err, &staged) {
		t.Fatalf("applyAssignOwner err = %v, want a *workflow.StagedApprovalError", err)
	}
	if staged.ApprovalID != fake.id {
		t.Errorf("StagedApprovalError.ApprovalID = %v, want %v", staged.ApprovalID, fake.id)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("Stage called %d times, want exactly 1", len(fake.calls))
	}
}
