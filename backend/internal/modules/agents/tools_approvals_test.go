// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The four queue doors are thin over their seam, so what is pinned here is
// everything the seam cannot answer for them: what each one costs a caller, what
// a verdict outside the vocabulary does, and the two shapes a model must never
// be handed — a null where a list was promised, and a listing carrying every
// staged document.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// fakeInbox records what the door asked for and answers what the test set.
type fakeInbox struct {
	query   ApprovalQuery
	decided ids.UUID
	approve bool
	reason  string
	page    ApprovalPage
	one     StagedApproval
	members []DecidedMember
	err     error
}

func (f *fakeInbox) ListApprovals(_ context.Context, q ApprovalQuery) (ApprovalPage, error) {
	f.query = q
	return f.page, f.err
}

func (f *fakeInbox) ReadApproval(_ context.Context, id ids.UUID) (StagedApproval, error) {
	f.decided = id
	return f.one, f.err
}

func (f *fakeInbox) DecideApproval(_ context.Context, id ids.UUID, approve bool, reason string) (StagedApproval, error) {
	f.decided, f.approve, f.reason = id, approve, reason
	return f.one, f.err
}

func (f *fakeInbox) DecideApprovalBundle(_ context.Context, id ids.UUID, approve bool, reason string) ([]DecidedMember, error) {
	f.decided, f.approve, f.reason = id, approve, reason
	return f.members, f.err
}

// Reading the queue costs a read; answering it costs a write, and the answer
// runs when it is given rather than staging a second card to release the first.
func TestTheQueueDoorsCostWhatTheyDo(t *testing.T) {
	cases := []struct {
		tool  mcp.Tool
		scope principal.Scope
	}{
		{listApprovalsTool{}, principal.ScopeRead},
		{readApprovalTool{}, principal.ScopeRead},
		{decideApprovalTool{}, principal.ScopeWrite},
		{decideBundleTool{}, principal.ScopeWrite},
	}
	for _, tc := range cases {
		spec := tc.tool.Spec()
		t.Run(spec.Name, func(t *testing.T) {
			if spec.RequiredScope != tc.scope {
				t.Errorf("RequiredScope = %v, want %v", spec.RequiredScope, tc.scope)
			}
			// Confirm-first here would stage an approval in order to approve an
			// approval, and the regress has no fixed point: there is no human
			// the second card could reach that the first could not.
			if spec.Tier != mcp.TierAutoExecute {
				t.Errorf("tier = %v, want auto-execute", spec.Tier)
			}
			if spec.Egress {
				t.Error("marked as leaving the workspace; the release does that, not the decision")
			}
		})
	}
}

func TestAnAbsentQueueRegistersNoTool(t *testing.T) {
	r := NewRegistry(nil, nil)
	RegisterApprovalTools(r, nil)
	for _, name := range []string{"list_approvals", "read_approval", "decide_approval", "decide_approval_bundle"} {
		if _, found := r.Spec(name); found {
			t.Errorf("%s registered with no queue behind it", name)
		}
	}
}

// "Nothing is waiting" is the answer this tool gives most often, and it has to
// be sayable: a model handed null reads it as "I could not find out" and hedges
// where it should have said the queue is empty.
func TestAnEmptyQueueAnswersAnEmptyListNotNull(t *testing.T) {
	inbox := &fakeInbox{}
	out, err := listApprovalsTool{inbox: inbox}.Handle(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !strings.Contains(string(out), `"approvals":[]`) {
		t.Errorf("answer = %s, want an empty approvals list", out)
	}
	if inbox.query.Status != "" {
		t.Errorf("the door invented a status filter (%q); defaulting is the seam's", inbox.query.Status)
	}
}

// A bundle decision that moved nothing still answers with a list: the caller
// asked what happened to every member, and null is not "none of them".
func TestABundleDecisionAnswersAListEvenWhenNoMemberMoved(t *testing.T) {
	out, err := decideBundleTool{inbox: &fakeInbox{}}.Handle(context.Background(),
		json.RawMessage(`{"bundle_id":"019fdba6-779b-72ce-8ace-7ddb290cd7cc","decision":"reject"}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !strings.Contains(string(out), `"members":[]`) {
		t.Errorf("answer = %s, want an empty members list", out)
	}
}

// The verdict decides somebody's queue, so it is read strictly: a word outside
// the vocabulary is refused rather than folded onto either answer, and the
// refusal names what the two are.
func TestAVerdictOutsideTheVocabularyDecidesNothing(t *testing.T) {
	inbox := &fakeInbox{}
	_, err := decideApprovalTool{inbox: inbox}.Handle(context.Background(),
		json.RawMessage(`{"staged_action_id":"019fdba6-779b-72ce-8ace-7ddb290cd7cc","decision":"yes"}`))
	var bad *BadArgsError
	if !errors.As(err, &bad) {
		t.Fatalf("err = %v, want a BadArgsError", err)
	}
	for _, want := range []string{"approve", "reject"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal never names %q: %v", want, err)
		}
	}
	if !inbox.decided.IsZero() {
		t.Error("an unreadable verdict reached the queue")
	}
}

// Both verdicts reach the seam as themselves. A test that only proved approve
// would pass over a door that approved everything.
func TestEachVerdictReachesTheQueueAsItself(t *testing.T) {
	for _, tc := range []struct {
		decision string
		approve  bool
	}{{"approve", true}, {"reject", false}} {
		t.Run(tc.decision, func(t *testing.T) {
			inbox := &fakeInbox{}
			_, err := decideApprovalTool{inbox: inbox}.Handle(context.Background(), json.RawMessage(
				`{"staged_action_id":"019fdba6-779b-72ce-8ace-7ddb290cd7cc","decision":"`+tc.decision+`","reason":"the customer asked"}`))
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if inbox.approve != tc.approve {
				t.Errorf("approve = %v, want %v", inbox.approve, tc.approve)
			}
			if inbox.reason != "the customer asked" {
				t.Errorf("reason = %q, want the caller's own words", inbox.reason)
			}
		})
	}
}

// An argument this surface does not have is refused rather than dropped: a
// caller that thought it was filtering, and was not, reads the whole queue as
// the filtered one.
func TestAnUnknownArgumentIsRefusedRatherThanIgnored(t *testing.T) {
	_, err := listApprovalsTool{inbox: &fakeInbox{}}.Handle(context.Background(),
		json.RawMessage(`{"target_entity_type":"deal"}`))
	if err == nil {
		t.Fatal("an argument the tool does not have was accepted")
	}
	if !strings.Contains(err.Error(), "target_entity_type") {
		t.Errorf("the refusal never names the argument it refused: %v", err)
	}
}
