// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The controller's view of what a statutory obligation is holding
// (A165/ADR-0114 §4): what, why, until when — and never the correspondence
// itself. The audit log proves what HAPPENED; this answers what is being held
// RIGHT NOW, which is the question a supervisory authority asks.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// RestrictedRecord is one held row, stated without disclosing it: no subject,
// no body, no counterparty. Deals is EVERY transaction it qualifies through —
// a record can qualify through more than one, and naming one would be a guess
// presented as a fact.
type RestrictedRecord struct {
	ActivityID      ids.UUID
	Kind            string
	OccurredAt      time.Time
	RestrictedAt    time.Time
	RestrictedUntil time.Time
	Class           string
	RedactedFields  []string
	Deals           []QualifyingRecord
	Projects        []QualifyingRecord
}

// QualifyingRecord is one record the evidence names as having qualified the
// correspondence — a deal or a project. The name is the copy FROZEN at
// qualification (activity_retention_evidence.deal_name / project_name), so it
// still answers after the record is renamed; one that has since been deleted
// has no id to point at and is left out of the list, reported through the
// reason. The json tags are how the aggregated evidence scans out of one
// statement.
//
// One type for both because the controller is being shown the same fact in
// both cases — which record obliges us to keep this — and two identical
// structs would be two spellings of one answer.
type QualifyingRecord struct {
	ID   ids.UUID `json:"id"`
	Name string   `json:"name"`
}

// RestrictedPage is one oldest-obligation-first keyset page.
type RestrictedPage struct {
	Records    []RestrictedRecord
	NextCursor string
	HasMore    bool
}

// ListRestrictedActivities reads the held records, oldest obligation first.
//
// Gated on the retention-policy authority's READ, the same object that governs
// the ladder — held admin-only by the seeded roles. Human-only like every
// governance read beside it: which records an erasure could not destroy is not
// reconnaissance to hand an agent, even one carrying an admin's passport.
//
// The keyset is (restricted_at, id): the cursor codec's created_at slot carries
// the restriction instant, which is what the page is ordered by.
func ListRestrictedActivities(ctx context.Context, db *database.DB, cursor *string, limit *int) (RestrictedPage, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.Type != principal.PrincipalHuman {
		return RestrictedPage{}, fmt.Errorf("human-only compliance read: %w", apperrors.ErrPermissionDenied)
	}
	if err := auth.Require(ctx, retentionPolicyObject, principal.ActionRead); err != nil {
		return RestrictedPage{}, err
	}
	size := storekit.ClampLimit(limit)
	where, args, err := restrictedListWhere(cursor, size)
	if err != nil {
		return RestrictedPage{}, err
	}
	var page RestrictedPage
	err = db.Tx(ctx, func(tx pgx.Tx) error {
		// The qualifying records ride the same statement as aggregated arrays
		// rather than a query per row: a page is bounded, the evidence per
		// record is small, and one round trip keeps the page consistent with
		// itself.
		rows, err := tx.Query(ctx, `
			SELECT a.id, a.kind, a.occurred_at, a.restricted_at, a.restricted_until,
			       a.retention_class, a.redacted_fields,
			       coalesce((SELECT jsonb_agg(jsonb_build_object('id', e.deal_id, 'name', e.deal_name) ORDER BY e.deal_name)
			                   FROM (SELECT DISTINCT deal_id, deal_name FROM activity_retention_evidence
			                          WHERE activity_id = a.id AND deal_id IS NOT NULL) e), '[]'::jsonb),
			       coalesce((SELECT jsonb_agg(jsonb_build_object('id', e.project_id, 'name', e.project_name) ORDER BY e.project_name)
			                   FROM (SELECT DISTINCT project_id, project_name FROM activity_retention_evidence
			                          WHERE activity_id = a.id AND project_id IS NOT NULL) e), '[]'::jsonb)
			  FROM activity a
			 WHERE a.restricted_at IS NOT NULL `+where+`
			 ORDER BY a.restricted_at ASC, a.id ASC
			 LIMIT $`+fmt.Sprint(len(args))+``, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r RestrictedRecord
			if err := rows.Scan(&r.ActivityID, &r.Kind, &r.OccurredAt, &r.RestrictedAt, &r.RestrictedUntil,
				&r.Class, &r.RedactedFields, &r.Deals, &r.Projects); err != nil {
				return err
			}
			page.Records = append(page.Records, r)
		}
		return rows.Err()
	})
	if err != nil {
		return RestrictedPage{}, err
	}
	if len(page.Records) > size {
		page.Records = page.Records[:size]
		last := page.Records[len(page.Records)-1]
		page.NextCursor = storekit.EncodeCursor(last.RestrictedAt, last.ActivityID)
		page.HasMore = true
	}
	return page, nil
}

// restrictedListWhere renders the keyset predicate and the LIMIT arg (limit+1,
// so the page knows whether another follows). The keyset direction is
// ascending — the oldest obligation is the one nearest its end, and the one
// the controller wants to see first.
func restrictedListWhere(cursor *string, size int) (string, []any, error) {
	where := ""
	args := []any{}
	if cursor != nil && *cursor != "" {
		c, err := storekit.DecodeCursor(*cursor)
		if err != nil {
			return "", nil, err
		}
		args = append(args, c.CreatedAt, c.ID)
		where = " AND (a.restricted_at, a.id) > ($1, $2)"
	}
	args = append(args, size+1)
	return where, args, nil
}
