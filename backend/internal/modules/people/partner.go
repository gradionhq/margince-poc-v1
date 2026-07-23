// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The partner extension (A41/ADR-0032): an organization promoted to a
// first-class partner. Identity is never duplicated — partner is a
// one-to-one extension row, and upserting it flips the org's
// classification; the org's own .updated event carries the change.

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

const partnerColumns = `organization_id, cert_status, partner_role, margin_tier,
	certified_staff, retention_rate, relationship_stage, next_step, next_step_due_at,
	served_segments, partner_fit_score, partner_fit_score_computed,
	partner_fit_override_reason, relationship_health::text, last_contact_at,
	version, created_at, updated_at, archived_at`

type partnerRow struct {
	OrganizationID    ids.OrganizationID
	CertStatus        string
	PartnerRole       *string
	MarginTier        *string
	CertifiedStaff    int16
	RetentionRate     *int16
	RelationshipStage string
	NextStep          *string
	NextStepDueAt     *time.Time
	ServedSegments    []string
	// The A68/ADR-0053 Commercial Judgement pair (formulas §17): a non-nil
	// FitOverrideReason marks FitScore human-set; the machine value is then
	// retained in FitScoreComputed instead of overwriting FitScore.
	FitScore           *int16
	FitScoreComputed   *int16
	FitOverrideReason  *string
	RelationshipHealth *string // numeric(3,2) rendered ::text — no float touches it
	LastContactAt      *time.Time
	Version            int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
	ArchivedAt         *time.Time
}

func scanPartner(r pgx.Row) (partnerRow, error) {
	var p partnerRow
	err := r.Scan(&p.OrganizationID, &p.CertStatus, &p.PartnerRole, &p.MarginTier,
		&p.CertifiedStaff, &p.RetentionRate, &p.RelationshipStage, &p.NextStep, &p.NextStepDueAt,
		&p.ServedSegments, &p.FitScore, &p.FitScoreComputed, &p.FitOverrideReason,
		&p.RelationshipHealth, &p.LastContactAt, &p.Version, &p.CreatedAt, &p.UpdatedAt, &p.ArchivedAt)
	return p, err
}

type UpsertPartnerInput struct {
	OrganizationID    ids.OrganizationID
	PartnerRole       string
	CertStatus        *string
	MarginTier        *string
	CertifiedStaff    *int16
	RetentionRate     *int16
	RelationshipStage *string
	NextStep          *string
	NextStepDueAt     *time.Time
	ServedSegments    *[]string
	// The fit-override pair is store-seam only: the contract exposes
	// partner_fit_score/…_override_reason read-only, so no handler maps
	// them — the invariant is pinned here for the future fit engine.
	FitScore *int16
	// FitOverrideReason is tri-state, mirroring the lead-score pair: nil =
	// no override change; non-nil empty = the explicit CLEAR gesture;
	// non-nil non-empty = the written reason for a human fit score.
	FitOverrideReason *string
	IfVersion         *int64
}

// PartnerFitOverrideReasonRequiredError rejects a human partner-fit score
// with no written reason — the Commercial Judgement rule (formulas §17,
// A68/ADR-0053): an override is auditable or it does not happen.
type PartnerFitOverrideReasonRequiredError struct{}

func (e *PartnerFitOverrideReasonRequiredError) Error() string {
	return "a partner_fit_score override requires a non-empty partner_fit_override_reason"
}

// partnerFitState is the override pair's column triple as one value.
type partnerFitState struct {
	Score          *int16
	Computed       *int16
	OverrideReason *string
}

