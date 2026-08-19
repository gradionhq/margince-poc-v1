// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The confirm-first queue read from the side of whoever is waiting on it.
//
// The 🟡 loop had a hole where its other half should be: a staged call told the
// agent to wait for a person, and neither party could reach the queue from the
// conversation they were both already in. The agent could not see that a send
// was already staged — so it composed a second, worse copy of it — and the
// person who wanted to release it had to leave for the web app.
//
// The decision itself is unchanged and is not this file's: it demands the RBAC
// the staged effect needs, row-scope visibility of its target, the granting
// human's seat and a still-pending row, all in the approvals engine. What a
// passport adds is a bound on what it may RELEASE, which lives there too. Here
// there are four doors onto it.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// StagedApproval is one item of the queue in the shape a model reads: what was
// proposed, what it points at, and how long it stands.
//
// ProposedChange and Evidence are carried by read_approval and left empty by the
// listing. A queue is scanned to choose; the staged payload is a whole email or
// a whole field patch, and repeating every one of them in a listing spends a
// run's transcript on documents the caller has not asked to see yet.
type StagedApproval struct {
	StagedActionID ids.UUID `json:"staged_action_id"`
	// Kind is the staged action, and it is what says what approving DOES:
	// send_email releases a message, advance_deal moves a deal.
	Kind   string `json:"kind"`
	Status string `json:"status"`
	// Summary is the one line the inbox shows a human. Absent on a staging that
	// never wrote one, rather than filled with a restatement of the kind.
	Summary    string `json:"summary,omitempty"`
	ProposedBy string `json:"proposed_by"`
	// TargetType and TargetID are the record the staged change acts on, absent
	// together for a proposal that names none.
	//
	// The optional ids on this shape are POINTERS, which is what makes absent
	// absent: `omitempty` cannot drop a struct, so a value id would put
	// 00000000-0000-… on the wire for every proposal carrying none — and a
	// caller reading decided_by would be told that a nobody had answered it.
	TargetType string    `json:"target_type,omitempty"`
	TargetID   *ids.UUID `json:"target_id,omitempty"`
	// BundleID groups the proposals ONE act staged together. Present here so a
	// caller can decide them in one call instead of walking the group by hand.
	BundleID  *ids.UUID `json:"bundle_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	// ExpiresAt is when the proposal lapses. A lapsed item is not decidable and
	// reads as expired here before any sweep has run.
	ExpiresAt      *time.Time       `json:"expires_at,omitempty"`
	DecidedAt      *time.Time       `json:"decided_at,omitempty"`
	DecidedBy      *ids.UUID        `json:"decided_by,omitempty"`
	DiffHash       string           `json:"diff_hash,omitempty"`
	Evidence       []StagedEvidence `json:"evidence,omitempty"`
	ProposedChange json.RawMessage  `json:"proposed_change,omitempty"`
}

// StagedEvidence is one thing a proposal was read out of. It travels with the
// staged change rather than as prose about it: a claim a caller cannot check is
// what evidence exists to prevent.
type StagedEvidence struct {
	Snippet    string    `json:"evidence_snippet"`
	SourceType string    `json:"source_type,omitempty"`
	SourceID   *ids.UUID `json:"source_id,omitempty"`
}

// DecidedMember is one member of a bundle after a decision, with what that
// decision did to it. A member already decided, or lapsed, is REPORTED rather
// than silently skipped — a caller told only about the ones that moved would
// read the rest as having moved too.
type DecidedMember struct {
	StagedApproval
	Outcome string `json:"outcome"`
}

// ApprovalQuery is the listing's filter. Status defaults to pending at the
// seam, not here, so both doors onto the inbox default alike.
type ApprovalQuery struct {
	Status string
	Kind   string
	Limit  int
	Cursor string
}

// ApprovalPage is one page of the queue.
type ApprovalPage struct {
	Approvals  []StagedApproval `json:"approvals"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

// ApprovalInbox is the confirm-first queue as this surface needs it. Compose
// implements it over the approvals engine, which is where every authority
// question is answered — this package depends on seams, never on a sibling
// module.
type ApprovalInbox interface {
	ListApprovals(ctx context.Context, q ApprovalQuery) (ApprovalPage, error)
	ReadApproval(ctx context.Context, stagedActionID ids.UUID) (StagedApproval, error)
	DecideApproval(ctx context.Context, stagedActionID ids.UUID, approve bool, reason string) (StagedApproval, error)
	DecideApprovalBundle(ctx context.Context, bundleID ids.UUID, approve bool, reason string) ([]DecidedMember, error)
}

// RegisterApprovalTools wires the queue onto the tool surface. A nil seam
// registers nothing: a role with no approvals engine does not advertise a queue
// it cannot read.
func RegisterApprovalTools(r *Registry, inbox ApprovalInbox) {
	if inbox == nil {
		return
	}
	r.Register(listApprovalsTool{inbox: inbox})
	r.Register(readApprovalTool{inbox: inbox})
	r.Register(decideApprovalTool{inbox: inbox})
	r.Register(decideBundleTool{inbox: inbox})
}

// --- list_approvals (🟢 read) ---

type listApprovalsTool struct{ inbox ApprovalInbox }

type listApprovalsArgs struct {
	Status string `json:"status"`
	Kind   string `json:"kind"`
	Limit  int    `json:"limit"`
	Cursor string `json:"cursor"`
}

func (t listApprovalsTool) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "list_approvals", Title: "List what is waiting for a decision", Version: toolVersionV1,
		Description:   listApprovalsCopy.render(),
		RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
		OpenAPIOp: "listApprovals",
		InputSchema: schema(`{"type":"object","properties":{
			"status":{"type":"string","enum":["pending","approved","rejected"],"description":"Defaults to pending — what is still waiting."},
			"kind":{"type":"string","description":"One staged action, e.g. send_email or advance_deal."},
			"limit":{"type":"integer","minimum":1,"maximum":50},
			"cursor":{"type":"string","description":"next_cursor from a previous page."}},
			"additionalProperties":false}`),
		OutputSchema: schemaFor[ApprovalPage](),
	}
}

