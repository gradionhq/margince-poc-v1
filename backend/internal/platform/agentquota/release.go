// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agentquota

// Confirm-and-continue: the §2.4 ladder's release, and the only thing that ever
// widens a window from inside it.

import (
	"context"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Release grants one more allowance of counter c to passport, in the window
// named by bucket, and reports whether it applied.
//
// IT TAKES ITS SUBJECT EXPLICITLY rather than reading the context, and that is
// the whole shape of this call. Every other method here meters the caller; this
// one is invoked by the HUMAN who lent the passport, from an approval decision,
// and the agent whose window is widened is nowhere near that request. A version
// that read ctx would silently release the approver's own counter — which is not
// metered at all, so it would appear to work and do nothing.
//
// It applies only to a RELEASABLE counter (BYO-STEP-1 and -2). Egress and calls
// are hard stops the spec ends with the window, and cost refuses nothing, so
// there is nothing there to release; a request to release one is a defect in the
// caller, answered as an error rather than as a quiet success.
//
// It applies only to the CURRENT window. A release names the bucket the human
// was shown, so an approval answered after that window rolled applies to
// nothing — correctly: the counter it would have widened is already back at
// zero, and the agent it was granted for is no longer refused. Reporting that as
// `applied == false` lets the decision path say so instead of claiming an effect
// it did not have. A bucket in the FUTURE is refused for the opposite reason: it
// would pre-authorize a window nobody has looked at, and the payload a release
// reads is editable before it is approved.
func (m *Meter) Release(ctx context.Context, ws, passport ids.UUID, c Counter, bucket int64) (bool, error) {
	// Every argument is judged BEFORE the meter's own reachability, so a caller
	// defect reads the same whether or not Redis is up. The other order hides a
	// wiring fault in exactly the deployment where one is most likely.
	if !c.Releasable() {
		return false, fmt.Errorf("agentquota: %s is not a releasable quota", c)
	}
	if ws == (ids.UUID{}) || passport == (ids.UUID{}) {
		return false, fmt.Errorf("agentquota: releasing %s needs both a workspace and a passport", c)
	}
	current := m.Bucket()
	if bucket > current {
		return false, fmt.Errorf("agentquota: cannot release %s for a window that has not started", c)
	}
	if m.unbounded || m.rdb == nil {
		// Unmetered bounds nothing, so there is nothing to widen; no Redis means
		// the meter is refusing every read already and a release it cannot
		// record must not be reported as one.
		return false, nil
	}
	if bucket < current {
		return false, nil
	}
	key := m.releaseKey(ws, passport.String(), c, bucket)
	if err := addScript.Run(ctx, m.rdb, []string{key}, 1, m.ttlSeconds()).Err(); err != nil {
		return false, fmt.Errorf("agentquota: recording a release of %s: %w", c, err)
	}
	return true, nil
}
