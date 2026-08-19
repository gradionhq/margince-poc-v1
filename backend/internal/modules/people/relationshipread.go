// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Reading a relationship edge, and the row-scope gates that guard it.
// An edge's visibility derives from its ENDPOINTS, so every read here
// answers the same question the write path asks before it mutates:
// absence and out-of-scope are indistinguishable to the caller.

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// visibleRelationship loads one edge under the endpoint-visibility rule
// — absence and out-of-scope read identically (existence-hiding).
func (s *Store) visibleRelationship(ctx context.Context, tx pgx.Tx, id ids.UUID) (relationshipRow, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	idPos := arg(id)
	scope, err := auth.RelationshipEndpointScope(ctx, "r", arg)
	if err != nil {
		return relationshipRow{}, err
	}
	sql := storekit.SQLf(`SELECT %s FROM relationship r WHERE r.id = $%d`, aliased(relationshipColumns, "r"), idPos)
	if scope != "" {
		sql += " AND " + scope
	}
	out, err := scanRelationship(tx.QueryRow(ctx, sql, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return relationshipRow{}, apperrors.ErrNotFound
	}
	return out, err
}

// EnsureDealVisible probes a deal id under the caller's row scope —
// the deal-scoped stakeholder view needs the anchor's own answer when
// the edge list is empty (owned SQL on the deal row).
func (s *Store) EnsureDealVisible(ctx context.Context, dealID ids.DealID) error {
	if err := auth.Require(ctx, "deal", principal.ActionRead); err != nil {
		return err
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		// EnsureLinkTarget, not EnsureVisible: the anchor must EXIST for
		// everyone — unbounded actors skip only the scope half.
		return auth.EnsureLinkTarget(ctx, tx, "deal", dealID.UUID)
	})
}

// aliased qualifies a comma-separated column list with a table alias.
func aliased(columns, alias string) string {
	parts := strings.Split(columns, ",")
	for i, part := range parts {
		parts[i] = alias + "." + strings.TrimSpace(part)
	}
	return strings.Join(parts, ", ")
}

// GetRelationship reads one edge under the endpoint-visibility rule, in its own
// transaction — the entry point the datasource seam needs, since every other
// caller of visibleRelationship already holds a transaction of its own.
//
// The seam needs it for three verbs and not only for a read tool: create_record
// reads the edge back after writing it, and archive_record reads the target
// BEFORE staging, to summarize for the human who will approve. Without this the
// edge would commit and the tool would report a read-back failure — a false
// failure with a real side effect — and the 🟡 archive could not even stage.
//
// The RBAC gate is the read one, not the anchor's update one that the mutating
// verbs also demand: reading an edge discloses its endpoints, which is what
// `relationship` read governs. Absence and out-of-scope answer identically.
func (s *Store) GetRelationship(ctx context.Context, id ids.UUID) (relationshipRow, error) {
	if err := auth.Require(ctx, "relationship", principal.ActionRead); err != nil {
		return relationshipRow{}, err
	}
	var out relationshipRow
	err := s.tx(ctx, func(tx pgx.Tx) error {
		row, err := s.visibleRelationship(ctx, tx, id)
		if err != nil {
			return err
		}
		// Live only, matching every other record type's Read (which passes
		// storekit.LiveOnly). visibleRelationship deliberately does NOT filter —
		// Update locks live-only itself and Archive's own WHERE clause does the
		// work — so the filter belongs here, or an archived edge would go on
		// being served by the one verb whose whole job is to say what the record
		// currently is. Post-filtering is safe: the row already passed this
		// caller's endpoint scope, so nothing about it is disclosed by the check.
		if row.ArchivedAt != nil {
			return apperrors.ErrNotFound
		}
		out = row
		return nil
	})
	return out, err
}

// EnsureProjectVisible probes a project id under the caller's row scope —
// the project-scoped stakeholder view needs the anchor's own answer when
// the edge list is empty, so "no stakeholders yet" and "no such project"
// stay distinguishable without disclosing either.
func (s *Store) EnsureProjectVisible(ctx context.Context, projectID ids.ProjectID) error {
	if err := auth.Require(ctx, projectObjectName, principal.ActionRead); err != nil {
		return err
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		return auth.EnsureLinkTarget(ctx, tx, projectObjectName, projectID.UUID)
	})
}
