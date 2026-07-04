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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

var errInvalidApprovalID = errors.New("approval_id must be a UUID string")

// Approvals is the staging/redemption dependency, implemented by the
// approvals module and injected at the composition root (this package
// depends on seams, never on sibling modules).
type Approvals interface {
	Stage(ctx context.Context, in StageRequest) (ids.UUID, error)
	Redeem(ctx context.Context, approvalID ids.UUID, tool, diffHash string) error
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

// splitApproval pops the approval_id argument and canonicalizes what
// remains: the diff_hash is computed over the SAME bytes on staging and
// redemption, so "identical call" is a property of content, not of
// whitespace or key order.
func splitApproval(in json.RawMessage) (args json.RawMessage, approvalID ids.UUID, diffHash string, err error) {
	// Decoding into interface maps and re-marshaling sorts keys at EVERY
	// depth — a re-invocation hashes equal by content, not by the
	// client's serialization habits.
	var m map[string]any
	if err := json.Unmarshal(in, &m); err != nil {
		return nil, ids.Nil, "", &BadArgsError{Cause: err}
	}
	if raw, ok := m["approval_id"]; ok {
		s, isStr := raw.(string)
		if !isStr {
			return nil, ids.Nil, "", &BadArgsError{Cause: errInvalidApprovalID}
		}
		approvalID, err = ids.Parse(s)
		if err != nil {
			return nil, ids.Nil, "", &BadArgsError{Cause: err}
		}
		delete(m, "approval_id")
	}
	canonical, err := json.Marshal(m)
	if err != nil {
		return nil, ids.Nil, "", err
	}
	sum := sha256.Sum256(canonical)
	return canonical, approvalID, hex.EncodeToString(sum[:]), nil
}
