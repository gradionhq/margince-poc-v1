// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The confirm-first queue, injected onto the tool surface.
//
// It is a SECOND door onto the approvals engine and adds no authority of its
// own: every call below is the call the HTTP handlers make, through the same
// contract shape, so the two doors cannot describe one approval differently or
// admit a decision the other would refuse. What a passport may see and release
// is decided in the engine, where it is decided for everyone.
//
// It is a separate adapter from approvalsAdapter (registry.go) because it is a
// separate relationship: that one is the surface's STAGING dependency, which
// the registry cannot be built without, and this one is the queue read back,
// which a role composing no approvals engine simply does not offer.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// approvalInbox is the tool surface's read/decide door onto the queue.
type approvalInbox struct{ svc *approvals.Service }

// approvalQueue builds that door, and refuses at BOOT an engine that could
// decide a kind it cannot release.
//
// The failure it prevents has no symptom at the call: approving a held draft on
// an engine with no executor for it commits the decision, answers the caller
// success, and leaves the message held forever — the card reads approved and
// nothing else ever happens. The send-dependent releases are registered late,
// from the assembled send path, which is precisely the registration a second
// door onto the same engine forgets (registry.go builds one; applySendPath
// gives the inbox handlers the other).
//
// A panic rather than a refusal at call time, for the reason Registry.Register
// panics: this is composition wiring, and a deployment that got it wrong should
// not start.
func approvalQueue(svc *approvals.Service) approvalInbox {
	releasable := svc.EffectKinds()
	for kind := range lateApprovalEffects {
		if !slices.Contains(releasable, kind) {
			//craft:ignore panic-in-domain composition-time wiring assertion — fires only while cmd wiring runs, never on a request path
			panic(fmt.Sprintf("compose: the tool surface would decide %s on an engine that cannot release it — "+
				"a decision would commit, answer success, and perform nothing", kind))
		}
	}
	return approvalInbox{svc: svc}
}

// ListApprovals answers the caller's own decidable queue.
//
// It defaults to PENDING where the HTTP door defaults to everything, and the
// difference is the question each is asked. A screen shows a queue somebody is
// looking at and offers them the decided tabs; a tool is asked "what is waiting"
// by a caller that will otherwise propose again what already stands staged. A
// caller that wants the decided ones names them.
func (i approvalInbox) ListApprovals(ctx context.Context, q agents.ApprovalQuery) (agents.ApprovalPage, error) {
	status := q.Status
	if status == "" {
		status = approvals.StatusPending
	}
	in := approvals.ListInput{Status: &status, Limit: q.Limit, Cursor: q.Cursor}
	if q.Kind != "" {
		in.Kind = &q.Kind
	}
	rows, page, err := i.svc.ListWire(ctx, in)
	if err != nil {
		return agents.ApprovalPage{}, err
	}
	out := agents.ApprovalPage{
		Approvals:  make([]agents.StagedApproval, 0, len(rows)),
		NextCursor: page.NextCursor,
	}
	for _, a := range rows {
		// Without the staged payload: a listing is scanned to choose, and the
		// payloads are whole emails. read_approval is the door onto one.
		out.Approvals = append(out.Approvals, stagedActionFrom(a, false))
	}
	return out, nil
}

// ReadApproval answers one staged action in full.
func (i approvalInbox) ReadApproval(ctx context.Context, stagedActionID ids.UUID) (agents.StagedApproval, error) {
	a, err := i.svc.GetWire(ctx, ids.From[ids.ApprovalKind](stagedActionID))
	if err != nil {
		return agents.StagedApproval{}, err
	}
	return stagedActionFrom(a, true), nil
}

// DecideApproval carries one person's verdict to the engine.
func (i approvalInbox) DecideApproval(ctx context.Context, stagedActionID ids.UUID, approve bool, reason string) (agents.StagedApproval, error) {
	a, err := i.svc.DecideWire(ctx, ids.From[ids.ApprovalKind](stagedActionID), approve, decisionReason(reason))
	if err != nil {
		return agents.StagedApproval{}, err
	}
	return stagedActionFrom(a, true), nil
}

