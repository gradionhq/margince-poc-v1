// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// Capture-fed voice ingestion is connector-only and owner-bound. It stores
// sanitized own-authored text, or metadata plus an exclusion reason with no
// body when the personal-mail guard fires.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// EmailVoiceExtractorVersion pins the quote/signature removal algorithm.
const EmailVoiceExtractorVersion = 1

// IngestCapturedEmail retains sanitized own-authored text for an opted-in owner.
func (s *VoiceStore) IngestCapturedEmail(ctx context.Context, ownerID ids.UUID, sourceRef, label, text string, occurredAt time.Time, exclusionReason *string) error {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.Type != principal.PrincipalConnector || actor.UserID != ownerID {
		return errors.New("voice capture requires the owning connector principal")
	}
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var profileID ids.UUID
		err := tx.QueryRow(ctx, `
			SELECT id FROM voice_profile
			WHERE owner_id = $1 AND scope = 'user' AND auto_learning_enabled AND archived_at IS NULL
			FOR UPDATE`, ownerID).Scan(&profileID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		excluded := exclusionReason != nil
		content := strings.TrimSpace(text)
		if excluded {
			content = ""
		}
		hash := sha256.Sum256([]byte(content))
		wordCount := WordCount(content)
		var sourceID ids.UUID
		var inserted bool
		if err := tx.QueryRow(ctx, `
			INSERT INTO voice_corpus_source
			  (workspace_id, voice_profile_id, kind, register, weight, source_label, source_ref,
			   content, word_count, excluded, origin, exclusion_reason, content_hash, extractor_version, occurred_at)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        $1, 'email', 'written', 1.0, $2, $3, $4, $5, $6, 'capture', $7, $8, $9, $10)
			ON CONFLICT (workspace_id, voice_profile_id, source_ref) DO UPDATE SET
			  source_label = EXCLUDED.source_label, content = EXCLUDED.content,
			  word_count = EXCLUDED.word_count, excluded = EXCLUDED.excluded,
			  exclusion_reason = EXCLUDED.exclusion_reason, content_hash = EXCLUDED.content_hash,
			  extractor_version = EXCLUDED.extractor_version, occurred_at = EXCLUDED.occurred_at,
			  updated_at = now()
			RETURNING id, (xmax = 0)`, profileID, label, sourceRef, content, wordCount, excluded,
			exclusionReason, hex.EncodeToString(hash[:]), EmailVoiceExtractorVersion, occurredAt).Scan(&sourceID, &inserted); err != nil {
			return err
		}
		action := "update"
		if inserted {
			action = "create"
		}
		if _, err := storekit.Audit(ctx, tx, action, "voice_corpus_source", sourceID, nil, map[string]any{
			voiceFieldProfileID: profileID, "source_ref": sourceRef, "origin": "capture",
			"word_count": wordCount, voiceFieldExcluded: excluded, voiceFieldExclusionReason: exclusionReason,
		}); err != nil {
			return err
		}
		if !excluded {
			_, err = tx.Exec(ctx, `UPDATE voice_profile SET status = CASE WHEN profile_version = 0 THEN 'building' ELSE 'stale' END, updated_at = now() WHERE id = $1`, profileID)
		}
		return err
	})
}
