// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// Capture-audit minimization (ADR-0072/A118): a connector-captured activity's
// audit after-image is metadata-only — the natural key, kind, direction, and
// timestamp — never the subject/body. The message content already lives on the
// activity row and the raw_capture blob under their own retention; duplicating
// it into the append-only audit spine (which survives an erasure differently,
// and is special-category-adjacent for mail) is the "noise is not stored" gap
// this closes. Field/record-history for a captured activity therefore show its
// metadata, not its body. Human-authored activities (activities.LogActivity)
// keep their full audit image.

import "github.com/gradionhq/margince/backend/internal/shared/ports/connector"

// capturedActivityAuditImage is the metadata-only after-image for a captured
// activity's create audit.
func capturedActivityAuditImage(rec connector.NormalizedRecord, fields ActivityFields) map[string]any {
	return map[string]any{
		"kind":            fields.Kind,
		"direction":       fields.Direction,
		"occurred_at":     defaultOccurredAt(fields.OccurredAt),
		fieldSourceSystem: rec.NaturalKey.SourceSystem,
		fieldSourceID:     rec.NaturalKey.SourceID,
	}
}
