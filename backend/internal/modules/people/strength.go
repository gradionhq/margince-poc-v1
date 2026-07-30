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
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// bucketNone is the display bucket for a relationship with no
// qualifying interaction at all — shown as "no interactions yet", never
// as a number.
const bucketNone = "none"

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
	ContributingIDs     []ids.ActivityID
}

// strengthKinds are the qualifying interaction kinds (§4 inputs).
const strengthKinds = `('email','call','meeting')`

// PersonStrength computes the §4 baseline for one person. The person
// read is row-scoped exactly like GetPerson: a person the caller cannot
// see has no strength to disclose.
func (s *Store) PersonStrength(ctx context.Context, personID ids.PersonID, now time.Time) (RelationshipStrength, error) {
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return RelationshipStrength{}, err
	}
	var out RelationshipStrength
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "person", personID.UUID); err != nil {
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

// AccountStrength is the §4 org roll-up: the strongest current contact's
// score, which contact carries it, and how many contacts it was chosen
// from. The two extra facts exist because the number alone is not
// actionable on an account — the rep needs to know whose relationship it is.
type AccountStrength struct {
	RelationshipStrength
	// ContributorPersonID names the contact whose relationship the score is.
	// It is nil when there is no relationship to attribute: no contact the
	// caller can read, or none of them has ever interacted. A dormant
	// account has no carrier, and inventing one would read as a claim.
	ContributorPersonID *ids.PersonID
	ContactCount        int
}

// OrganizationStrength is the §4 org roll-up: the MAX over the org's
// current employees' strengths — one strong relationship makes the
// account warm; an average would dilute it. A contact outside the caller's
// row scope contributes nothing, so the roll-up never out-sees the contact
// list.
func (s *Store) OrganizationStrength(ctx context.Context, orgID ids.OrganizationID, now time.Time) (AccountStrength, error) {
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		return AccountStrength{}, err
	}
	var out AccountStrength
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "organization", orgID.UUID); err != nil {
			return err
		}
		var err error
		out, err = AccountStrengthFor(ctx, tx, orgID, now)
		return err
	})
	if err != nil {
		return AccountStrength{}, err
	}
	return out, nil
}

// AccountStrengthFor is OrganizationStrength's body without the
// transaction or the organization gate, so a composite read that already
// opened one transaction and already gated the account computes the same
// roll-up inside it rather than opening a second one at a second instant.
func AccountStrengthFor(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, now time.Time) (AccountStrength, error) {
	contacts, err := StrengthForOrgContacts(ctx, tx, orgID, now)
	if errors.Is(err, apperrors.ErrPermissionDenied) {
		// A caller holding organization:read but not person:read sees an
		// account with no contacts they may read, so the roll-up is dormant
		// with nobody behind it. Refusing here instead would newly 403 a
		// route that has answered this shape since it shipped.
		return AccountStrength{RelationshipStrength: RelationshipStrength{Bucket: bucketNone}}, nil
	}
	if err != nil {
		return AccountStrength{}, err
	}
	return FoldAccountStrength(contacts), nil
}

// FoldAccountStrength picks the strongest contact out of an already-read
// contact set, so a caller that needs both the per-contact scores and the
// account roll-up pays for the underlying read once.
//
// Picking the strongest: A contributor is named
// only when there is a relationship to attribute: an account whose contacts
// have never interacted is dormant, and naming one of them as the carrier
// of a zero would invent a relationship that does not exist.
func FoldAccountStrength(contacts []ContactStrength) AccountStrength {
	out := AccountStrength{
		RelationshipStrength: RelationshipStrength{Bucket: bucketNone},
		ContactCount:         len(contacts),
	}
	for i := range contacts {
		c := contacts[i]
		if c.Strength.LastInteraction == nil {
			continue
		}
		if out.ContributorPersonID != nil && c.Strength.Strength <= out.Strength {
			continue
		}
		out.RelationshipStrength = c.Strength
		personID := c.PersonID
		out.ContributorPersonID = &personID
	}
	return out
}

// StrengthToWire renders a §4 result onto the contract's shared
// RelationshipStrength. It lives with the computation, not beside one of
// its transports: the per-person route, the per-organization route and the
// company view all answer this shape, and a bucket rename made in only one
// of three places is the drift this prevents.
//
// factors.direction has no dedicated domain field — the §4 computation
// derives it internally on the way to reciprocity — so it is recomputed
// here from the same two counts rather than invented.
func StrengthToWire(rs RelationshipStrength, now time.Time) crmcontracts.RelationshipStrength {
	inbound, outbound := rs.Inbound90d, rs.Outbound90d
	direction := 0.0
	if directed := inbound + outbound; directed > 0 {
		direction = 1 - math.Abs(float64(inbound-outbound))/float64(directed)
	}
	contributing := make([]openapi_types.UUID, len(rs.ContributingIDs))
	for i, activityID := range rs.ContributingIDs {
		contributing[i] = openapi_types.UUID(activityID.UUID)
	}
	computedAt := now
	wire := crmcontracts.RelationshipStrength{
		Score:                   rs.Strength,
		Bucket:                  StrengthBucketToWire(rs.Bucket),
		LastInteraction:         rs.LastInteraction,
		ComputedAt:              &computedAt,
		Inbound90d:              &inbound,
		Outbound90d:             &outbound,
		ContributingActivityIds: &contributing,
	}
	wire.Factors.Recency = float32(rs.Recency)
	wire.Factors.Frequency = float32(rs.Frequency)
	wire.Factors.Reciprocity = float32(rs.Reciprocity)
	wire.Factors.Direction = float32(direction)
	return wire
}

