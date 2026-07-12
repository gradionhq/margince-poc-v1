// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package quotas

// The attainment read (RD-FORM-2/RD-WIRE-3): Σ closed-won base value in
// the quota's period ÷ the base-converted target, decomposed per
// contributing deal so the figure is explainable, never client-summed.
// Always computed live — a zero target or a missing FX rate is an honest
// typed refusal, never a cached, capped, or invented number (RD-AC-4).
// The deal/team_membership/workspace/fx_rate SELECTs here are the
// ratified module-local read posture (decisions/0011/0015): reads are
// not ownership-gated, and RLS scopes every one to the caller's tenant.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// AttainmentDeal is one closed-won deal counted toward a quota — the
// per-deal decomposition "Explain This Number" rides on. BaseValueMinor
// is the deal's own frozen amount_minor_base (0065): already in the
// workspace base currency, never re-converted here.
type AttainmentDeal struct {
	DealID         ids.UUID
	BaseValueMinor int64
}

// Attainment is one quota's server-computed attainment (RD-FORM-2).
// Every money figure — ClosedWonMinor, TargetMinor, GapMinor — is in
// Currency, the workspace base, integer minor units: TargetMinor is the
// base-converted target (identical to Quota.target_minor whenever the
// quota is set in the base currency), so gap arithmetic never mixes
// currencies. AttainmentPct is the uncapped raw value; display capping
// is a UI concern.
type Attainment struct {
	QuotaID           ids.UUID
	ClosedWonMinor    int64
	TargetMinor       int64
	Currency          string
	AttainmentPct     float64
	GapMinor          int64
	PacePct           float64
	Band              string
	AsOfDate          time.Time
	ContributingDeals []AttainmentDeal
}

// ErrAttainmentTargetZero refuses attainment on a quota whose
// target_minor is zero — checked before any deal or FX query, so the
// refusal is a division-by-zero guard, never a partial computation. The
// transport maps it to the contract's 422 attainment_target_zero.
var ErrAttainmentTargetZero = errors.New("quota target_minor is zero; attainment is refused rather than computed against a zero denominator")

// ConvertedTargetZeroError is the same zero-denominator refusal reached
// through FX: a tiny cross-currency target can round to zero base minor
// units (round(1 × 0.4) = 0), and dividing by the EFFECTIVE target would
// answer Inf/NaN. Unwrap returns ErrAttainmentTargetZero — one invariant
// (a zero denominator refuses), so callers branch on the sentinel; the
// typed detail names the conversion because the stored target_minor
// itself is NOT zero.
type ConvertedTargetZeroError struct {
	From, To string
}

func (e *ConvertedTargetZeroError) Error() string {
	return fmt.Sprintf("quota target converts from %s to zero %s minor units at the stored fx rate; attainment is refused rather than computed against a zero denominator", e.From, e.To)
}

func (*ConvertedTargetZeroError) Unwrap() error { return ErrAttainmentTargetZero }

// ErrAttainmentComputationFailed is the honest "cannot compute" answer
// (e.g. no stored FX rate for the target's currency): the cause rides
// the wrapping message for the log, and the transport maps the sentinel
// to the contract's 422 attainment_computation_failed — never a stale or
// guessed figure.
var ErrAttainmentComputationFailed = errors.New("attainment computation failed")

// pacePct answers how far the quota period has elapsed at now, as a
// percentage (RD-PARAM-4's pace indicator): 0 before period_start, 100
// at/after period_end, linear elapsed/total between. The instant is a
// parameter, never a wall-clock read — the store's injected clock feeds
// it so a pinned test evaluates the same moment it seeded against.
func pacePct(periodStart, periodEnd, now time.Time) float64 {
	if now.Before(periodStart) {
		return 0
	}
	if !now.Before(periodEnd) {
		return 100
	}
	return now.Sub(periodStart).Seconds() / periodEnd.Sub(periodStart).Seconds() * 100
}

// attainmentBand maps an attainment percentage to its display band
// (RD-PARAM-4): met ≥ 100, accent 60–99.9…, behind < 60. Computed
// server-side once; the client never re-derives it from attainment_pct.
func attainmentBand(pct float64) string {
	switch {
	case pct >= 100:
		return "met"
	case pct >= 60:
		return "accent"
	default:
		return "behind"
	}
}

// QuotaAttainment computes the quota's live attainment in one
// workspace transaction. It carries the quota.read object gate of every
// quota read PLUS deal.read — the aggregate is built from deal sums, so
// it follows the hierarchy-rollup posture: object gates on both record
// types, per-deal row visibility deliberately not consulted (a
// workspace-level revenue figure would be dishonest if it varied by the
// caller's row scope).
func (s *Store) QuotaAttainment(ctx context.Context, id ids.UUID) (Attainment, error) {
	if err := auth.Require(ctx, "quota", principal.ActionRead); err != nil {
		return Attainment{}, err
	}
	if err := auth.Require(ctx, "deal", principal.ActionRead); err != nil {
		return Attainment{}, err
	}
	asOf := s.now().UTC()
	var (
		q          crmcontracts.Quota
		targetBase int64
		base       string
		deals      []AttainmentDeal
	)
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		// An archived quota still serves its attainment: the record stays
		// fetchable by id (the house single-get convention — see
		// GetQuota's IncludeArchived callers), and a historical period's
		// numbers remain an honest, computable read — archival hides the
		// quota from lists, it does not rewrite what was attained.
		if q, err = readQuota(ctx, tx, id, storekit.IncludeArchived); err != nil {
			return err
		}
		if q.TargetMinor == 0 {
			return ErrAttainmentTargetZero
		}
		if targetBase, base, err = targetInBase(ctx, tx, q, asOf); err != nil {
			return err
		}
		owners, err := measuredOwners(ctx, tx, q)
		if err != nil {
			return err
		}
		deals, err = contributingDeals(ctx, tx, owners, q.PeriodStart.Time, q.PeriodEnd.Time)
		return err
	})
	if err != nil {
		return Attainment{}, err
	}

	var closedWon int64
	for _, d := range deals {
		closedWon += d.BaseValueMinor
	}
	pct := float64(closedWon) / float64(targetBase) * 100
	return Attainment{
		QuotaID:           id,
		ClosedWonMinor:    closedWon,
		TargetMinor:       targetBase,
		Currency:          base,
		AttainmentPct:     pct,
		GapMinor:          closedWon - targetBase,
		PacePct:           pacePct(q.PeriodStart.Time, q.PeriodEnd.Time, asOf),
		Band:              attainmentBand(pct),
		AsOfDate:          asOf,
		ContributingDeals: deals,
	}, nil
}

