// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// The automatic learner is a cheap fleet scan. It never calls a model per
// message: capture only grows the corpus, while this daily pass selects a
// bounded rebuild after enough new evidence or one weekly refresh.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

const (
	automaticVoiceNewWords    = 2000
	automaticVoiceNewMessages = 10
)

// AutomaticVoiceCandidate is an owner-bound profile due for one batch build.
type AutomaticVoiceCandidate struct {
	WorkspaceID ids.UUID
	ProfileID   ids.UUID
	OwnerID     ids.UUID
}

// DueAutomaticProfiles walks tenant workspaces under their own RLS GUC and
// returns only profiles that opted in and have no active build. One workspace
// fault does not starve the others.
func (s *VoiceStore) DueAutomaticProfiles(ctx context.Context) ([]AutomaticVoiceCandidate, error) {
	// rls-exempt: workspace is the fleet root; tenant reads below enter each workspace's GUC.
	rows, err := s.pool.Query(ctx, `SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("voice learning: list workspaces: %w", err)
	}
	workspaces, err := pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
	if err != nil {
		return nil, err
	}
	var candidates []AutomaticVoiceCandidate
	var scanErrors error
	for _, workspaceID := range workspaces {
		workspaceContext := principal.WithWorkspaceID(ctx, workspaceID)
		err := database.WithWorkspaceTx(workspaceContext, s.pool, func(tx pgx.Tx) error {
			profileRows, err := tx.Query(workspaceContext, `
				SELECT p.id, p.owner_id
				FROM voice_profile p
				WHERE p.scope = 'user' AND p.owner_id IS NOT NULL
				  AND p.auto_learning_enabled AND p.archived_at IS NULL
				  AND NOT EXISTS (
				    SELECT 1 FROM voice_build b
				    WHERE b.voice_profile_id = p.id AND b.status IN ('queued','running'))
				  AND (SELECT coalesce(sum(s.word_count), 0)
				       FROM voice_corpus_source s
				       WHERE s.voice_profile_id = p.id AND NOT s.excluded) >= $1
				  AND (
				    p.profile_version = 0
				    OR EXISTS (
				      SELECT 1 FROM voice_corpus_source s
				      WHERE s.voice_profile_id = p.id AND NOT s.excluded
				        AND coalesce(s.occurred_at, s.created_at) > p.last_built_at
				      HAVING (count(*) >= $2 AND coalesce(sum(s.word_count), 0) >= $3)
				         OR (p.last_built_at <= now() - interval '7 days' AND count(*) > 0)
				    )
				  )
				ORDER BY p.updated_at`, StarterVoiceWords, automaticVoiceNewMessages, automaticVoiceNewWords)
			if err != nil {
				return err
			}
			defer profileRows.Close()
			for profileRows.Next() {
				candidate := AutomaticVoiceCandidate{WorkspaceID: workspaceID}
				if err := profileRows.Scan(&candidate.ProfileID, &candidate.OwnerID); err != nil {
					return err
				}
				candidates = append(candidates, candidate)
			}
			return profileRows.Err()
		})
		if err != nil {
			scanErrors = errors.Join(scanErrors, fmt.Errorf("voice learning: scan workspace %s: %w", workspaceID, err))
		}
	}
	return candidates, scanErrors
}
