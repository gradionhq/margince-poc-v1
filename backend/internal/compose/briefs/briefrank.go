// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package briefs is the Morning-Brief orchestration (E05) — a compose
// subpackage under the decisions/0018 growth policy because it is a
// cross-module composition, never a module: deal facts (deals),
// relationship warmth (people §4), and the overnight activity signal
// (activities) rank into the persisted run the home surface reads.
// The deterministic ranker (this file) implements formulas-and-rules
// §10/§10.1; the pure fold it feeds is briefscore.go, the persisted
// read model briefstore.go, the advisory model re-order briefl2.go,
// and the contract transport briefhandlers.go. The composite is the
// fallback rank when the L2 layer is unavailable and the evidence basis
// every ranked item exposes (B-E05.12).
package briefs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// BriefRanking is one deterministic ranking pass: the honest-short queue
// plus the reproducibility metadata a persisted run snapshots.
type BriefRanking struct {
	Queue            []BriefQueueItem
	CandidateCount   int
	RevenueNormMinor int64
	AsOf             time.Time
}

// briefStrengthSource is the compose-injected §4 warmth seam —
// people.Store satisfies it; the brief never reaches into people's SQL.
type briefStrengthSource interface {
	PersonStrength(ctx context.Context, personID ids.UUID, now time.Time) (people.RelationshipStrength, error)
}

// BriefEngine ranks a rep's open deals and owns the brief_run/brief_item
// read model (B-E05.3b/.13). The L2 ranker (B-E05.2) is optional: without
// one the queue is the deterministic §10.1 composite order, which is also
// the AI-off fallback rank.
type BriefEngine struct {
	pool     *pgxpool.Pool
	strength briefStrengthSource
	ranker   *briefL2Ranker
	log      *slog.Logger
}

func NewBriefEngine(pool *pgxpool.Pool, strength briefStrengthSource) *BriefEngine {
	return &BriefEngine{pool: pool, strength: strength, log: slog.Default()}
}

// WithL2Ranker enables the model-bound re-order over the deterministic
// candidate set. The api role wires it from the brief_ranking model lane;
// without it the engine stays fully functional on the deterministic floor.
func (e *BriefEngine) WithL2Ranker(brain briefBrain, log *slog.Logger) *BriefEngine {
	if log == nil {
		log = slog.Default()
	}
	e.log = log
	e.ranker = &briefL2Ranker{brain: brain, log: log}
	return e
}

// briefBaseValueSQL renders the §6 base-currency value of d (joined to
// its workspace w): native amount when already in base currency, the
// frozen rate for closed deals, the latest daily rate on or before the
// as-of date for open ones. A missing rate yields NULL — the revenue
// factor floors rather than guessing (a wrong number is worse than a
// missing one). asOfPos is the bind position of the as-of date.
func briefBaseValueSQL(asOfPos int) string {
	return fmt.Sprintf(`CASE
		WHEN d.amount_minor IS NULL THEN NULL
		WHEN d.currency IS NULL OR d.currency = w.base_currency THEN d.amount_minor
		WHEN d.fx_rate_to_base IS NOT NULL THEN round(d.amount_minor * d.fx_rate_to_base)::bigint
		ELSE (SELECT round(d.amount_minor * fr.rate)::bigint FROM fx_rate fr
		      WHERE fr.from_currency = d.currency AND fr.to_currency = w.base_currency
		        AND fr.rate_date <= $%d::date
		      ORDER BY fr.rate_date DESC LIMIT 1)
	END`, asOfPos)
}

