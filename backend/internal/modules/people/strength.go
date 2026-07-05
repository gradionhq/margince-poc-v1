// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Relationship strength (formulas-and-rules §4, B-E13.16): one
// deterministic recency × frequency × reciprocity function over captured
// interactions — never predictive ML. The score decomposes exactly to
// its three named factors (P6 "no mystery number") and reads person +
// activity ONLY: leads never contribute (ADR-0008 — a lead-linked
// activity carries lead_id, not person_id, so exclusion is structural
// and the tests pin it).

import (
	"context"
	"math"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// §4 tunables (spec parameter registry names in comments).
const (
	relStrengthHalfLifeDays     = 30.0 // RELSTRENGTH_HALFLIFE_DAYS
	relStrengthFreqSaturation   = 20.0 // RELSTRENGTH_FREQ_SATURATION
	relStrengthReciprocityFloor = 0.25 // RELSTRENGTH_RECIPROCITY_FLOOR
	relStrengthWindowDays       = 90   // frequency/reciprocity window
	// relStrengthEvidenceCap bounds the contributing-ids payload; the
	// factors are computed over the FULL window regardless.
	relStrengthEvidenceCap = 200
)

// RelationshipStrength is the explainable §4 output: the 0–100 score,
// its display bucket, the three factors it reconciles to, and the
// contributing activity ids (clickable, "no mystery number").
type RelationshipStrength struct {
	Strength int
	Bucket   string // weak | moderate | strong | none (no interactions yet)

	Recency     float64
	Frequency   float64
	Reciprocity float64

	LastInteraction     *time.Time
	InteractionCount90d int
	Inbound90d          int
	Outbound90d         int
	ContributingIDs     []ids.UUID
}

// strengthKinds are the qualifying interaction kinds (§4 inputs).
const strengthKinds = `('email','call','meeting')`

// PersonStrength computes the §4 baseline for one person. The person
// read is row-scoped exactly like GetPerson: a person the caller cannot
// see has no strength to disclose.
func (s *Store) PersonStrength(ctx context.Context, personID ids.UUID, now time.Time) (RelationshipStrength, error) {
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return RelationshipStrength{}, err
	}
	var out RelationshipStrength
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "person", personID); err != nil {
			return err
		}
		return strengthInputs(ctx, tx, personID, now, &out)
	})
	if err != nil {
		return RelationshipStrength{}, err
	}
	out.finish(now)
	return out, nil
}

// OrganizationStrength is the §4 org roll-up: the MAX over the org's
// current employees' strengths — one strong relationship makes the
// account warm; an average would dilute it.
func (s *Store) OrganizationStrength(ctx context.Context, orgID ids.UUID, now time.Time) (RelationshipStrength, error) {
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		return RelationshipStrength{}, err
	}
	var people []ids.UUID
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "organization", orgID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT person_id FROM relationship
			WHERE kind = 'employment' AND organization_id = $1
			  AND ended_at IS NULL AND archived_at IS NULL`, orgID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id ids.UUID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			people = append(people, id)
		}
		return rows.Err()
	})
	if err != nil {
		return RelationshipStrength{}, err
	}
	best := RelationshipStrength{Bucket: "none"}
	for _, personID := range people {
		st, err := s.PersonStrength(ctx, personID, now)
		if err != nil {
			// A person outside the caller's row scope contributes nothing
			// — the roll-up must not out-see the person list.
			continue
		}
		if st.Strength > best.Strength || best.LastInteraction == nil {
			best = st
		}
	}
	return best, nil
}

func strengthInputs(ctx context.Context, tx pgx.Tx, personID ids.UUID, now time.Time, out *RelationshipStrength) error {
	windowStart := now.AddDate(0, 0, -relStrengthWindowDays)

	// One pass over the person's qualifying interactions: overall last
	// touch, the 90-day direction counts, and the contributing ids.
	if err := tx.QueryRow(ctx, `
		SELECT max(a.occurred_at),
		       count(*) FILTER (WHERE a.occurred_at >= $2),
		       count(*) FILTER (WHERE a.occurred_at >= $2 AND a.direction = 'inbound'),
		       count(*) FILTER (WHERE a.occurred_at >= $2 AND a.direction = 'outbound')
		FROM activity a
		JOIN activity_link l ON l.activity_id = a.id AND l.person_id = $1
		WHERE a.kind IN `+strengthKinds+` AND a.archived_at IS NULL`,
		personID, windowStart).Scan(&out.LastInteraction, &out.InteractionCount90d, &out.Inbound90d, &out.Outbound90d); err != nil {
		return err
	}

	rows, err := tx.Query(ctx, `
		SELECT a.id FROM activity a
		JOIN activity_link l ON l.activity_id = a.id AND l.person_id = $1
		WHERE a.kind IN `+strengthKinds+` AND a.archived_at IS NULL AND a.occurred_at >= $2
		ORDER BY a.occurred_at DESC
		LIMIT $3`, personID, windowStart, relStrengthEvidenceCap)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id ids.UUID
		if err := rows.Scan(&id); err != nil {
			return err
		}
		out.ContributingIDs = append(out.ContributingIDs, id)
	}
	return rows.Err()
}

// finish folds the gathered inputs through the §4 formula.
func (r *RelationshipStrength) finish(now time.Time) {
	if r.LastInteraction == nil {
		// No interactions: undefined → 0, shown as "no interactions yet",
		// never as a number.
		r.Bucket = "none"
		return
	}
	days := now.Sub(*r.LastInteraction).Hours() / 24
	if days < 0 {
		days = 0
	}
	r.Recency = math.Exp2(-days / relStrengthHalfLifeDays)
	r.Frequency = math.Min(1.0, float64(r.InteractionCount90d)/relStrengthFreqSaturation)
	directed := r.Inbound90d + r.Outbound90d
	balance := 0.0
	if directed > 0 {
		balance = 1 - math.Abs(float64(r.Inbound90d-r.Outbound90d))/float64(directed)
	}
	r.Reciprocity = relStrengthReciprocityFloor + (1-relStrengthReciprocityFloor)*balance
	r.Strength = int(math.Round(100 * r.Recency * r.Frequency * r.Reciprocity))
	switch {
	case r.Strength >= 60:
		r.Bucket = "strong"
	case r.Strength >= 25:
		r.Bucket = "moderate"
	default:
		r.Bucket = "weak"
	}
}