func (t listApprovalsTool) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args listApprovalsArgs
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	// Converted rather than copied field by field: the wire shape and the seam's
	// query are the same four members, and a conversion cannot silently drop the
	// fifth somebody adds to one of them.
	if err := knownStatus(args.Status); err != nil {
		return nil, err
	}
	page, err := t.inbox.ListApprovals(ctx, ApprovalQuery(args))
	if err != nil {
		return nil, err
	}
	if page.Approvals == nil {
		// An empty LIST, not a null. "Nothing is waiting" is the answer a caller
		// most often needs to state plainly, and a model handed null reads it as
		// "I could not find out" and hedges.
		page.Approvals = []StagedApproval{}
	}
	return json.Marshal(page)
}

// --- read_approval (🟢 read) ---

type readApprovalTool struct{ inbox ApprovalInbox }

type readApprovalArgs struct {
	StagedActionID ids.UUID `json:"staged_action_id"`
}

func (t readApprovalTool) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "read_approval", Title: "Read one staged action in full", Version: toolVersionV1,
		Description:   readApprovalCopy.render(),
		RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
		OpenAPIOp: "getApproval",
		InputSchema: schema(`{"type":"object","required":["staged_action_id"],"properties":{
			"staged_action_id":{"type":"string","format":"uuid","description":"From list_approvals."}},
			"additionalProperties":false}`),
		OutputSchema: schemaFor[StagedApproval](),
	}
}

func (t readApprovalTool) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args readApprovalArgs
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	staged, err := t.inbox.ReadApproval(ctx, args.StagedActionID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(staged)
}

// --- decide_approval (🟢 write) ---

// The decision is 🟢 and that is not an oversight. A confirm-first decide tool
// would stage an approval in order to approve an approval, and the regress has
// no fixed point — there is no human the second card could reach that the first
// one could not. What stands in place of a tier here is the credential: a
// passport decides on the authority of the person who minted it, spends the
// caps the release spends, and reaches nothing that person could not decide
// themselves in the app.
type decideApprovalTool struct{ inbox ApprovalInbox }

type decideApprovalArgs struct {
	StagedActionID ids.UUID `json:"staged_action_id"`
	Decision       string   `json:"decision"`
	Reason         string   `json:"reason"`
}

