// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// Retention for the AI-side stores: the voice signal corpus and the model-call
// payload log. Kept apart from the record retention ladder because the subject
// is different — these hold what a model was SHOWN and what it produced, not
// the CRM record it was shown about, and their windows are argued from a
// different place.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// aiRetentionStages is how many batched stages this file adds to a workspace
// pass — the embedding-kind ai_call sweep and the voice-signal sweep — each
// claiming its own retentionBatch. MaxPassDuration counts them alongside the
// policy ladder, so a third sweep here has to move this number with it.
const aiRetentionStages = 2

// evaluateVoiceSignalRetention erases the draft plaintext of over-age voice
// learning signals: the counters row survives (the learning statistics stay
// honest), the generated and final texts do not outlive their window.
func (s *RetentionService) evaluateVoiceSignalRetention(ctx context.Context) error {
	var due []ids.UUID
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id FROM voice_learning_signal
			WHERE retention_until < now() AND content_erased_at IS NULL
			  AND (generated_original IS NOT NULL OR final_text IS NOT NULL)
			LIMIT $1`, retentionBatch)
		if err != nil {
			return err
		}
		due, err = pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
		return err
	})
	if err != nil {
		return fmt.Errorf("retention voice_learning_signal: select: %w", err)
	}
	for _, id := range due {
		if err := s.eraseVoiceSignalContent(ctx, id); err != nil {
			return fmt.Errorf("retention voice_learning_signal on %s: %w", id, err)
		}
	}
	return nil
}

func (s *RetentionService) eraseVoiceSignalContent(ctx context.Context, id ids.UUID) error {
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		// The content_erased_at predicate is the CAS: a rival sweep that
		// already erased this row matches zero rows, and nothing is audited
		// twice for one erasure.
		tag, err := tx.Exec(ctx, `
			UPDATE voice_learning_signal
			SET generated_original = NULL, final_text = NULL, content_erased_at = now(),
			    version = version + 1, updated_at = now()
			WHERE id = $1 AND content_erased_at IS NULL`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		auditID, err := storekit.AuditWithEvidence(ctx, tx, actionErase, "voice_learning_signal", id, nil, nil, map[string]any{
			evidenceKeyRetentionAction: actionErase,
		})
		if err != nil {
			return err
		}
		return storekit.EmitEventForEntity(ctx, tx, auditID, "voice_learning_signal", id, retentionAppliedPayload(actionErase, nil, nil))
	})
}

// embedCallWithoutPayload narrows the embed sweep to the rows whose deletion
// destroys nothing but telemetry.
//
// It is what makes the sweep safe under the retain-only posture. ai_call's own
// columns are routing, spend and served identity — no subject content, which is
// why ageing them out is hygiene rather than storage limitation. But
// ai_call_payload FK-CASCADES from ai_call (0089), and an embed call's captured
// payload holds the INPUT TEXTS that were embedded (ai.buildEmbedPayload) —
// record content, and the same special-category-adjacent content the
// ai_call_payload/content policy governs. So deleting a payload-bearing embed
// call destroys subject content through the cascade, in the same pass that
// suppresses the policy which governs exactly those rows.
//
// Under the posture the sweep therefore skips any embed call that has a payload
// row. With Layer-3 capture off — the default — no embed call has one, so the
// volume hygiene this sweep exists for is unaffected.
const embedCallWithoutPayload = `
	AND NOT EXISTS (SELECT 1 FROM ai_call_payload p WHERE p.ai_call_id = ai_call.id)`

// evaluateEmbedCallRetention erases over-age embedding-kind ai_call trace
// rows, batched and audited one record per transaction like every other
// retention action — but driven by the fixed embedCallRetention cap
// instead of a workspace's retention_policy rows, since these rows are
// engine telemetry, not a policy-configurable domain record.
//
// retainOnly narrows it to the payload-free rows; see embedCallWithoutPayload.
func (s *RetentionService) evaluateEmbedCallRetention(ctx context.Context, retainOnly bool) error {
	selector := `
		SELECT id FROM ai_call
		WHERE kind = 'embedding' AND occurred_at < now() - make_interval(days => $1)`
	if retainOnly {
		selector += embedCallWithoutPayload
	}
	selector += ` LIMIT $2`
	var due []ids.UUID
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, selector, embedCallRetention, retentionBatch)
		if err != nil {
			return err
		}
		due, err = pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
		return err
	})
	if err != nil {
		return fmt.Errorf("retention ai_call/embedding: select: %w", err)
	}
	for _, id := range due {
		if err := s.eraseEmbedCall(ctx, id); err != nil {
			return fmt.Errorf("retention ai_call/embedding on %s: %w", id, err)
		}
	}
	return nil
}

// eraseEmbedCall deletes one over-age embedding-kind ai_call row outright
// — unlike activity/erase there is no metadata half left to keep: the
// embedding trace row IS the content being aged out.
func (s *RetentionService) eraseEmbedCall(ctx context.Context, id ids.UUID) error {
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM ai_call WHERE id = $1`, id); err != nil {
			return err
		}
		auditID, err := storekit.AuditWithEvidence(ctx, tx, actionErase, "ai_call", id, nil, nil, map[string]any{
			evidenceKeyRetentionAction: actionErase, "retain_days": embedCallRetention,
		})
		if err != nil {
			return err
		}
		return storekit.EmitEventForEntity(ctx, tx, auditID, "ai_call", id, retentionAppliedPayload(actionErase, nil, nil))
	})
}