// targetInBase converts the human-set target into the workspace base
// currency. The conversion runs in SQL: round() over numeric is the
// exact half-away-from-zero arithmetic deal.amount_minor_base's own
// GENERATED expression uses (0065), so target and closed-won round by
// one discipline and no float ever touches money. The as-of lookup is
// the house fx_rate read (deals.freezeFx, the rollup's fxConverter):
// latest stored rate on or before the UTC as-of day, and a missing rate
// refuses loudly — the system never invents a rate.
func targetInBase(ctx context.Context, tx pgx.Tx, q crmcontracts.Quota, asOf time.Time) (int64, string, error) {
	var base string
	if err := tx.QueryRow(ctx,
		`SELECT base_currency FROM workspace WHERE id = $1`, storekit.MustWorkspace(ctx)).Scan(&base); err != nil {
		return 0, "", fmt.Errorf("load workspace base currency: %w", err)
	}
	if q.Currency == base {
		return q.TargetMinor, base, nil
	}
	asOfDay := asOf.Format(time.DateOnly)
	var converted int64
	err := tx.QueryRow(ctx,
		`SELECT round($1::numeric * rate)::bigint FROM fx_rate
		 WHERE from_currency = $2 AND to_currency = $3 AND rate_date <= $4::date
		 ORDER BY rate_date DESC LIMIT 1`,
		q.TargetMinor, q.Currency, base, asOfDay).Scan(&converted)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", fmt.Errorf("no fx_rate from %s to %s on or before %s: %w",
			q.Currency, base, asOfDay, ErrAttainmentComputationFailed)
	}
	if err != nil {
		return 0, "", fmt.Errorf("convert quota target from %s to %s: %w", q.Currency, base, err)
	}
	if converted == 0 {
		return 0, "", &ConvertedTargetZeroError{From: q.Currency, To: base}
	}
	return converted, base, nil
}

// measuredOwners resolves whose closed-won deals count toward the quota:
// the owner-quota's single owner, or every current member of the
// team-quota's team. RLS scopes the membership read to the tenant, so
// team_id alone is the whole predicate.
func measuredOwners(ctx context.Context, tx pgx.Tx, q crmcontracts.Quota) ([]ids.UUID, error) {
	if q.OwnerId != nil {
		return []ids.UUID{ids.UUID(*q.OwnerId)}, nil
	}
	// team_id is non-nil here: the quota_owner_xor_team CHECK guarantees
	// every stored row carries exactly one of the two sides.
	rows, err := tx.Query(ctx,
		`SELECT user_id FROM team_membership WHERE team_id = $1`, ids.UUID(*q.TeamId))
	if err != nil {
		return nil, fmt.Errorf("resolve team members: %w", err)
	}
	defer rows.Close()
	var owners []ids.UUID
	for rows.Next() {
		var member ids.UUID
		if err := rows.Scan(&member); err != nil {
			return nil, err
		}
		owners = append(owners, member)
	}
	return owners, rows.Err()
}

// contributingDeals lists the closed-won live deals whose close falls in
// [period_start, period_end + 1 day) for the measured owners — the
// exclusive upper bound is computed in Go (poc-1's finding: Postgres
// cannot infer the type of a bare `$N + INTERVAL` parameter), so a deal
// closed any time ON the end date still counts. A NULL amount_minor_base
// (0065: the FX input is still missing) contributes nothing and is
// omitted from the list entirely — a zero row would misstate "this deal
// counted as 0".
func contributingDeals(ctx context.Context, tx pgx.Tx, owners []ids.UUID, periodStart, periodEnd time.Time) ([]AttainmentDeal, error) {
	out := []AttainmentDeal{}
	if len(owners) == 0 {
		// A team with no members measures nobody: an honest empty sum.
		return out, nil
	}
	rows, err := tx.Query(ctx,
		`SELECT id, amount_minor_base FROM deal
		 WHERE status = 'won' AND archived_at IS NULL
		   AND owner_id = ANY($1)
		   AND closed_at >= $2 AND closed_at < $3`,
		owners, periodStart, periodEnd.Add(24*time.Hour))
	if err != nil {
		return nil, fmt.Errorf("list contributing deals: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var d AttainmentDeal
		var baseMinor *int64
		if err := rows.Scan(&d.DealID, &baseMinor); err != nil {
			return nil, err
		}
		if baseMinor == nil {
			continue
		}
		d.BaseValueMinor = *baseMinor
		out = append(out, d)
	}
	return out, rows.Err()
}
