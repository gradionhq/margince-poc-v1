// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// Embedder is the embed lane this module consumes; compose injects the
// ai router (or the offline fake) — search never picks a model.
type Embedder interface {
	Embed(ctx context.Context, req model.EmbedRequest) (model.Embeddings, error)
}

// embedModelLabel records which lane produced a row. The binding is
// per-deployment config (one embedder per store — mixed models cannot
// rank against each other); swapping it means wiping the store, which
// the width-change custom migration forces anyway.
const embedModelLabel = "embed-lane"

// embeddingDims must match the migration's vector(1024) column; a
// binding that produces another width fails loudly here instead of
// storing unrankable vectors.
const embeddingDims = 1024

// UpsertEmbedding maintains one entity's vector. Content-hash keyed
// (ai-operational-spec §6): unchanged text costs NO model call — the
// returned bool says whether an embedding was actually computed.
func (s *Store) UpsertEmbedding(ctx context.Context, entityType string, entityID ids.UUID, text string, embedder Embedder) (bool, error) {
	if !knownEntity(entityType) {
		return false, fmt.Errorf("search: unembeddable entity type %q", entityType)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return false, nil // nothing to embed; an empty vector helps nobody
	}
	sum := sha256.Sum256([]byte(text))
	hash := hex.EncodeToString(sum[:])

	fresh := false
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var existing string
		err := tx.QueryRow(ctx, `
			SELECT chunk_hash FROM embedding
			WHERE entity_type = $1 AND entity_id = $2 AND chunk_ix = 0`,
			entityType, entityID).Scan(&existing)
		if err == nil && existing == hash {
			return nil // never re-embed unchanged text
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		res, err := embedder.Embed(ctx, model.EmbedRequest{Inputs: []string{text}, Dimensions: embeddingDims})
		if err != nil {
			return fmt.Errorf("search: embed: %w", err)
		}
		if len(res.Vectors) != 1 || res.Dims != embeddingDims {
			return fmt.Errorf("search: embedder returned %d vectors of width %d, need 1×%d", len(res.Vectors), res.Dims, embeddingDims)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO embedding (workspace_id, entity_type, entity_id, chunk_ix, chunk_hash, model, embedding)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, 0, $3, $4, $5::vector)
			ON CONFLICT (workspace_id, entity_type, entity_id, chunk_ix)
			DO UPDATE SET chunk_hash = EXCLUDED.chunk_hash, model = EXCLUDED.model,
			              embedding = EXCLUDED.embedding, created_at = now()`,
			entityType, entityID, hash, embedModelLabel, vectorLiteral(res.Vectors[0]))
		if err != nil {
			return fmt.Errorf("search: upsert embedding: %w", err)
		}
		fresh = true
		return nil
	})
	return fresh, err
}

// VectorHit is one similarity result, already visibility-filtered.
type VectorHit struct {
	Type       string
	ID         ids.UUID
	Title      string
	Similarity float64
}

// SimilarEntities ranks entities by cosine similarity to the query
// vector. Object RBAC and row scope gate every branch, exactly like the
// lexical union — a vector hit is a read too.
func (s *Store) SimilarEntities(ctx context.Context, queryVec []float32, limit int) ([]VectorHit, error) {
	limit = clampLimit(limit)
	var hits []VectorHit
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var args []any
		arg := func(v any) int { args = append(args, v); return len(args) }
		vecPos := arg(vectorLiteral(queryVec))

		var branches []string
		for _, branch := range searchBranches {
			scope, admitted, err := branchScope(ctx, branch, arg)
			if err != nil {
				return err
			}
			if !admitted {
				continue
			}
			sql := fmt.Sprintf(
				`SELECT '%s'::text AS rtype, e.entity_id AS id, %s AS title,
				        (1 - (e.embedding <=> $%d::vector))::float8 AS sim
				 FROM embedding e JOIN %s t ON t.id = e.entity_id
				 WHERE e.entity_type = '%s' AND t.archived_at IS NULL`,
				branch.entity, branch.title, vecPos, branch.table, branch.entity)
			if scope != "" {
				sql += " AND " + scope
			}
			branches = append(branches, sql)
		}
		if len(branches) == 0 {
			return nil
		}
		sql := "SELECT rtype, id, title, sim FROM (" + strings.Join(branches, " UNION ALL ") +
			fmt.Sprintf(") ranked ORDER BY sim DESC, rtype, id LIMIT $%d", arg(limit))
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return fmt.Errorf("search: similarity query: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var h VectorHit
			var title *string
			if err := rows.Scan(&h.Type, &h.ID, &title, &h.Similarity); err != nil {
				return err
			}
			if title != nil {
				h.Title = *title
			}
			hits = append(hits, h)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return hits, nil
}

// vectorLiteral renders pgvector's input syntax; parameterized as text
// and cast, so no vector codec dependency rides the driver.
func vectorLiteral(vec []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, v := range vec {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(v), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}
