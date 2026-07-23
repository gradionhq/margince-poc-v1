// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
)

// EmbedGen keeps the vector store current: it consumes entity events
// off the bus (cg:context-graph) and re-embeds the changed entity's
// text. The content-hash guard in UpsertEmbedding makes redelivery and
// no-op updates free — at-least-once delivery costs no model calls.
type EmbedGen struct {
	store    *Store
	embedder Embedder
}

func NewEmbedGen(store *Store, embedder Embedder) *EmbedGen {
	return &EmbedGen{store: store, embedder: embedder}
}

// The five embeddable entity types, named once so embedText and
// binding.go's pendingSources (the per-id and set-form views of the same
// source columns) key off the same identifiers rather than each
// repeating the literal.
const (
	entityPerson       = "person"
	entityOrganization = "organization"
	entityDeal         = "deal"
	entityLead         = "lead"
	entityActivity     = "activity"
)

// embedText mirrors each entity's search_tsv source columns — the
// vector lane and the lexical lane index the same content, so a hybrid
// hit means agreement about one text, not two.
var embedText = map[string]string{
	entityPerson:       `SELECT full_name FROM person WHERE id = $1 AND archived_at IS NULL`,
	entityOrganization: `SELECT concat_ws(' ', display_name, legal_name, industry) FROM organization WHERE id = $1 AND archived_at IS NULL`,
	entityDeal:         `SELECT name FROM deal WHERE id = $1 AND archived_at IS NULL`,
	entityLead:         `SELECT concat_ws(' ', full_name, company_name, title) FROM lead WHERE id = $1 AND archived_at IS NULL`,
	entityActivity:     `SELECT concat_ws(' ', subject, body) FROM activity WHERE id = $1 AND archived_at IS NULL`,
}

// HandleEvent maintains embeddings for created/updated/captured
// entities. Events that are not entity-content changes are not ours —
// nil, so the consumer group keeps flowing.
func (g *EmbedGen) HandleEvent(ctx context.Context, env kevents.Envelope) error {
	query, embeddable := embedText[env.Entity.Type]
	if !embeddable || !contentChanging(env.Type) {
		return nil
	}
	// The generator reads AS the system: embeddings are an index over
	// the whole workspace, filtered per caller at QUERY time — an
	// index built through one user's row scope would silently hide
	// records from everyone else's retrieval.
	wsCtx := systemWorkspaceContext(ctx, env.WorkspaceID)

	var text string
	err := database.WithWorkspaceTx(wsCtx, g.store.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(wsCtx, query, env.Entity.ID).Scan(&text)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // archived or gone since the event — nothing to index
	}
	if err != nil {
		return fmt.Errorf("search: loading %s %s for embedding: %w", env.Entity.Type, env.Entity.ID, err)
	}
	_, err = g.store.UpsertEmbedding(wsCtx, env.Entity.Type, env.Entity.ID, text, g.embedder)
	return err
}

// contentChanging filters the event types whose payload implies the
// indexed text may have moved.
func contentChanging(eventType string) bool {
	for _, suffix := range []string{".created", ".updated", ".captured", ".promoted", ".merged"} {
		if strings.HasSuffix(eventType, suffix) {
			return true
		}
	}
	return false
}
