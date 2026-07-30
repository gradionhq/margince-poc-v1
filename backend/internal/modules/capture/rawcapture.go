// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// RawRecord is one provider original waiting to be stored verbatim.
type RawRecord struct {
	SourceSystem string // e.g. "telegram"
	SourceID     string // the provider's REDELIVERY key (Telegram's update_id), never the domain natural key
	Payload      []byte // the verbatim provider payload
}

// InsertRawCaptureTx is the ONLY sanctioned way for another package to
// persist a raw provider record inside a caller's own transaction. capture
// owns raw_capture (tableownership_test.go); compose writing that table
// directly fails the ownership gate, and Sink.Upsert opens its OWN
// transaction so it cannot share a webhook handler's. This function opens
// none of its own — it joins whatever transaction the caller already holds,
// which is the entire reason it exists: a webhook must land the raw payload
// and enqueue its normalize job atomically (design §6.2), and that atomicity
// is only possible if the raw insert runs on the caller's tx.
//
// source_id is the update_id, not the bot:chat:message natural key — a
// redelivery repeats the update_id, and ON CONFLICT DO UPDATE refreshes the
// stored original rather than duplicating it, exactly like
// raw_capture_source_unique's own comment describes. The domain-row natural
// key is a different question and belongs on the domain row, not here.
func InsertRawCaptureTx(ctx context.Context, tx pgx.Tx, rec RawRecord) (ids.UUID, error) {
	var id ids.UUID
	err := tx.QueryRow(ctx, `
		INSERT INTO raw_capture (workspace_id, source_system, source_id, payload)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3)
		ON CONFLICT (workspace_id, source_system, source_id) DO UPDATE
		SET payload = EXCLUDED.payload, received_at = now()
		RETURNING id`,
		rec.SourceSystem, rec.SourceID, rec.Payload).Scan(&id)
	if err != nil {
		return ids.Nil, fmt.Errorf("capture: raw store: %w", err)
	}
	return id, nil
}