// StrengthBucketToWire maps the domain's display bucket onto the contract
// vocabulary. The domain emits only the four cases below; anything else
// reads as dormant rather than as a wire value the enum never declared.
func StrengthBucketToWire(bucket string) crmcontracts.RelationshipStrengthBucket {
	switch bucket {
	case "weak":
		return crmcontracts.RelationshipStrengthBucketWeak
	case "moderate":
		return crmcontracts.RelationshipStrengthBucketWarm
	case "strong":
		return crmcontracts.RelationshipStrengthBucketStrong
	default: // bucketNone
		return crmcontracts.RelationshipStrengthBucketDormant
	}
}

// ContactStrength pairs one of an organization's current contacts with
// that contact's §4 score.
type ContactStrength struct {
	PersonID ids.PersonID
	Strength RelationshipStrength
}

// StrengthForOrgContacts computes §4 for every current employee of one
// organization that the caller can read, inside the caller's OWN
// transaction and in a fixed number of queries — two, regardless of how
// many contacts the account has.
//
// It exists because the per-person path opens a transaction each: the
// company view needs a score beside every contact, and doing that through
// PersonStrength would open one transaction per row and read a different
// instant for each of them. The caller has already gated the organization;
// what this adds is the person row scope, applied here as a predicate so a
// contact the caller may not read contributes nothing and is not named.
//
// The results come back in the order the contacts sort by id, so a page
// built from them is deterministic.
func StrengthForOrgContacts(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, now time.Time) ([]ContactStrength, error) {
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return nil, err
	}
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	orgPos := arg(orgID)
	scope, err := auth.ScopeClauseFor(ctx, "person", "p", arg)
	if err != nil {
		return nil, err
	}
	if scope == "" {
		scope = "TRUE"
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT p.id FROM person p
		JOIN relationship r ON r.person_id = p.id
		WHERE r.kind = 'employment' AND r.organization_id = $%d
		  AND r.ended_at IS NULL AND r.archived_at IS NULL
		  AND p.archived_at IS NULL AND (%s)
		ORDER BY p.id`, orgPos, scope), args...)
	if err != nil {
		return nil, err
	}
	contacts, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ids.PersonID, error) {
		var id ids.PersonID
		err := row.Scan(&id)
		return id, err
	})
	if err != nil {
		return nil, err
	}
	if len(contacts) == 0 {
		return nil, nil
	}
	return contactStrengths(ctx, tx, contacts, now)
}

// contactStrengths folds the §4 inputs for a whole contact set out of ONE
// grouped pass over their qualifying activities. The evidence ids are
// deliberately NOT collected here: they are the person page's receipts, and
// carrying up to relStrengthEvidenceCap of them per contact would make an
// account list payload grow with its history rather than its contact count.
func contactStrengths(ctx context.Context, tx pgx.Tx, contacts []ids.PersonID, now time.Time) ([]ContactStrength, error) {
	windowStart := now.AddDate(0, 0, -relStrengthWindowDays)
	rows, err := tx.Query(ctx, `
		SELECT l.person_id,
		       max(a.occurred_at),
		       count(*) FILTER (WHERE a.occurred_at >= $2),
		       count(*) FILTER (WHERE a.occurred_at >= $2 AND a.direction = 'inbound'),
		       count(*) FILTER (WHERE a.occurred_at >= $2 AND a.direction = 'outbound')
		FROM activity a
		JOIN activity_link l ON l.activity_id = a.id
		WHERE l.person_id = ANY($1) AND a.kind IN `+strengthKinds+` AND a.archived_at IS NULL
		GROUP BY l.person_id`, contacts, windowStart)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byPerson := make(map[ids.PersonID]*RelationshipStrength, len(contacts))
	for rows.Next() {
		var personID ids.PersonID
		var rs RelationshipStrength
		if err := rows.Scan(&personID, &rs.LastInteraction, &rs.InteractionCount90d,
			&rs.Inbound90d, &rs.Outbound90d); err != nil {
			return nil, err
		}
		byPerson[personID] = &rs
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]ContactStrength, 0, len(contacts))
	for _, personID := range contacts {
		// A contact with no qualifying interaction is still a contact: it
		// carries the honest "none" bucket, never a missing row.
		rs := RelationshipStrength{Bucket: bucketNone}
		if found, ok := byPerson[personID]; ok {
			rs = *found
		}
		rs.finish(now)
		out = append(out, ContactStrength{PersonID: personID, Strength: rs})
	}
	return out, nil
}

func strengthInputs(ctx context.Context, tx pgx.Tx, personID ids.PersonID, now time.Time, out *RelationshipStrength) error {
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
		var id ids.ActivityID
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
		r.Bucket = bucketNone
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
