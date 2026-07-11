// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The deal read paths: single-row get, the filtered keyset list, and
// the one column list + scanner every deal read shares.

package deals

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
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

func (s *Store) GetDeal(ctx context.Context, id ids.DealID, archived storekit.ArchivedFilter) (crmcontracts.Deal, error) {
	if err := auth.Require(ctx, "deal", principal.ActionRead); err != nil {
		return crmcontracts.Deal{}, err
	}
	active, err := s.activeColumns(ctx, "deal")
	if err != nil {
		return crmcontracts.Deal{}, err
	}
	var out crmcontracts.Deal
	err = s.tx(ctx, func(tx pgx.Tx) (err error) {
		if err := auth.EnsureVisible(ctx, tx, "deal", id.UUID); err != nil {
			return err
		}
		out, err = readDeal(ctx, tx, id, archived, active)
		return err
	})
	return out, err
}

type ListDealsInput struct {
	Cursor          *string
	Limit           *int
	Query           *string
	PipelineID      *ids.PipelineID
	StageID         *ids.StageID
	OwnerID         *ids.UserID
	OrganizationID  *ids.OrganizationID
	PartnerOrgID    *ids.OrganizationID
	PartnerSourced  *bool
	Status          *string
	Stalled         *bool
	IncludeArchived bool
}

func (s *Store) ListDeals(ctx context.Context, in ListDealsInput) ([]crmcontracts.Deal, storekit.Page, error) {
	if err := auth.Require(ctx, "deal", principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	active, err := s.activeColumns(ctx, "deal")
	if err != nil {
		return nil, storekit.Page{}, err
	}
	limit := storekit.ClampLimit(in.Limit)

	where := []string{"1=1"}
	args := []any{}
	arg := func(v any) int { args = append(args, v); return len(args) }

	scope, err := auth.ScopeClauseFor(ctx, "deal", "", arg)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	if scope != "" {
		where = append(where, scope)
	}

	where, err = appendDealFilters(where, in, arg)
	if err != nil {
		return nil, storekit.Page{}, err
	}

	var deals []crmcontracts.Deal
	var page storekit.Page
	err = s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+dealColumns+storekit.SelectSuffix(active)+` FROM deal WHERE `+strings.Join(where, " AND ")+
				storekit.SQLf(` ORDER BY created_at DESC, id DESC LIMIT %d`, limit+1),
			args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanDeal(rows, active)
			if err != nil {
				return err
			}
			deals = append(deals, d)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(deals) > limit {
			deals = deals[:limit]
			last := deals[len(deals)-1]
			page = storekit.Page{HasMore: true, NextCursor: storekit.EncodeCursor(last.CreatedAt, ids.UUID(last.Id))}
		}
		return nil
	})
	if deals == nil {
		deals = []crmcontracts.Deal{}
	}
	return deals, page, err
}

// appendDealFilters translates the caller's list filters — archived
// visibility, full-text query, the column equality filters, the stalled
// predicate, and the keyset cursor — into WHERE clauses.
func appendDealFilters(where []string, in ListDealsInput, arg func(any) int) ([]string, error) {
	if !in.IncludeArchived {
		where = append(where, "archived_at IS NULL")
	}
	if in.Query != nil && *in.Query != "" {
		where = append(where, storekit.QuickFindClause(arg(*in.Query), "name"))
	}
	if in.PipelineID != nil {
		where = append(where, storekit.SQLf("pipeline_id = $%d", arg(*in.PipelineID)))
	}
	if in.StageID != nil {
		where = append(where, storekit.SQLf("stage_id = $%d", arg(*in.StageID)))
	}
	if in.OwnerID != nil {
		where = append(where, storekit.SQLf("owner_id = $%d", arg(*in.OwnerID)))
	}
	if in.OrganizationID != nil {
		where = append(where, storekit.SQLf("organization_id = $%d", arg(*in.OrganizationID)))
	}
	if in.PartnerOrgID != nil {
		where = append(where, storekit.SQLf("partner_org_id = $%d", arg(*in.PartnerOrgID)))
	}
	// partner_sourced is attribution presence, not a value match: true is
	// the partner-sourced pipeline slice, false its direct complement.
	if in.PartnerSourced != nil {
		if *in.PartnerSourced {
			where = append(where, "partner_org_id IS NOT NULL")
		} else {
			where = append(where, "partner_org_id IS NULL")
		}
	}
	if in.Status != nil {
		where = append(where, storekit.SQLf("status = $%d", arg(*in.Status)))
	}
	if in.Stalled != nil {
		if *in.Stalled {
			where = append(where, stalledSQL)
		} else {
			where = append(where, "NOT "+stalledSQL)
		}
	}
	if in.Cursor != nil && *in.Cursor != "" {
		c, err := storekit.DecodeCursor(*in.Cursor)
		if err != nil {
			return nil, err
		}
		where = append(where, storekit.SQLf("(created_at, id) < ($%d, $%d)", arg(c.CreatedAt), arg(c.ID)))
	}
	return where, nil
}

