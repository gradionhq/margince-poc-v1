// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// "Explain This Score" (AC-S7, ADR-0105/A156): the read side of the
// retained series.
//
// Three rules this file exists to hold, each of which produces a lie if
// it is dropped:
//
//   - The breakdown is READ, never recomputed. Behavioral factors decay
//     continuously, so a fresh computation explains a number the record
//     does not carry.
//   - Under a Commercial Judgement override the factors explain the
//     MACHINE value, not the human's. The response names both.
//   - Source activities are re-read through the CALLER's scope, not the
//     system-side list the recompute walked.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	openapitypes "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ExplainLeadScoreInput selects the current explanation or the series.
type ExplainLeadScoreInput struct {
	History bool
	Cursor  string
	Limit   int
}

type scoreHistoryRow struct {
	ID             ids.UUID
	Score          int
	ScoreComputed  int
	OverrideReason *string
	RawSum         float64
	RoundedSum     int
	Factors        []ScoreFactor
	ComputedAt     time.Time
}

// ExplainLeadScore answers with the decomposition behind a lead's score.
func (s *Store) ExplainLeadScore(ctx context.Context, leadID ids.LeadID, in ExplainLeadScoreInput) (crmcontracts.LeadScoreExplanation, error) {
	if err := auth.Require(ctx, "lead", principal.ActionRead); err != nil {
		return crmcontracts.LeadScoreExplanation{}, err
	}
	var out crmcontracts.LeadScoreExplanation
	err := s.tx(ctx, func(tx pgx.Tx) error {
		// The lead's own gate first: a lead the caller may not see answers
		// 404 here exactly as it does on the record read, so the score never
		// becomes a side channel onto a hidden lead's existence.
		if err := auth.EnsureVisibleLive(ctx, tx, "lead", leadID.UUID); err != nil {
			return err
		}
		var displayed int
		if err := tx.QueryRow(ctx, `SELECT score FROM lead WHERE id = $1`, leadID).Scan(&displayed); err != nil {
			return fmt.Errorf("read lead score: %w", err)
		}
		out.Score = displayed

		rows, hasMore, err := readLeadScoreHistory(ctx, tx, leadID, in)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			// Not an error and not an empty breakdown: a lead scored before
			// the series existed has no entry yet, and an empty factor list
			// would read as "nothing contributed" (ADR-0105 §1). The first
			// recompute fills it; nothing fabricates one after the fact.
			out.Explained = false
			return nil
		}
		out.Explained = true
		for i := range rows {
			if err := filterFactorSources(ctx, tx, &rows[i]); err != nil {
				return err
			}
		}
		if !in.History {
			entry := wireScoreEntry(rows[0])
			out.Current = &entry
			return nil
		}
		entries := make([]crmcontracts.LeadScoreEntry, 0, len(rows))
		for _, row := range rows {
			entries = append(entries, wireScoreEntry(row))
		}
		out.History = &entries
		page := crmcontracts.PageInfo{HasMore: hasMore}
		if hasMore {
			last := rows[len(rows)-1]
			next := storekit.EncodeCursor(last.ComputedAt, last.ID)
			page.NextCursor = &next
		}
		out.Page = &page
		return nil
	})
	if err != nil {
		return crmcontracts.LeadScoreExplanation{}, err
	}
	return out, nil
}