// applyPartnerFitOverride folds the §17 sticky-override rules — the exact
// sibling of the lead-score applyScoreOverride: setting a score demands a
// written reason and retains the machine value in Computed the first
// time; an explicit empty reason clears the override and Score reverts to
// the retained machine value (recompute, once the fit engine exists,
// resumes from there); a non-empty reason with no score amends the note
// on an override already in force.
func applyPartnerFitOverride(current partnerFitState, score *int16, reason *string) (partnerFitState, error) {
	overrideInForce := current.OverrideReason != nil

	switch {
	case score != nil:
		trimmed := ""
		if reason != nil {
			trimmed = strings.TrimSpace(*reason)
		}
		if trimmed == "" {
			return partnerFitState{}, &PartnerFitOverrideReasonRequiredError{}
		}
		next := partnerFitState{Score: score, OverrideReason: &trimmed, Computed: current.Computed}
		if !overrideInForce {
			next.Computed = current.Score
		}
		return next, nil

	case reason != nil && strings.TrimSpace(*reason) == "":
		if !overrideInForce {
			return current, nil // no override to clear — a no-op
		}
		return partnerFitState{Score: current.Computed}, nil

	case reason != nil:
		if !overrideInForce {
			return partnerFitState{}, &PartnerFitOverrideReasonRequiredError{} // a reason without a score sets nothing
		}
		trimmed := strings.TrimSpace(*reason)
		return partnerFitState{Score: current.Score, Computed: current.Computed, OverrideReason: &trimmed}, nil
	}
	return current, nil
}

func (s *Store) UpsertPartner(ctx context.Context, in UpsertPartnerInput) (partnerRow, error) {
	if err := auth.Require(ctx, "partner", principal.ActionUpdate); err != nil {
		return partnerRow{}, err
	}
	// Promotion flips organization.classification — that is an org
	// mutation, so the org's own write grant is required too; the
	// partner grant alone must not become a side door onto orgs.
	if err := auth.Require(ctx, "organization", principal.ActionUpdate); err != nil {
		return partnerRow{}, err
	}
	capturedBy, err := storekit.CapturedBy(ctx)
	if err != nil {
		return partnerRow{}, err
	}
	var out partnerRow
	err = s.tx(ctx, func(tx pgx.Tx) error {
		// The org reference is a client-supplied FK argument (H1).
		if err := auth.EnsureLinkTarget(ctx, tx, "organization", in.OrganizationID.UUID); err != nil {
			return err
		}
		// The row lock makes the version pre-read and the upsert below one
		// race-free unit; partner is keyed by its organization, so the org
		// row is the serialization point.
		if _, err := storekit.LockRow(ctx, tx, "organization", in.OrganizationID.UUID, storekit.LiveOnly); err != nil {
			return err
		}
		fit, err := resolvePartnerFit(ctx, tx, in)
		if err != nil {
			return err
		}
		if out, err = upsertPartnerRow(ctx, tx, in, fit, capturedBy); err != nil {
			return err
		}
		// Promotion is an org fact: classification flips with the row.
		if _, err := tx.Exec(ctx,
			`UPDATE organization SET classification = 'partner' WHERE id = $1 AND classification <> 'partner'`,
			in.OrganizationID); err != nil {
			return err
		}
		// Per-field keys, matching the upsert request's field names: the
		// human-edit-precedence probe derives field ownership from this
		// image, so a nested blob would make partner fields unownable.
		auditImage := map[string]any{
			"partner_role": in.PartnerRole, "cert_status": out.CertStatus,
			"margin_tier": in.MarginTier, "certified_staff": in.CertifiedStaff,
			"retention_rate": in.RetentionRate, "relationship_stage": out.RelationshipStage,
			"next_step": in.NextStep, "next_step_due_at": in.NextStepDueAt,
			"served_segments": in.ServedSegments,
		}
		// The fit pair enters the image only on an override gesture, so a
		// plain lifecycle edit never claims ownership of the fit fields.
		if in.FitScore != nil || in.FitOverrideReason != nil {
			auditImage["partner_fit_score"] = fit.Score
			auditImage["partner_fit_override_reason"] = fit.OverrideReason
		}
		auditID, err := storekit.Audit(ctx, tx, "update", "organization", in.OrganizationID.UUID, nil, auditImage)
		if err != nil {
			return err
		}
		return storekit.EmitEvent(ctx, tx, auditID, in.OrganizationID.UUID, crmcontracts.WebhookPayloadOrganizationUpdated{
			ChangedFields: map[string]any{
				eventKeyDelta: map[string]any{"partner": map[string]any{"role": in.PartnerRole, "cert_status": out.CertStatus}},
			},
		})
	})
	return out, err
}