// Rank computes the deterministic §10.1 queue for the acting rep at one
// instant. It is a read: nothing is persisted (SnapshotRun does that),
// and the candidate set is bounded by the caller's own row scope — a
// rep's brief only ranks deals they can see.
func (e *BriefEngine) Rank(ctx context.Context, now time.Time) (BriefRanking, error) {
	if err := auth.Require(ctx, "deal", principal.ActionRead); err != nil {
		return BriefRanking{}, err
	}
	userID, err := briefUser(ctx)
	if err != nil {
		return BriefRanking{}, err
	}

	facts := map[ids.UUID]briefDealFacts{}
	var order []ids.UUID
	stakeholders := map[ids.UUID][]ids.UUID{}
	revenueNorm := int64(briefRevenueNormFallbackMinor)

	err = database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
		// The rep's last brief view: the previous run's data cutoff. No
		// previous run → the overnight window is all-time.
		lastView, err := briefLastView(ctx, tx, userID)
		if err != nil {
			return err
		}

		norm, err := briefRevenueNorm(ctx, tx, now)
		if err != nil {
			return err
		}
		revenueNorm = norm

		if err := briefCandidates(ctx, tx, userID, now, facts, &order); err != nil {
			return err
		}
		return briefEvidenceRows(ctx, tx, lastView, facts, order, stakeholders)
	})
	if err != nil {
		return BriefRanking{}, err
	}

	if err := e.resolveWarmth(ctx, now, facts, stakeholders); err != nil {
		return BriefRanking{}, err
	}

	scored := make([]BriefQueueItem, 0, len(order))
	for _, dealID := range order {
		scored = append(scored, briefScore(facts[dealID], revenueNorm, now))
	}

	// The deterministic floor first: the full §10.1 candidate set, ordered
	// and evidence-gated. The L2 layer re-orders WITHIN it (never below the
	// cutoff), then the honest-short truncation and the post-L2 gate close
	// over the result.
	candidates := briefCandidateOrder(scored, facts)
	if err := validateBriefCandidates(candidates); err != nil {
		return BriefRanking{}, err
	}
	ordered := candidates
	if e.ranker != nil {
		ordered = e.ranker.reorder(ctx, candidates)
	}
	queue := ordered
	if len(queue) > briefQueueTarget {
		queue = queue[:briefQueueTarget]
	}
	if err := validateBriefQueue(queue, candidates); err != nil {
		return BriefRanking{}, err
	}
	return BriefRanking{
		Queue:            queue,
		CandidateCount:   len(candidates),
		RevenueNormMinor: revenueNorm,
		AsOf:             now,
	}, nil
}

// briefUser resolves the human the brief belongs to. The brief is a
// personal lens — a principal without a user identity (the system actor)
// has no "my morning" to rank.
func briefUser(ctx context.Context) (ids.UUID, error) {
	p, ok := principal.Actor(ctx)
	if !ok {
		return ids.Nil, errors.New("brief: no actor bound to context")
	}
	if p.UserID.IsZero() {
		return ids.Nil, apperrors.ErrPermissionDenied
	}
	return p.UserID, nil
}

// briefLastView reads the previous run's data cutoff for this user; nil
// when the user never had a brief.
func briefLastView(ctx context.Context, tx pgx.Tx, userID ids.UUID) (*time.Time, error) {
	var lastView *time.Time
	err := tx.QueryRow(ctx, `
		SELECT as_of FROM brief_run
		WHERE user_id = $1
		ORDER BY generated_at DESC, id DESC
		LIMIT 1`, userID).Scan(&lastView)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return lastView, nil
}

// briefRevenueNorm computes REVENUE_NORM: the workspace P90 base deal
// value over live deals with an evidencable amount, or the fixed
// fallback below ten deals of history.
func briefRevenueNorm(ctx context.Context, tx pgx.Tx, now time.Time) (int64, error) {
	var valued int
	var p90 *float64
	err := tx.QueryRow(ctx, fmt.Sprintf(`
		WITH sized AS (
			SELECT %s AS base_value
			FROM deal d
			JOIN workspace w ON w.id = d.workspace_id
			WHERE d.archived_at IS NULL
		)
		SELECT count(*), percentile_cont(%v) WITHIN GROUP (ORDER BY base_value::double precision)
		FROM sized WHERE base_value IS NOT NULL`,
		briefBaseValueSQL(1), briefRevenueNormPercentile), now.UTC()).Scan(&valued, &p90)
	if err != nil {
		return 0, err
	}
	if valued < briefRevenueNormMinDeals || p90 == nil || *p90 <= 0 {
		return briefRevenueNormFallbackMinor, nil
	}
	return int64(math.Round(*p90)), nil
}

