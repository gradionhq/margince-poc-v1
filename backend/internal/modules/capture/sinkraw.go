// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// storeRawCapture appends the provider's original bytes under the natural
// key. Raw capture is EVIDENCE: append-once, never rewritten. A replay
// carrying different bytes for the same natural key keeps the original —
// silently replacing provenance would gut lineage and forensic replay. A
// record that arrived with no original stores nothing.
func storeRawCapture(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord) error {
	if len(rec.Raw) == 0 {
		return nil
	}
	payload := rec.Raw
	if !json.Valid(payload) {
		// Non-JSON originals are stored as a JSON string so the
		// column type never rejects a provider's format.
		encoded, err := json.Marshal(string(rec.Raw))
		if err != nil {
			return err
		}
		payload = encoded
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO raw_capture (source_system, source_id, payload)
		VALUES ($1, $2, $3)
		ON CONFLICT (source_system, source_id) DO NOTHING`,
		rec.NaturalKey.SourceSystem, rec.NaturalKey.SourceID, payload); err != nil {
		return fmt.Errorf("capture: raw store: %w", err)
	}
	return nil
}
