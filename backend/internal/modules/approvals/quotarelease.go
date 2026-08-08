// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

// Confirm-and-continue (api-rate-limits-and-abuse §2.4, BYO-STEP-1/-2): the one
// staged kind whose approval widens a volume window rather than releasing a
// write.
//
// It is here rather than behind the per-kind effect table because that table is
// deliberately closed to agent-minted stagings (serverProposed): a kind is not a
// namespace, and an agent choosing a kind that matched a registered executor
// used to be able to invoke it. This kind is never chosen by a caller — the tool
// surface mints it on a refusal, from the meter's own reading — and its subject
// is the passport the staging was stamped with, which no request body can
// influence. Saying that in a typed branch is honest; smuggling it past the
// provenance guard would not be.

import (
	"context"
	"errors"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/platform/agentquota"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// KindQuotaRelease is the staged kind a step-up asks under. Exported because the
// tool surface stages it and the composition root's fitness tests hold it to the
// same obligations every other kind carries.
const KindQuotaRelease = "quota_release"

// QuotaReleaser applies a confirm-and-continue release to the meter.
//
// It is a seam rather than the concrete meter because the meter holds a Redis
// client, and this module owns rows: a service constructed for a role that
// serves no agents composes none, and its releases are then a loud absence
// rather than a silent no-op.
type QuotaReleaser interface {
	Release(ctx context.Context, ws, passport ids.UUID, c agentquota.Counter, bucket int64) (bool, error)
}

// WithQuotaReleaser installs the meter an approved step-up widens.
func (s *Service) WithQuotaReleaser(r QuotaReleaser) *Service {
	s.quota = r
	return s
}

// applyQuotaRelease widens the window an approved step-up asked about.
//
// EVERY INPUT COMES FROM THE STORED ROW, and that is what makes it safe. The
// passport is the one the staging stamped from the authenticated agent
// principal; the workspace is the deciding request's own; the counter and window
// come from the payload, which is validated on the way in (only a releasable
// counter) and again where it lands (only the current window). Nothing here is
// read from the deciding human's request body, so the modify-then-approve arm —
// which pins entity references, of which this payload has none — cannot re-aim a
// release at another agent's window.
//
// A release that applied to NOTHING is not an error. The ordinary cause is a
// human answering after the window rolled, and by then the agent is no longer
// refused: there is nothing to widen because there is nothing to release it
// from. The decision still stands as the record that they said yes.
func (s *Service) applyQuotaRelease(ctx context.Context, a row) error {
	if s.quota == nil {
		return errors.New("crmapprovals: this composition has no quota meter, so a step-up cannot be released")
	}
	if a.PassportID == nil {
		// A step-up is always staged by an agent asserting a passport. A row
		// without one names no window, and releasing "the current context's"
		// would widen the approver's own counter — which is not metered at all,
		// so it would look like it worked.
		return errors.New("crmapprovals: a staged step-up carries no passport, so there is no window to release")
	}
	wsID, ok := principal.WorkspaceID(ctx)
	if !ok {
		return errors.New("crmapprovals: no workspace bound to context")
	}
	proposal, err := agentquota.DecodeReleaseProposal(a.ProposedChange)
	if err != nil {
		return err
	}
	if _, err := s.quota.Release(ctx, wsID, a.PassportID.UUID, proposal.Counter, proposal.Bucket); err != nil {
		return fmt.Errorf("crmapprovals: releasing the %s window: %w", proposal.Counter, err)
	}
	return nil
}