// readLeadScoreHistory reads the newest entry, or one keyset page of the
// series. The page is fetched one row over its limit so has_more is a fact
// about the table rather than a guess from a full page.
func readLeadScoreHistory(ctx context.Context, tx pgx.Tx, leadID ids.LeadID, in ExplainLeadScoreInput) ([]scoreHistoryRow, bool, error) {
	limit := 1
	if in.History {
		limit = in.Limit
	}
	args := []any{leadID, limit + 1}
	where := "lead_id = $1"
	if in.History && in.Cursor != "" {
		cursor, err := storekit.DecodeCursor(in.Cursor)
		if err != nil {
			return nil, false, err
		}
		// Newest first, so a later page is everything ORDER-BY-lower than
		// where the last one stopped. The id breaks ties on a shared
		// timestamp — two recomputes inside the same instant are ordinary.
		args = append(args, cursor.CreatedAt, cursor.ID)
		where += " AND (computed_at, id) < ($3, $4)"
	}
	rows, err := tx.Query(ctx,
		`SELECT id, score, score_computed, override_reason, raw_sum, rounded_sum, factors, computed_at
		   FROM lead_score_history
		  WHERE `+where+`
		  ORDER BY computed_at DESC, id DESC
		  LIMIT $2`, args...)
	if err != nil {
		return nil, false, fmt.Errorf("read lead score history: %w", err)
	}
	defer rows.Close()
	var out []scoreHistoryRow
	for rows.Next() {
		var row scoreHistoryRow
		var encoded []byte
		if err := rows.Scan(&row.ID, &row.Score, &row.ScoreComputed, &row.OverrideReason,
			&row.RawSum, &row.RoundedSum, &encoded, &row.ComputedAt); err != nil {
			return nil, false, fmt.Errorf("scan lead score history: %w", err)
		}
		if err := json.Unmarshal(encoded, &row.Factors); err != nil {
			return nil, false, fmt.Errorf("decode lead score factors: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	return out, hasMore, nil
}

// filterFactorSources re-reads each factor's source activities through the
// CALLER's scope. The recompute that stored them ran system-side with no
// viewer, and a system-side list handed back verbatim is not a reader's
// list. Under the current ANY-link rule this removes nothing — a caller
// who may read the lead may open every activity linked to it — but the
// gate is applied rather than assumed, because that rule is not this
// module's to depend on.
//
// A factor whose sources are ALL invisible is dropped entirely, points and
// all: a bare count beside a factor name discloses that a message exists
// and roughly when, which is what existence-hiding withholds (ADR-0105 §3).
func filterFactorSources(ctx context.Context, tx pgx.Tx, row *scoreHistoryRow) error {
	kept := make([]ScoreFactor, 0, len(row.Factors))
	for _, factor := range row.Factors {
		if len(factor.SourceActivityIDs) == 0 {
			kept = append(kept, factor) // a fit or manual factor cites no activity
			continue
		}
		visible, err := visibleActivityIDs(ctx, tx, factor.SourceActivityIDs)
		if err != nil {
			return err
		}
		if len(visible) == 0 {
			continue
		}
		factor.SourceActivityIDs = visible
		kept = append(kept, factor)
	}
	row.Factors = kept
	return nil
}

// visibleActivityIDs narrows a stored id list to what this caller may read.
func visibleActivityIDs(ctx context.Context, tx pgx.Tx, want []ids.ActivityID) ([]ids.ActivityID, error) {
	args := []any{want}
	arg := func(v any) int {
		args = append(args, v)
		return len(args)
	}
	scope, err := auth.ActivityDiscoverClause(ctx, "a", arg)
	if err != nil {
		return nil, err
	}
	where := []string{"a.id = ANY($1)", "a.archived_at IS NULL"}
	if scope != "" {
		where = append(where, scope)
	}
	rows, err := tx.Query(ctx,
		`SELECT a.id FROM activity a WHERE `+strings.Join(where, " AND "), args...)
	if err != nil {
		return nil, fmt.Errorf("filter score sources: %w", err)
	}
	defer rows.Close()
	var out []ids.ActivityID
	for rows.Next() {
		var id ids.ActivityID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan score source: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// wireScoreEntry maps one retained point onto the contract.
func wireScoreEntry(row scoreHistoryRow) crmcontracts.LeadScoreEntry {
	factors := make([]crmcontracts.LeadScoreFactor, 0, len(row.Factors))
	for _, f := range row.Factors {
		wire := crmcontracts.LeadScoreFactor{Factor: f.Factor, Points: float32(f.Points)}
		if f.BasePoints != 0 {
			base := float32(f.BasePoints)
			wire.BasePoints = &base
		}
		if len(f.SourceActivityIDs) > 0 {
			sources := make([]openapitypes.UUID, 0, len(f.SourceActivityIDs))
			for _, id := range f.SourceActivityIDs {
				sources = append(sources, openapitypes.UUID(id.UUID))
			}
			wire.SourceActivityIds = &sources
		}
		factors = append(factors, wire)
	}
	entry := crmcontracts.LeadScoreEntry{
		Score:         row.Score,
		ScoreComputed: row.ScoreComputed,
		RawSum:        float32(row.RawSum),
		RoundedSum:    row.RoundedSum,
		ComputedAt:    row.ComputedAt,
		Factors:       &factors,
	}
	entry.OverrideReason = row.OverrideReason
	return entry
}

// UnknownLeadFactorError names a factor outside the §24 catalog. The
// catalog is closed by construction: a new factor is a model change, not a
// value a caller may invent.
type UnknownLeadFactorError struct{ Factor string }

func (e *UnknownLeadFactorError) Error() string {
	return "no scoring factor named " + e.Factor
}

// FieldFault refuses a factor the model does not know.
func (e *UnknownLeadFactorError) FieldFault() (field, code, message string) {
	return fieldKeyFactor, "unknown_factor", e.Error()
}

// InvalidLeadBandError refuses a band the named factor does not accept.
type InvalidLeadBandError struct{ Factor, Band string }

func (e *InvalidLeadBandError) Error() string {
	return e.Factor + " does not take the band " + e.Band
}

// FieldFault names the band as the input to fix.
func (e *InvalidLeadBandError) FieldFault() (field, code, message string) {
	return "band", "invalid_band", e.Error()
}

// FactorAutoSourcedError refuses a hand-entered value for a factor the
// model now fetches. The auto value wins (ADR-0105 §4), and a rep who
// disagrees with a fetched fact is overruling the model — which is what
// the Commercial Judgement score override is for.
type FactorAutoSourcedError struct{ Factor string }

func (e *FactorAutoSourcedError) Error() string {
	return e.Factor + " is fetched automatically now; override the score instead of the factor"
}

// FieldFault names the factor and points at the override path.
func (e *FactorAutoSourcedError) FieldFault() (field, code, message string) {
	return fieldKeyFactor, "factor_auto_sourced", e.Error()
}
