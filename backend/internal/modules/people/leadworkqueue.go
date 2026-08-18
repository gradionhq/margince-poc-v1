// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// leadQueueCursor is the complete keyset behind the default leads queue:
// SLA band, score, then the oldest lead. Every term in ORDER BY is carried so
// page two starts strictly after page one rather than repeating or skipping a
// lead at a shared score.
type leadQueueCursor struct {
	AsOf      time.Time `json:"as_of"`
	Rank      int       `json:"rank"`
	Score     int       `json:"score"`
	CreatedAt time.Time `json:"created_at"`
	ID        ids.UUID  `json:"id"`
}

func encodeLeadQueueCursor(cursor leadQueueCursor) (string, error) {
	raw, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode lead queue cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeLeadQueueCursor(token string) (leadQueueCursor, error) {
	var cursor leadQueueCursor
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || json.Unmarshal(raw, &cursor) != nil || cursor.AsOf.IsZero() || cursor.CreatedAt.IsZero() || cursor.ID.IsZero() {
		return cursor, &storekit.MalformedCursorError{}
	}
	if cursor.Rank < 0 || cursor.Rank > 3 || cursor.Score < 0 || cursor.Score > 100 {
		return cursor, &storekit.MalformedCursorError{}
	}
	return cursor, nil
}

// listLeadWorkQueue serves the contract's default ordering: SLA band first,
// score inside the band, then oldest lead and id for deterministic ties.
func (s *Store) listLeadWorkQueue(ctx context.Context, in ListLeadsInput) ([]crmcontracts.Lead, storekit.Page, error) {
	if err := auth.Require(ctx, leadEntity, principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	active, err := s.activeColumns(ctx, leadEntity)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	limit := storekit.ClampLimit(in.Limit)
	where, args, arg, err := leadQueueWhere(ctx, in, active)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	asOf := leadSLAClock().UTC()
	var cursor *leadQueueCursor
	if in.Cursor != nil && *in.Cursor != "" {
		decoded, err := decodeLeadQueueCursor(*in.Cursor)
		if err != nil {
			return nil, storekit.Page{}, err
		}
		cursor = &decoded
		asOf = decoded.AsOf
	}
	rank := leadQueueRank(arg, asOf)
	if cursor != nil {
		where = append(where, storekit.SQLf("("+rank+", -score, created_at, id) > ($%d, $%d, $%d, $%d)",
			arg(cursor.Rank), arg(-cursor.Score), arg(cursor.CreatedAt), arg(cursor.ID)))
	}
	query := `SELECT ` + leadColumns + storekit.SelectSuffix(active) + `, ` + rank +
		` FROM lead WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY ` + rank + `, score DESC, created_at, id` + storekit.SQLf(` LIMIT %d`, limit+1)
	return s.readLeadQueuePage(ctx, query, *args, active, limit, asOf)
}

func leadQueueWhere(ctx context.Context, in ListLeadsInput, active []fieldcatalog.Column) ([]string, *[]any, func(any) int, error) {
	args := []any{}
	arg := func(value any) int { args = append(args, value); return len(args) }
	where := []string{whereAlways}
	scope, err := auth.ScopeClauseFor(ctx, leadEntity, "", arg)
	if err != nil {
		return nil, nil, nil, err
	}
	if scope != "" {
		where = append(where, scope)
	}
	defaultSort, err := storekit.ParseListSort(nil, leadListFields)
	if err != nil {
		return nil, nil, nil, err
	}
	shared := listFilters{
		IncludeArchived: in.IncludeArchived, CapturedByKind: in.CapturedByKind,
		AiWritten: in.AiWritten, entity: leadEntity, OwnerID: in.OwnerID,
		OwnerTeamID: in.OwnerTeamID, Unassigned: in.Unassigned, Query: nil,
		nameColumn: leadNameColumn,
	}
	filters, err := shared.clauses(active, defaultSort, arg)
	if err != nil {
		return nil, nil, nil, err
	}
	where = append(where, filters...)
	if in.Query != nil && *in.Query != "" {
		where = append(where, leadQuickFindClause(*in.Query, arg))
	}
	if in.Status != nil {
		where = append(where, storekit.SQLf(leadStatusColumn+" = $%d", arg(*in.Status)))
	}
	if in.MinScore != nil {
		where = append(where, storekit.SQLf(leadScoreColumn+" >= $%d", arg(*in.MinScore)))
	}
	if in.Source != nil {
		where = append(where, storekit.SQLf(leadSourceColumn+" = $%d", arg(*in.Source)))
	}
	if in.SLAState != nil {
		where = append(where, slaStateClause(*in.SLAState, arg))
	}
	return where, &args, arg, nil
}

func leadQueueRank(arg func(any) int, asOf time.Time) string {
	minutes := int(FirstResponseTarget / time.Minute)
	risk := int(slaAtRiskWindow / time.Minute)
	deadline := storekit.SQLf("COALESCE(routed_at, created_at) + $%d * interval '1 minute'", arg(minutes))
	return storekit.SQLf(`CASE
		WHEN archived_at IS NOT NULL OR first_response_at IS NOT NULL THEN 3
		WHEN `+deadline+` < $%d THEN 0
		WHEN `+deadline+` - $%d * interval '1 minute' <= $%d THEN 1
		ELSE 2 END`, arg(asOf), arg(risk), arg(asOf))
}

func (s *Store) readLeadQueuePage(ctx context.Context, query string, args []any, active []fieldcatalog.Column, limit int, asOf time.Time) ([]crmcontracts.Lead, storekit.Page, error) {
	var leads []crmcontracts.Lead
	var ranks []int
	err := s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var rank int
			lead, err := scanLead(rows, active, &rank)
			if err != nil {
				return err
			}
			leads = append(leads, lead)
			ranks = append(ranks, rank)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, storekit.Page{}, err
	}
	page := storekit.Page{}
	if len(leads) > limit {
		leads = leads[:limit]
		last := leads[limit-1]
		next, err := encodeLeadQueueCursor(leadQueueCursor{
			AsOf: asOf, Rank: ranks[limit-1], Score: last.Score, CreatedAt: last.CreatedAt, ID: ids.UUID(last.Id),
		})
		if err != nil {
			return nil, storekit.Page{}, err
		}
		page = storekit.Page{HasMore: true, NextCursor: next}
	}
	if leads == nil {
		leads = []crmcontracts.Lead{}
	}
	return leads, page, nil
}