func (t decideApprovalTool) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "decide_approval", Title: "Approve or reject one staged action", Version: toolVersionV1,
		Description:   decideApprovalCopy.render(),
		RequiredScope: principal.ScopeWrite, Tier: mcp.TierAutoExecute,
		OpenAPIOp: "approveApproval/rejectApproval",
		InputSchema: schema(`{"type":"object","required":["staged_action_id","decision"],"properties":{
			"staged_action_id":{"type":"string","format":"uuid","description":"From list_approvals."},
			"decision":{"type":"string","enum":["approve","reject"]},
			"reason":{"type":"string","description":"Why, in the deciding person's words. Recorded with the decision."}},
			"additionalProperties":false}`),
		OutputSchema: schemaFor[StagedApproval](),
	}
}

func (t decideApprovalTool) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args decideApprovalArgs
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	approve, err := verdict(args.Decision)
	if err != nil {
		return nil, err
	}
	decided, err := t.inbox.DecideApproval(ctx, args.StagedActionID, approve, args.Reason)
	if err != nil {
		return nil, err
	}
	return json.Marshal(decided)
}

// --- decide_approval_bundle (🟢 write) ---

type decideBundleTool struct{ inbox ApprovalInbox }

type decideBundleArgs struct {
	BundleID ids.UUID `json:"bundle_id"`
	Decision string   `json:"decision"`
	Reason   string   `json:"reason"`
}

type decideBundleAnswer struct {
	Members []DecidedMember `json:"members"`
}

func (t decideBundleTool) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "decide_approval_bundle", Title: "Approve or reject one act's proposals together", Version: toolVersionV1,
		Description:   decideBundleCopy.render(),
		RequiredScope: principal.ScopeWrite, Tier: mcp.TierAutoExecute,
		OpenAPIOp: "approveApprovalBundle/rejectApprovalBundle",
		InputSchema: schema(`{"type":"object","required":["bundle_id","decision"],"properties":{
			"bundle_id":{"type":"string","format":"uuid"},
			"decision":{"type":"string","enum":["approve","reject"]},
			"reason":{"type":"string","description":"Why, in the deciding person's words. Recorded against every member."}},
			"additionalProperties":false}`),
		OutputSchema: schemaFor[decideBundleAnswer](),
	}
}

func (t decideBundleTool) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args decideBundleArgs
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	approve, err := verdict(args.Decision)
	if err != nil {
		return nil, err
	}
	members, err := t.inbox.DecideApprovalBundle(ctx, args.BundleID, approve, args.Reason)
	if err != nil {
		return nil, err
	}
	if members == nil {
		members = []DecidedMember{}
	}
	return json.Marshal(decideBundleAnswer{Members: members})
}

// knownStatus refuses a filter this queue has no such thing as.
//
// An unknown status is not an empty queue, and that is the whole reason it
// cannot be passed through: the answer to a filter nobody serves is
// `{"approvals":[]}`, which this tool's own handler tells a caller to read as
// "nothing is waiting". A model asking for `expired` — a word the surface hands
// back on a lapsed item — would be told the queue is clear.
//
// Checked here rather than trusted to the schema's enum: that enum is what a
// CLIENT validates against, and this surface is reachable by callers that never
// read it.
func knownStatus(status string) error {
	switch status {
	case "", "pending", "approved", "rejected":
		return nil
	default:
		return &BadArgsError{Cause: fmt.Errorf(
			"`status` is %q; it is pending, approved or rejected. A proposal that lapsed reads as "+
				"expired among the pending ones — it is not a filter of its own", status)}
	}
}

// verdict reads the one argument that decides which way this call goes.
//
// Checked here rather than left to the schema's enum: the enum is what a client
// enforces before the call, and this surface is reachable by callers that never
// read it. A verdict defaulting to either answer would decide somebody's queue
// on a typo.
func verdict(decision string) (bool, error) {
	switch decision {
	case "approve":
		return true, nil
	case "reject":
		return false, nil
	default:
		return false, &BadArgsError{Cause: fmt.Errorf("`decision` is %q; it is `approve` or `reject`", decision)}
	}
}
