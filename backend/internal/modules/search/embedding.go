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
	// EmbedIdentity is the current binding's stamp — cheap, no API call.
	// identity = "<provider>/<model>@<dims>"; dims is the width guard's expected size.
	EmbedIdentity() (identity string, dims int)
}

// isZero reports whether every vector component is exactly 0. Cosine
// similarity against the zero vector is 0/0 = NaN, and a naive
// `ORDER BY sim DESC` sorts NaN FIRST — silently outranking every real
// match — so a zero vector must never reach storage.
func isZero(vec []float32) bool {
	for _, v := range vec {
		if v != 0 {
			return false
		}
	}
	return true
}

// UpsertEmbedding maintains one entity's vector. Content-hash keyed
// (ai-operational-spec §6): unchanged text under an unchanged embed
// binding costs NO model call — the returned bool says whether an
// embedding was actually computed. A text match under a CHANGED binding
// (an operator swap to a different provider/model/width) still
// re-embeds: skipping on hash alone would leave the row stamped with a
// model no longer serving the workspace, indistinguishable from a live
// one.
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
	identity, dims := embedder.EmbedIdentity()

	fresh := false
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var existingHash, existingModel string
		err := tx.QueryRow(ctx, `
			SELECT chunk_hash, model FROM embedding
			WHERE entity_type = $1 AND entity_id = $2 AND chunk_ix = 0`,
			entityType, entityID).Scan(&existingHash, &existingModel)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			// No row yet: existingHash/existingModel stay "" — the honest
			// "nothing stored" case, distinct from a real empty hash/model.
		}
		if existingHash == hash && existingModel == identity {
			return nil // unchanged text, unchanged binding — never re-embed
		}

		res, err := embedder.Embed(ctx, model.EmbedRequest{Inputs: []string{text}, Dimensions: dims})
		if err != nil {
			return fmt.Errorf("search: embed: %w", err)
		}
		if len(res.Vectors) != 1 || res.Dims != dims {
			return fmt.Errorf("search: embedder returned %d vectors of width %d, need 1×%d", len(res.Vectors), res.Dims, dims)
		}
		if isZero(res.Vectors[0]) {
			return fmt.Errorf("search: embedder returned a zero vector (cosine NaN)")
		}

		// CAS on the hash read above ('' when no row existed): a
		// concurrent writer that already advanced chunk_hash past what we
		// read (a redelivered event racing this one, or another identity
		// swap) already won — leave fresh=false rather than clobbering a
		// row fresher than the one this call started from.
		tag, err := tx.Exec(ctx, `
			INSERT INTO embedding (workspace_id, entity_type, entity_id, chunk_ix, chunk_hash, model, embedding)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, 0, $3, $4, $5::vector)
			ON CONFLICT (workspace_id, entity_type, entity_id, chunk_ix)
			DO UPDATE SET chunk_hash = EXCLUDED.chunk_hash, model = EXCLUDED.model,
			              embedding = EXCLUDED.embedding, created_at = now()
			WHERE embedding.chunk_hash IS NOT DISTINCT FROM $6`,
			entityType, entityID, hash, identity, vectorLiteral(res.Vectors[0]), existingHash)
		if err != nil {
			return fmt.Errorf("search: upsert embedding: %w", err)
		}
		fresh = tag.RowsAffected() > 0
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
