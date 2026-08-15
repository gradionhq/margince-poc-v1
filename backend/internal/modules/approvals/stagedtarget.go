// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

// The record a staging points at, read back by the executor that releases it.

import (
	"context"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// StagedTarget answers the entity one staging proposes to write, for a release
// executor whose payload does not name it.
//
// Most executors need nothing like this: their proposed_change carries the
// record id, because the proposal was composed around it. A kind staged from an
// automation's own action does not — the action's args ARE the patch, and the
// record the patch applies to lives in the row's target columns, put there by
// the firing rather than by the proposal.
//
// Reading it back rather than folding it into the payload is the security half.
// The payload is what the deciding human sees and may EDIT; the target columns
// are not editable, and the version pin and the decision grants are both
// resolved against them. Carrying the record id inside the editable payload
// would let an approver retarget the write onto a record they were never shown
// — the same substitution held drafts refuse by pinning their addressee.
//
// Returned as the two primitives rather than a datasource.EntityRef so this
// module keeps no dependency on the record port it deliberately knows nothing
// about; the composition layer that owns the provider builds the ref.
//
// It reads through Get rather than querying the row itself, so it inherits that
// path's gate: an approval the caller could not decide reads as absent here too.
// A second query would have been a second reading of one row, and the one
// without the visibility check is the one that becomes a lookup oracle for
// target ids the caller was never shown.
//
// A staging with no target is an error rather than an empty ref: a caller
// asking for one has already decided its release writes to a record, and a
// silent zero value would be a write aimed at nothing.
func (s *Service) StagedTarget(ctx context.Context, id ids.ApprovalID) (entityType string, entityID ids.UUID, err error) {
	a, err := s.Get(ctx, id)
	if err != nil {
		return "", ids.UUID{}, err
	}
	if a.TargetType == nil || a.TargetID == nil {
		return "", ids.UUID{}, fmt.Errorf("crmapprovals: staging %s of kind %q names no target record", id, a.Kind)
	}
	return *a.TargetType, *a.TargetID, nil
}
