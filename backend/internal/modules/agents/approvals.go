// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The 🟡 loop from the tool surface's side. A refused confirm-first call
// is STAGED (approval.requested) so the human sees exactly what the agent
// wanted; after the human approves, the agent re-invokes the IDENTICAL
// call plus `approval_id`, and redemption checks tool + diff_hash +
// passport + target version before consuming the staging once. The agent
// never receives a bearer secret — the approval row itself is the
// authority object, and it only fits the caller it was staged by.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/diffhash"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

var errInvalidApprovalID = errors.New("approval_id must be a UUID string")

// Approvals is the staging/redemption dependency, implemented by the
// approvals module and injected at the composition root (this package
// depends on seams, never on sibling modules).
type Approvals interface {
	Stage(ctx context.Context, in StageRequest) (ids.ApprovalID, error)
	// Redeem answers the version the approval was pinned to, so a transport
	// that forwards the authorized call can bind its own write to it. pinned
	// is false when the approval carried none — a create, or a target type
	// with no version column — and version is meaningless then.
	Redeem(ctx context.Context, approvalID ids.ApprovalID, tool, diffHash string) (version int64, pinned bool, err error)
}

// StageRequest carries what the inbox shows the human and what redemption
// later re-checks.
type StageRequest struct {
	Tool           string
	ProposedChange json.RawMessage
	DiffHash       string
	TargetType     string
	TargetID       ids.UUID
	TargetVersion  *int64
	Summary        string
}

// StageInfo is what a 🟡-capable tool contributes to its own staging: the
// row the effect targets (for the version re-check) and the one-liner the
// inbox displays.
type StageInfo struct {
	TargetType    string
	TargetID      ids.UUID
	TargetVersion *int64
	Summary       string
}

// stageableTool is implemented by tools whose refused 🟡 calls should
// land in the inbox rather than dead-end.
type stageableTool interface {
	StageInfo(ctx context.Context, args json.RawMessage) (StageInfo, error)
}

// approvalRedeemedKey marks a context whose call already consumed a
// redeemed approval at the dispatch layer: the handler may perform the
// exact effect the human released without re-asking the precedence
// question (the diff_hash binding guarantees the call IS that effect).
type approvalRedeemedKey struct{}

// withApprovalRedeemed marks ctx as carrying a released approval. It stays
// unexported on purpose: everything downstream — including the seam's
// external-egress backstop — treats the marker as proof that a human released
// exactly this call, so an exported setter would let any caller forge that
// proof, and a doc comment asking them not to is not enforcement. The only way
// to obtain a marked context is RedeemAndMark, which cannot mark without
// redeeming first.
func withApprovalRedeemed(ctx context.Context) context.Context {
	return context.WithValue(ctx, approvalRedeemedKey{}, true)
}

// RedeemAndMark consumes an approval and returns a context marked as released,
// binding the two together so neither can happen without the other. The
// version pin travels back for a transport that must forward it as its own
// precondition; pinned is false when the approval carried none.
//
// This is the ONLY way to obtain a released context. There are two dispatch
// layers — the MCP registry and the REST agent gate — and both must mark what
// they redeem, or the gate refuses the very write the approval was granted for;
// making the marking a consequence of redeeming is what keeps that true without
// trusting either caller to remember.
func RedeemAndMark(ctx context.Context, approvals Approvals, approvalID ids.ApprovalID, tool, diffHash string,
) (marked context.Context, version int64, pinned bool, err error) {
	version, pinned, err = approvals.Redeem(ctx, approvalID, tool, diffHash)
	if err != nil {
		return ctx, 0, false, err
	}
	return withApprovalRedeemed(ctx), version, pinned, nil
}

// ApprovalRedeemed reports whether this call already consumed a redeemed
// approval. Exported because the composition layer needs the same answer at
// the datasource seam, where a write into an external system of record is
// refused unless a human released this exact call. Read-only by design: the
// marker is settable only here, immediately after a successful Redeem.
func ApprovalRedeemed(ctx context.Context) bool {
	redeemed, ok := ctx.Value(approvalRedeemedKey{}).(bool)
	return ok && redeemed
}

// refuseStagingElsewhere refuses to stage a change whose target's authority
// lives in another system of record.
//
// A staged approval is an authority object a human must be able to both SEE
// and RELEASE, and for such a target neither holds: the decidability probe and
// the redemption version pin both read our own tables, which the record has no
// row in. Staging anyway puts a decision in an inbox that can never take
// effect and cannot be withdrawn — the zombie authority object the REST gate's
// own decision-grant check refuses to mint. Answering the declared
// unsupported-by-SoR sentinel instead makes the 🟡 tools agree with the
// datasource seam, which refuses the same write for the same reason. That
// agreement is only true while EVERY stageable tool calls this, so the set is
// derived rather than trusted: stagingfitness_test.go walks the registry and
// fails on a stageable tool that does not refuse.
//
// rec must be the record the change targets, read through the seam.
func refuseStagingElsewhere(rec datasource.Record) error {
	if rec.Freshness.Authoritative {
		return nil
	}
	return fmt.Errorf(
		"this %s is held in an external system of record, so an approval for it could never be released: %w",
		rec.Ref.Type, apperrors.ErrUnsupportedBySoR)
}

// splitApproval pops the approval_id argument and canonicalizes what
// remains through the shared diffhash spelling: the diff_hash is
// computed over the SAME bytes on staging, redemption, and
// modify-then-approve, so "identical call" is a property of content,
// not of whitespace or key order.
func splitApproval(in json.RawMessage) (args json.RawMessage, approvalID ids.ApprovalID, diffHash string, err error) {
	var m map[string]any
	if err := json.Unmarshal(in, &m); err != nil {
		return nil, ids.ApprovalID{}, "", &BadArgsError{Cause: err}
	}
	if raw, ok := m["approval_id"]; ok {
		s, isStr := raw.(string)
		if !isStr {
			return nil, ids.ApprovalID{}, "", &BadArgsError{Cause: errInvalidApprovalID}
		}
		approvalID, err = ids.ParseAs[ids.ApprovalKind](s)
		if err != nil {
			return nil, ids.ApprovalID{}, "", &BadArgsError{Cause: err}
		}
		delete(m, "approval_id")
	}
	canonical, diffHash, err := diffhash.Object(m)
	if err != nil {
		return nil, ids.ApprovalID{}, "", err
	}
	return canonical, approvalID, diffHash, nil
}
