// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The audit half of the write shape for capture's own lifecycle records —
// the connector connection a human grants and withdraws, and the exclusion
// rules they keep. Every mutation of these rows is somebody's deliberate act
// over their mailbox, which is exactly the kind of change the audit spine
// exists to attribute.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// captureConnectionObject names the connector connection in the audit trail.
const captureConnectionObject = "capture_connection"

// captureExclusionRuleObject names a personal-mail exclusion rule in the
// audit trail.
const captureExclusionRuleObject = "capture_exclusion_rule"

// auditLifecycle writes the audit row for one capture lifecycle mutation
// inside that mutation's own transaction, so the record and its attribution
// commit together or not at all.
//
// It is audit-only. The paired event half of the write shape needs a kernel
// entity kind for the record it announces, and neither the connector
// connection nor the exclusion rule is modelled as one — the closed event
// catalog defines no verb that could carry them. This is the same ratified
// posture the capture-settings write holds (EVT-NOEVT-3), not an omission.
//
// before is nil when the mutation creates the record. Neither image ever
// carries credential material: the vault is the custodian of the secret and
// the audit trail must not become a second one.
func auditLifecycle(ctx context.Context, tx pgx.Tx, verb, object string, id ids.UUID, before, after map[string]any) error {
	if _, err := storekit.Audit(ctx, tx, verb, object, id, before, after); err != nil {
		return fmt.Errorf("capture: auditing the %s of %s %s: %w", verb, object, id, err)
	}
	return nil
}
