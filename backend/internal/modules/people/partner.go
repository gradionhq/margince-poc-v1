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
	certified_staff, retention_rate, version, created_at, updated_at, archived_at`

type partnerRow struct {
	OrganizationID ids.UUID
	CertStatus     string
	PartnerRole    *string
	MarginTier     *string
	CertifiedStaff int16
	RetentionRate  *int16
	Version        int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ArchivedAt     *time.Time
}

func scanPartner(r pgx.Row) (partnerRow, error) {
	var p partnerRow
	err := r.Scan(&p.OrganizationID, &p.CertStatus, &p.PartnerRole, &p.MarginTier,
		&p.CertifiedStaff, &p.RetentionRate, &p.Version, &p.CreatedAt, &p.UpdatedAt, &p.ArchivedAt)
	return p, err
}

type UpsertPartnerInput struct {
	OrganizationID ids.UUID
	PartnerRole    string
	CertStatus     *string
	MarginTier     *string
	CertifiedStaff *int16
	RetentionRate  *int16
	IfVersion      *int64
}

func (s *Store) UpsertPartner(ctx context.Context, in UpsertPartnerInput) (partnerRow, error) {
	if err := auth.Require(ctx, "partner", principal.ActionUpdate); err != nil {
		return partnerRow{}, err
	}
	capturedBy, err := storekit.CapturedBy(ctx)
	if err != nil {
		return partnerRow{}, err
	}
	var out partnerRow
	err = s.tx(ctx, func(tx pgx.Tx) error {
		// The org reference is a client-supplied FK argument (H1).
		if err := auth.EnsureLinkTarget(ctx, tx, "organization", in.OrganizationID); err != nil {
			return err
		}
		if in.IfVersion != nil {
			var current int64
			err := tx.QueryRow(ctx,
				`SELECT version FROM partner WHERE organization_id = $1`, in.OrganizationID).Scan(&current)
			if err == nil && current != *in.IfVersion {
				return apperrors.ErrVersionSkew
			}
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		row := tx.QueryRow(ctx, `
			INSERT INTO partner (workspace_id, organization_id, partner_role, cert_status, margin_tier,
			                     certified_staff, retention_rate, source, captured_by)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        $1, $2, coalesce($3, 'applied'), $4, coalesce($5, 0), $6, 'manual', $7)
			ON CONFLICT (organization_id) DO UPDATE SET
			  partner_role = EXCLUDED.partner_role,
			  cert_status = coalesce($3, partner.cert_status),
			  margin_tier = coalesce($4, partner.margin_tier),
			  certified_staff = coalesce($5, partner.certified_staff),
			  retention_rate = coalesce($6, partner.retention_rate),
			  archived_at = NULL
			RETURNING `+partnerColumns,
			in.OrganizationID, in.PartnerRole, in.CertStatus, in.MarginTier,
			in.CertifiedStaff, in.RetentionRate, capturedBy)
		if out, err = scanPartner(row); err != nil {
			return err
		}
		// Promotion is an org fact: classification flips with the row.
		if _, err := tx.Exec(ctx,
			`UPDATE organization SET classification = 'partner' WHERE id = $1 AND classification <> 'partner'`,
			in.OrganizationID); err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "update", "organization", in.OrganizationID, nil, map[string]any{
			"partner": map[string]any{"role": in.PartnerRole, "cert_status": out.CertStatus},
		})
		if err != nil {
			return err
		}
		return storekit.Emit(ctx, tx, auditID, "organization.updated", "organization", in.OrganizationID, map[string]any{
			"delta": map[string]any{"partner": map[string]any{"role": in.PartnerRole, "cert_status": out.CertStatus}},
		})
	})
	return out, err
}

func (s *Store) GetPartner(ctx context.Context, organizationID ids.UUID) (partnerRow, error) {
	if err := auth.Require(ctx, "partner", principal.ActionRead); err != nil {
		return partnerRow{}, err
	}
	var out partnerRow
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "organization", organizationID); err != nil {
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
	limit := storekit.ClampLimit(in.Limit)
	var out []partnerRow
	var page storekit.Page
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var args []any
		arg := func(v any) int { args = append(args, v); return len(args) }
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
				return &RequiredFieldError{Field: "cursor"}
			}
			where = append(where, storekit.SQLf("p.organization_id > $%d", arg(after)))
		}
		// A partner row is a read of its organization: the org's own
		// row scope bounds the list.
		scope, err := auth.ScopeClauseFor(ctx, "o", arg)
		if err != nil {
			return err
		}
		if scope != "" {
			where = append(where, scope)
		}
		sql := storekit.SQLf(`
			SELECT %s FROM partner p
			JOIN organization o ON o.id = p.organization_id AND o.archived_at IS NULL
			WHERE %s ORDER BY p.organization_id LIMIT $%d`,
			aliased(partnerColumns, "p"), strings.Join(where, " AND "), arg(limit+1))
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanPartner(rows)
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(out) > limit {
			out = out[:limit]
			page = storekit.Page{HasMore: true, NextCursor: out[limit-1].OrganizationID.String()}
		}
		return nil
	})
	return out, page, err
}

func wirePartner(p partnerRow) crmcontracts.Partner {
	out := crmcontracts.Partner{
		OrganizationId: openapi_types.UUID(p.OrganizationID),
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
	metrics := map[string]any{"certified_staff": p.CertifiedStaff}
	if p.RetentionRate != nil {
		metrics["retention_rate"] = *p.RetentionRate
	}
	out.GateMetrics = &metrics
	return out
}