const dealColumns = `id, workspace_id, name, amount_minor, currency, pipeline_id, stage_id,
	organization_id, owner_id, partner_org_id, status, lost_reason,
	expected_close_date, close_date_provisional, closed_at, forecast_category, wait_until, last_activity_at,
	source, captured_by, version, created_at, updated_at, archived_at`

// readDeal resolves one deal row; active names the custom-field columns
// to carry alongside the core ones — nil for internal decision reads
// whose result never reaches the wire.
func readDeal(ctx context.Context, tx pgx.Tx, id ids.DealID, archived storekit.ArchivedFilter, active []fieldcatalog.Column) (crmcontracts.Deal, error) {
	q := `SELECT ` + dealColumns + storekit.SelectSuffix(active) + ` FROM deal WHERE id = $1`
	if archived == storekit.LiveOnly {
		q += ` AND archived_at IS NULL`
	}
	d, err := scanDeal(tx.QueryRow(ctx, q, id), active)
	if errors.Is(err, pgx.ErrNoRows) {
		return crmcontracts.Deal{}, apperrors.ErrNotFound
	}
	return d, err
}

func scanDeal(row pgx.Row, active []fieldcatalog.Column) (crmcontracts.Deal, error) {
	var d crmcontracts.Deal
	var id, wsID, pipelineID, stageID ids.UUID
	var orgID, ownerID, partnerID *ids.UUID
	var status string
	var forecastCat *string
	var expectedClose, waitUntil *time.Time
	var closeDateProvisional bool
	var version int64

	dests := []any{&id, &wsID, &d.Name, &d.AmountMinor, &d.Currency, &pipelineID, &stageID,
		&orgID, &ownerID, &partnerID, &status, &d.LostReason,
		&expectedClose, &closeDateProvisional, &d.ClosedAt, &forecastCat, &waitUntil, &d.LastActivityAt,
		&d.Source, &d.CapturedBy, &version, &d.CreatedAt, &d.UpdatedAt, &d.ArchivedAt}
	cf := storekit.ScanDests(active)
	if err := row.Scan(append(dests, cf...)...); err != nil {
		return d, err
	}
	if values := storekit.ExtractValues(active, cf); len(values) > 0 {
		d.AdditionalProperties = values
	}
	if forecastCat != nil {
		cat := crmcontracts.DealForecastCategory(*forecastCat)
		d.ForecastCategory = &cat
	}

	d.Id = openapi_types.UUID(id)
	d.WorkspaceId = openapi_types.UUID(wsID)
	d.PipelineId = openapi_types.UUID(pipelineID)
	d.StageId = openapi_types.UUID(stageID)
	d.OrganizationId = uuidPtr(orgID)
	d.OwnerId = uuidPtr(ownerID)
	d.PartnerOrgId = uuidPtr(partnerID)
	d.Status = crmcontracts.DealStatus(status)
	if expectedClose != nil {
		d.ExpectedCloseDate = &openapi_types.Date{Time: *expectedClose}
	}
	d.CloseDateProvisional = &closeDateProvisional
	if waitUntil != nil {
		d.WaitUntil = &openapi_types.Date{Time: *waitUntil}
	}
	d.Version = &version
	stalled := IsStalled(status, d.CreatedAt, d.LastActivityAt, waitUntil, time.Now().UTC())
	d.Stalled = &stalled
	return d, nil
}