// briefCandidates gathers the open, row-scoped candidate deals, minus
// the ones this user acted on or dismissed with no linked activity since
// the mark (B-E05.13: a dismissed deal reappears only when it materially
// changed; an unchanged one stays out — across ALL previous runs, not
// just the last).
func briefCandidates(ctx context.Context, tx pgx.Tx, userID ids.UUID, now time.Time, facts map[ids.UUID]briefDealFacts, order *[]ids.UUID) error {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	asOfPos := arg(now.UTC())
	userPos := arg(userID)

	scope, err := auth.ScopeClauseFor(ctx, "deal", "d", arg)
	if err != nil {
		return err
	}
	q := fmt.Sprintf(`
		SELECT d.id, s.win_probability, %s, d.expected_close_date
		FROM deal d
		JOIN stage s ON s.id = d.stage_id
		JOIN workspace w ON w.id = d.workspace_id
		WHERE d.archived_at IS NULL AND d.status = 'open'
		  AND NOT EXISTS (
			SELECT 1 FROM brief_item bi
			JOIN brief_run br ON br.id = bi.brief_run_id
			WHERE br.user_id = $%d AND bi.deal_id = d.id AND bi.state <> 'new'
			  AND NOT EXISTS (
				SELECT 1 FROM activity a
				JOIN activity_link l ON l.activity_id = a.id AND l.deal_id = d.id
				WHERE a.archived_at IS NULL AND a.occurred_at > bi.state_at))`,
		briefBaseValueSQL(asOfPos), userPos)
	if scope != "" {
		q += " AND " + scope
	}
	q += " ORDER BY d.id"

	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var f briefDealFacts
		if err := rows.Scan(&f.dealID, &f.winProbability, &f.baseValueMinor, &f.expectedClose); err != nil {
			return err
		}
		facts[f.dealID] = f
		*order = append(*order, f.dealID)
	}
	return rows.Err()
}

// briefEvidenceRows gathers each candidate's overnight activities (the
// momentum evidence) and stakeholder persons, after the candidate rows
// are drained (one connection, one active query).
func briefEvidenceRows(ctx context.Context, tx pgx.Tx, lastView *time.Time, facts map[ids.UUID]briefDealFacts, order []ids.UUID, stakeholders map[ids.UUID][]ids.UUID) error {
	for _, dealID := range order {
		f := facts[dealID]
		overnight, err := collectIDList(tx.Query(ctx, `
			SELECT a.id FROM activity a
			JOIN activity_link l ON l.activity_id = a.id AND l.deal_id = $1
			WHERE a.archived_at IS NULL
			  AND ($2::timestamptz IS NULL OR a.occurred_at > $2)
			ORDER BY a.occurred_at DESC, a.id DESC
			LIMIT $3`, dealID, lastView, briefOvernightEvidenceCap))
		if err != nil {
			return err
		}
		f.overnightActivityIDs = overnight
		facts[dealID] = f

		persons, err := collectIDList(tx.Query(ctx, `
			SELECT r.person_id FROM relationship r
			WHERE r.kind = 'deal_stakeholder' AND r.deal_id = $1 AND r.archived_at IS NULL
			ORDER BY r.person_id`, dealID))
		if err != nil {
			return err
		}
		stakeholders[dealID] = persons
	}
	return nil
}

// resolveWarmth fills each deal's warmth from its strongest visible
// stakeholder through the injected §4 seam. A stakeholder outside the
// caller's row scope — or a caller with no person grant at all —
// contributes nothing: the warmth factor floors instead of out-seeing
// the people list.
func (e *BriefEngine) resolveWarmth(ctx context.Context, now time.Time, facts map[ids.UUID]briefDealFacts, stakeholders map[ids.UUID][]ids.UUID) error {
	cache := map[ids.UUID]people.RelationshipStrength{}
	for dealID, persons := range stakeholders {
		f := facts[dealID]
		for _, personID := range persons {
			st, ok := cache[personID]
			if !ok {
				var err error
				st, err = e.strength.PersonStrength(ctx, personID, now)
				switch {
				case errors.Is(err, apperrors.ErrNotFound), errors.Is(err, apperrors.ErrPermissionDenied):
					// Invisible to this caller: no strength to disclose.
					st = people.RelationshipStrength{}
				case err != nil:
					return err
				}
				cache[personID] = st
			}
			if st.Strength > f.warmthStrength {
				f.warmthStrength = st.Strength
				f.warmthEvidence = st.ContributingIDs
			}
		}
		facts[dealID] = f
	}
	return nil
}

// collectIDList drains a single-uuid-column result set (the compose
// spelling of the modules' collectIDs helpers).
func collectIDList(rows pgx.Rows, err error) ([]ids.UUID, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ids.UUID
	for rows.Next() {
		var id ids.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