// DecideApprovalBundle carries the verdict to every member of one act.
func (i approvalInbox) DecideApprovalBundle(ctx context.Context, bundleID ids.UUID, approve bool, reason string) ([]agents.DecidedMember, error) {
	members, err := i.svc.DecideBundleWire(ctx, bundleID, approve, decisionReason(reason))
	if err != nil {
		return nil, err
	}
	out := make([]agents.DecidedMember, 0, len(members))
	for _, member := range members {
		out = append(out, agents.DecidedMember{
			StagedApproval: stagedActionFrom(member.Approval, true),
			Outcome:        string(member.Outcome),
		})
	}
	return out, nil
}

// decisionReason maps an omitted reason onto the absent one the engine records.
// An empty string is a reason nobody gave, and storing it as one would put a
// blank quotation in an audit row that reads as if somebody wrote it.
func decisionReason(reason string) *string {
	if reason == "" {
		return nil
	}
	return &reason
}

// stagedActionFrom maps the contract shape onto the tool surface's, which calls
// a pending proposal a staged action.
//
// withPayload carries the staged change and the evidence it was formed on. The
// listing leaves both behind rather than repeating every proposal's whole
// document into a transcript later prompts of the same run carry.
func stagedActionFrom(a crmcontracts.Approval, withPayload bool) agents.StagedApproval {
	out := agents.StagedApproval{
		StagedActionID: ids.UUID(a.Id),
		Kind:           a.Kind,
		Status:         string(a.Status),
		ProposedBy:     a.ProposedBy,
		CreatedAt:      a.CreatedAt,
		ExpiresAt:      a.ExpiresAt,
		DecidedAt:      a.DecidedAt,
	}
	if a.Summary != nil {
		out.Summary = *a.Summary
	}
	if a.DiffHash != nil {
		out.DiffHash = *a.DiffHash
	}
	if a.TargetEntityType != nil {
		out.TargetType = *a.TargetEntityType
	}
	out.TargetID = optionalID(a.TargetEntityId)
	out.BundleID = optionalID(a.BundleId)
	out.DecidedBy = optionalID(a.DecidedBy)
	if !withPayload {
		return out
	}
	out.ProposedChange = stagedPayload(a.ProposedChange)
	out.Evidence = stagedEvidence(a.Evidence)
	return out
}

// stagedPayload re-encodes the staged change for a surface that carries it as a
// document rather than as a decoded map.
//
// A payload that cannot be re-encoded is DROPPED rather than replaced with an
// error string: the rest of the answer — what kind of action this is, what it
// points at, when it lapses — is still true and still worth answering, and a
// caller reading no proposed_change asks to see it in the app, which is the
// right move for a proposal nothing here could render.
func stagedPayload(change *map[string]any) json.RawMessage {
	if change == nil {
		return nil
	}
	encoded, err := json.Marshal(*change)
	if err != nil {
		return nil
	}
	return encoded
}

// stagedEvidence carries what each claim was read out of. Evidence is the half
// that lets a person check a proposal instead of taking its word.
func stagedEvidence(evidence *[]crmcontracts.ApprovalEvidence) []agents.StagedEvidence {
	if evidence == nil {
		return nil
	}
	out := make([]agents.StagedEvidence, 0, len(*evidence))
	for _, e := range *evidence {
		item := agents.StagedEvidence{Snippet: e.EvidenceSnippet, SourceID: optionalID(e.SourceId)}
		if e.SourceType != nil {
			item.SourceType = string(*e.SourceType)
		}
		out = append(out, item)
	}
	return out
}

// optionalID carries an absent id through as absent. The contract's optional
// uuids are pointers and so are the surface's, for the same reason: a member
// dropped from the answer says "there is none", and a zero uuid printed in its
// place names a record nobody can look up — read_approval's decided_by is the
// one that would have been read as "somebody answered this".
func optionalID(id *openapi_types.UUID) *ids.UUID {
	if id == nil {
		return nil
	}
	out := ids.UUID(*id)
	return &out
}
