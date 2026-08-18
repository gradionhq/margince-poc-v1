// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The person tombstone: what an Art. 17 erasure certifies when it closes, and
// nothing about whom.

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// personErasureCounts is what an erasure tombstone certifies: how much each arm
// of the cascade removed, and nothing whatsoever about whom.
type personErasureCounts struct {
	emailsSuppressed            int
	rawRowsPurged               int64
	aiPayloadsPurged            int64
	activitiesRedacted          int
	activitiesRestricted        int
	channelIdentitiesSuppressed int
}

// tombstonePersonErasure closes the cascade with action=erase and counts only —
// proof without PII. The counts are evidence ABOUT the scrub, so they ride the
// evidence column; before/after stay empty — they are reserved for field
// images, and the record-history read serves a tombstone's images verbatim. The
// paired event tells consumers the subject is gone.
func tombstonePersonErasure(ctx context.Context, tx pgx.Tx, subject ids.PersonID, reason string, counts personErasureCounts) error {
	auditID, err := storekit.AuditWithEvidence(ctx, tx, actionErase, "person", subject.UUID, nil, nil, map[string]any{
		"reason": reason, "emails_suppressed": counts.emailsSuppressed, "raw_rows_purged": counts.rawRowsPurged,
		"ai_payloads_purged": counts.aiPayloadsPurged, "activities_redacted": counts.activitiesRedacted,
		"activities_restricted":         counts.activitiesRestricted,
		"channel_identities_suppressed": counts.channelIdentitiesSuppressed,
	})
	if err != nil {
		return err
	}
	return storekit.EmitEventForEntity(ctx, tx, auditID, "person", subject.UUID, retentionAppliedPayload(actionErase, nil, &reason))
}
