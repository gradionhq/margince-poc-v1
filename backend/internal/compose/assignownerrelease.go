// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Releasing a reassignment at scale (AUTO-T07's 🟡 branch).
//
// An automation that reassigns one record writes straight through the provider;
// one that reassigns at scale stages instead and waits (assign_owner_tier.go's
// resolveAssignOwnerTier). The staging half shipped and the release half did
// not, so approving the card spent a human's decision on nothing: no owner
// moved, and the run that raised it stayed in requires_approval permanently.
//
// The effect performs the IDENTICAL write the 🟢 branch performs — same
// provider call, same patch, same system provenance — because a reassignment a
// human released must not be a second spelling of a reassignment an automation
// made. What the approval adds is the human, and the human is on the decision's
// own audit row.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// assignOwnerReleaseActor is the provenance the released write carries. The
// owner change came from an automation's rule, not from somebody typing it, and
// a later human edit must still read as stronger than it.
const assignOwnerReleaseActor = "system:assign-owner-release"

// assignOwnerReleaseEffect executes an approved at-scale reassignment and
// finishes the run that waited for it.
//
// Redeem-then-execute, like every 🟡 executor here, and the version pin is why
// it cannot be the other way round. The redemption re-checks that the target
// still carries the version the staging pinned; performing the write first
// would bump that version and the redemption would then refuse the very write
// it had just authorized. Ordering is not a preference here — one order does
// not work.
//
// The version the redemption returns is carried straight into the write as
// IfVersion, which is what closes the gap the ordering opens. Between the
// redemption committing and the provider being called, the record is
// unprotected; pinning the write to the version the approval was granted
// against means anything that moved it in that window makes the write refuse
// rather than silently overwrite somebody.
//
// A write that fails leaves the approval spent — the accepted trade of the
// redeem-then-execute discipline, and the reason Decide reports the executor's
// cause to its caller rather than swallowing it. What must not happen is the
// run reporting success: the transition runs only after the write returns, so a
// failed reassignment leaves its run parked and honest.
func assignOwnerReleaseEffect(svc *approvals.Service, provider datasource.SystemOfRecordProvider, db *database.DB) approvals.ApprovedEffect {
	kind := string(workflow.ActionAssignOwner)
	return func(ctx context.Context, approvalID ids.ApprovalID, proposedChange json.RawMessage, diffHash string) error {
		entityType, entityID, err := svc.StagedTarget(ctx, approvalID)
		if err != nil {
			return fmt.Errorf("compose: the record an approved reassignment names: %w", err)
		}
		decider, ok := principal.Actor(ctx)
		if !ok {
			return fmt.Errorf("compose: assign_owner release without a deciding principal")
		}
		version, pinned, err := svc.Redeem(ctx, approvalID, kind, diffHash)
		if err != nil {
			return err
		}
		execCtx := principal.WithActor(ctx, principal.Principal{
			Type:       principal.PrincipalSystem,
			ID:         assignOwnerReleaseActor,
			UserID:     decider.UserID,
			OnBehalfOf: decider.UserID,
		})
		var ifVersion *int64
		if pinned {
			ifVersion = &version
		}
		// The patch is handed over exactly as staged — the canonical form of the
		// action's own args — rather than decoded and re-encoded here. The
		// provider owns which fields an entity type accepts, and unpacking the
		// payload at this layer would be compose forming a second opinion about
		// that, which is how the released write stops being the same write.
		if _, err := provider.Update(execCtx, datasource.UpdateInput{
			Ref:       datasource.EntityRef{Type: datasource.EntityType(entityType), ID: entityID},
			Patch:     proposedChange,
			Source:    assignOwnerReleaseActor,
			IfVersion: ifVersion,
		}); err != nil {
			return fmt.Errorf("compose: applying the approved reassignment: %w", err)
		}
		return automation.CompleteApprovedRun(execCtx, db, approvalID)
	}
}
