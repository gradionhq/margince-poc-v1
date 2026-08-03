// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The audit half of the write shape for capture's own lifecycle records — the
// connector connection a human grants and withdraws. Every mutation of these
// rows is somebody's deliberate act over their mailbox, which is exactly the
// kind of change the audit spine exists to attribute.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// captureConnectionObject names the connector connection in the audit trail.
const captureConnectionObject = "capture_connection"

// auditLifecycle writes the audit row for one capture lifecycle mutation
// inside that mutation's own transaction, so the record and its attribution
// commit together or not at all.
//
// It is audit-only. The paired event half of the write shape needs a kernel
// entity kind for the record it announces, and the connector connection is not
// modelled as one — the closed event catalog defines no verb that could carry
// it. This is the same ratified posture the capture-settings write holds
// (EVT-NOEVT-3), not an omission.
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

// connectionAuditImage is one side of a connector connection's audit trail.
// The shape is spelled once so a grant and its withdrawal are diffable field
// for field. accountLabel stays a pointer: a connector that cannot name its
// account records no claim about it, which is not the same as naming an empty
// one. The credential ref is deliberately absent — the vault is the custodian
// of that secret and the audit spine must not become a second one.
func connectionAuditImage(provider, status string, accountLabel *string) map[string]any {
	return map[string]any{
		"provider":      provider,
		"status":        status,
		"account_label": accountLabel,
	}
}