// resolvePartnerFit pre-reads the current row under the org lock — one
// read feeds both the version guard and the fit-override fold (ErrNoRows
// means this upsert is the promotion) — and folds the §17 override rules.
func resolvePartnerFit(ctx context.Context, tx pgx.Tx, in UpsertPartnerInput) (partnerFitState, error) {
	var currentVersion *int64
	var current partnerFitState
	err := tx.QueryRow(ctx,
		`SELECT version, partner_fit_score, partner_fit_score_computed, partner_fit_override_reason
		 FROM partner WHERE organization_id = $1`, in.OrganizationID).
		Scan(&currentVersion, &current.Score, &current.Computed, &current.OverrideReason)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return partnerFitState{}, err
	}
	if in.IfVersion != nil && currentVersion != nil && *currentVersion != *in.IfVersion {
		return partnerFitState{}, apperrors.ErrVersionSkew
	}
	return applyPartnerFitOverride(current, in.FitScore, in.FitOverrideReason)
}

// upsertPartnerRow lands the promotion-or-update: absent lifecycle fields
// keep their current value (coalesce), while the fit triple is always
// written as resolved — a cleared override must land its NULLs.
func upsertPartnerRow(ctx context.Context, tx pgx.Tx, in UpsertPartnerInput, fit partnerFitState, capturedBy string) (partnerRow, error) {
	// served_segments keeps its current value only when the field is
	// absent; pgx maps a nil any to SQL NULL for the coalesce.
	var segments any
	if in.ServedSegments != nil {
		segments = *in.ServedSegments
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO partner (workspace_id, organization_id, partner_role, cert_status, margin_tier,
		                     certified_staff, retention_rate, relationship_stage, next_step,
		                     next_step_due_at, served_segments, partner_fit_score,
		                     partner_fit_score_computed, partner_fit_override_reason, source, captured_by)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
		        $1, $2, coalesce($3, 'applied'), $4, coalesce($5, 0), $6,
		        coalesce($8, 'research'), $9, $10, $11, $12, $13, $14, 'manual', $7)
		ON CONFLICT (organization_id) DO UPDATE SET
		  partner_role = EXCLUDED.partner_role,
		  cert_status = coalesce($3, partner.cert_status),
		  margin_tier = coalesce($4, partner.margin_tier),
		  certified_staff = coalesce($5, partner.certified_staff),
		  retention_rate = coalesce($6, partner.retention_rate),
		  relationship_stage = coalesce($8, partner.relationship_stage),
		  next_step = coalesce($9, partner.next_step),
		  next_step_due_at = coalesce($10, partner.next_step_due_at),
		  served_segments = coalesce($11, partner.served_segments),
		  partner_fit_score = $12,
		  partner_fit_score_computed = $13,
		  partner_fit_override_reason = $14,
		  archived_at = NULL
		RETURNING `+partnerColumns,
		in.OrganizationID, in.PartnerRole, in.CertStatus, in.MarginTier,
		in.CertifiedStaff, in.RetentionRate, capturedBy,
		in.RelationshipStage, in.NextStep, in.NextStepDueAt, segments,
		fit.Score, fit.Computed, fit.OverrideReason)
	return scanPartner(row)
}

func (s *Store) GetPartner(ctx context.Context, organizationID ids.OrganizationID) (partnerRow, error) {
	if err := auth.Require(ctx, "partner", principal.ActionRead); err != nil {
		return partnerRow{}, err
	}
	// Partner rows are organization-derived data.
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		return partnerRow{}, err
	}
	var out partnerRow
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "organization", organizationID.UUID); err != nil {
			return err
		}
		var err error
		out, err = scanPartner(tx.QueryRow(ctx,
			`SELECT `+partnerColumns+` FROM partner WHERE organization_id = $1 AND archived_at IS NULL`,
			organizationID))
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound // the org is not a partner
		}
		return err
	})
	return out, err
}

type ListPartnersInput struct {
	PartnerRole *string
	CertStatus  *string
	Limit       *int
	Cursor      string
}

func (s *Store) ListPartners(ctx context.Context, in ListPartnersInput) ([]partnerRow, storekit.Page, error) {
	if err := auth.Require(ctx, "partner", principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	limit := storekit.ClampLimit(in.Limit)
	var out []partnerRow
	var page storekit.Page
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, page, err = listPartnersTx(ctx, tx, in, limit)
		return err
	})
	return out, page, err
}

// listPartnersTx runs the keyset-paged partner list inside the caller's
// transaction: filters + org-derived row scope, one page + lookahead.
func listPartnersTx(ctx context.Context, tx pgx.Tx, in ListPartnersInput, limit int) ([]partnerRow, storekit.Page, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	where, err := partnerListWhere(ctx, in, arg)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	sql := storekit.SQLf(`
		SELECT %s FROM partner p
		JOIN organization o ON o.id = p.organization_id AND o.archived_at IS NULL
		WHERE %s ORDER BY p.organization_id LIMIT $%d`,
		aliased(partnerColumns, "p"), strings.Join(where, " AND "), arg(limit+1))
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	defer rows.Close()
	var out []partnerRow
	for rows.Next() {
		p, err := scanPartner(rows)
		if err != nil {
			return nil, storekit.Page{}, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, storekit.Page{}, err
	}
	var page storekit.Page
	if len(out) > limit {
		out = out[:limit]
		page = storekit.Page{HasMore: true, NextCursor: out[limit-1].OrganizationID.String()}
	}
	return out, page, nil
}

// partnerListWhere builds the WHERE fragments for the partner list: the
// role/cert-status filters, the keyset cursor, and the org's own row scope
// (a partner row is a read of its organization, so the org scope bounds
// the list).
func partnerListWhere(ctx context.Context, in ListPartnersInput, arg func(any) int) ([]string, error) {
	where := []string{"p.archived_at IS NULL"}
	if in.PartnerRole != nil {
		where = append(where, storekit.SQLf("p.partner_role = $%d", arg(*in.PartnerRole)))
	}
	if in.CertStatus != nil {
		where = append(where, storekit.SQLf("p.cert_status = $%d", arg(*in.CertStatus)))
	}
	if in.Cursor != "" {
		after, err := ids.Parse(in.Cursor)
		if err != nil {
			return nil, &RequiredFieldError{Field: "cursor"}
		}
		where = append(where, storekit.SQLf("p.organization_id > $%d", arg(after)))
	}
	scope, err := auth.ScopeClauseFor(ctx, "organization", "o", arg)
	if err != nil {
		return nil, err
	}
	if scope != "" {
		where = append(where, scope)
	}
	return where, nil
}

func wirePartner(p partnerRow) crmcontracts.Partner {
	out := crmcontracts.Partner{
		OrganizationId: openapi_types.UUID(p.OrganizationID.UUID),
		CertStatus:     crmcontracts.PartnerCertStatus(p.CertStatus),
		CreatedAt:      p.CreatedAt,
		UpdatedAt:      p.UpdatedAt,
		ArchivedAt:     p.ArchivedAt,
	}
	version := crmcontracts.RowVersion(p.Version)
	out.Version = &version
	if p.PartnerRole != nil {
		role := crmcontracts.PartnerPartnerRole(*p.PartnerRole)
		out.PartnerRole = &role
	}
	if p.MarginTier != nil {
		tier := crmcontracts.PartnerMarginTier(*p.MarginTier)
		out.MarginTier = &tier
	}
	stage := crmcontracts.PartnerRelationshipStage(p.RelationshipStage)
	out.RelationshipStage = &stage
	out.NextStep = p.NextStep
	if p.NextStepDueAt != nil {
		out.NextStepDueAt = &openapi_types.Date{Time: *p.NextStepDueAt}
	}
	if p.ServedSegments != nil {
		segments := p.ServedSegments
		out.ServedSegments = &segments
	}
	out.PartnerFitScore = intPtr(p.FitScore)
	out.PartnerFitScoreComputed = intPtr(p.FitScoreComputed)
	out.PartnerFitOverrideReason = p.FitOverrideReason
	out.RelationshipHealth = p.RelationshipHealth
	out.LastContactAt = p.LastContactAt
	metrics := map[string]any{"certified_staff": p.CertifiedStaff}
	if p.RetentionRate != nil {
		metrics["retention_rate"] = *p.RetentionRate
	}
	out.GateMetrics = &metrics
	return out
}

// intPtr widens the DB's smallint to the contract's int without sharing
// the row's storage.
func intPtr(v *int16) *int {
	if v == nil {
		return nil
	}
	w := int(*v)
	return &w
}
