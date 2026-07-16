// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// The approvals seam (AUTO-T05, features/03 §5): ApplyActions recognizes
// a 🟡 action but has nowhere to send it without a staging dependency —
// without one, the run parks at requires_approval with no approval row
// behind it and no way for a human to ever see or decide it. Declared
// with ids/json/stdlib types only, so this module never imports the
// approvals module directly (a module never imports a sibling,
// ADR-0054 §9); compose owns the adapter that maps this onto the real
// approvals.Service.

import (
	"context"
	"encoding/json"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Approvals is the staging dependency ApplyActions holds: a 🟡 action
// stages here and ApplyActions returns workflow.StagedApprovalError
// carrying the resulting id back to the caller, which runOne then writes
// onto the parked run row. Redemption (resuming a staged action with an
// approval token) is a later slice — runOne always calls Apply with a
// nil token today, so no Redeem method is declared here; adding one with
// no caller would be speculative.
type Approvals interface {
	Stage(ctx context.Context, in StageRequest) (ids.ApprovalID, error)
}

// StageRequest is what ApplyActions hands the approvals seam for one
// staged 🟡 action: enough for the inbox to show a human what the
// automation wants to do and for the action to be identified again once
// decided.
type StageRequest struct {
	Kind           string          // the action kind being staged, e.g. "send_email"
	ProposedChange json.RawMessage // the action's args, as the approver will see them
	DiffHash       string
	TargetType     string
	TargetID       ids.UUID
	Summary        string
}
