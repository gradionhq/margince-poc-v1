// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The deal read paths: single-row get, the filtered keyset list, and
// the one column list + scanner every deal read shares.

package deals

import (
	"context"
	"errors"
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
	active, err := s.activeColumns(ctx)
	if err != nil {
		return crmcontracts.Deal{}, err
	}
	var out crmcontracts.Deal
	err = s.tx(ctx, func(tx pgx.Tx) (err error) {
		if err := auth.EnsureVisible(ctx, tx, "deal", id.UUID); err != nil {
			return err
		}
		out, err = readDealForCaller(ctx, tx, id, archived, active)
		return err
	})
	return out, err
}

// readDealForCaller reads one deal and masks it — the spelling EVERY entry
// point that hands a deal back uses, a mutation response included. A response
// is a read: the row a PATCH echoes is the same row a GET withholds from, and
// the reference withholding cannot ride on write authority the way a role mask
// does — being allowed to change the DEAL says nothing about being allowed to
// read the ORGANIZATION it names.
//
// readDeal itself stays unmasked on purpose. The update path builds its
// before-image from it, and an audit diff taken against a withheld null would
// record a change nobody made.
func readDealForCaller(ctx context.Context, tx pgx.Tx, id ids.DealID, archived storekit.ArchivedFilter, active []fieldcatalog.Column) (crmcontracts.Deal, error) {
	d, err := readDeal(ctx, tx, id, archived, active)
	if err != nil {
		return crmcontracts.Deal{}, err
	}
	return maskDealForCaller(ctx, tx, d)
}

type ListDealsInput struct {
	Cursor         *string
	Limit          *int
	Query          *string
	PipelineID     *ids.PipelineID
	StageID        *ids.StageID
	OwnerID        *ids.UserID
	OrganizationID *ids.OrganizationID
	ProjectID      *ids.ProjectID
	PartnerOrgID   *ids.OrganizationID
	PartnerSourced *bool
	// PartnerAttribution narrows to what the partner did — "sourced" or
	// "influenced". Narrower than PartnerSourced, which only asks whether a
	// partner is named at all.
	PartnerAttribution *string
	Status             *string
	Stalled            *bool
	IncludeArchived    bool
	// Sort is the contract's sort spec, validated against the core
	// vocabulary below plus the workspace's active cf_ columns.
	Sort *string
	// CustomFilters carries the request's cf_* query parameters —
	// equality matches against active custom columns (storekit listquery).
	CustomFilters map[string]string
}

// dealNameColumn is the deal's display-name column, the quick-find
// match expression. Deliberately NOT in the sortable vocabulary: the
// data-model §13.5 DM-VOCAB-3 set does not list it.
const dealNameColumn = "name"

// dealListFields is the deal list's core sortable vocabulary — exactly
// the data-model §13.5 DM-VOCAB-3 set; active cf_ columns join it per
// request.
var dealListFields = map[string]string{
	"created_at":          storekit.KindTimestamp,
	"updated_at":          storekit.KindTimestamp,
	"last_activity_at":    storekit.KindTimestamp,
	"amount_minor":        fieldcatalog.TypeCurrency,
	"expected_close_date": fieldcatalog.TypeDate,
}

func (s *Store) ListDeals(ctx context.Context, in ListDealsInput) ([]crmcontracts.Deal, storekit.Page, error) {
	if err := auth.Require(ctx, "deal", principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	active, err := s.activeColumns(ctx)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	pre, where, err := dealListQuery(ctx, in, active)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	return runListPage(ctx, s, pre, "deal", dealColumns, active, where, scanDealPage,
		func(d crmcontracts.Deal) (time.Time, ids.UUID) { return d.CreatedAt, ids.UUID(d.Id) },
		func(tx pgx.Tx, page []crmcontracts.Deal) error { return maskDeals(ctx, tx, page) })
}

// ListDealsTx is ListDeals inside a caller-opened transaction — the composite
// record reads, whose deal section must describe the same instant as its
// siblings. Same gate, same filters, same field-mask pass; the catalog answer
// is threaded in because a caller-opened read cannot fetch it without a
// second connection (ActiveDealColumns).
func (s *Store) ListDealsTx(ctx context.Context, tx pgx.Tx, in ListDealsInput, active CustomColumns) ([]crmcontracts.Deal, storekit.Page, error) {
	if err := auth.Require(ctx, "deal", principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	pre, where, err := dealListQuery(ctx, in, active.cols)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	return runListPageTx(ctx, tx, pre, "deal", dealColumns, active.cols, where, scanDealPage,
		func(d crmcontracts.Deal) (time.Time, ids.UUID) { return d.CreatedAt, ids.UUID(d.Id) },
		func(tx pgx.Tx, page []crmcontracts.Deal) error { return maskDeals(ctx, tx, page) })
}

// dealListQuery is the half of a deal list both entry points share: the sort
// refusal, the shared prelude and the deal's own filters.
func dealListQuery(ctx context.Context, in ListDealsInput, active []fieldcatalog.Column) (*listPrelude, []string, error) {
	if err := refuseMaskedSort(ctx, in.Sort); err != nil {
		return nil, nil, err
	}
	pre, err := buildListPrelude(ctx, "deal", dealListFields, active,
		in.Sort, in.Limit, in.Cursor, in.CustomFilters)
	if err != nil {
		return nil, nil, err
	}
	where, err := appendDealFilters(ctx, pre.where, in, pre.arg)
	if err != nil {
		return nil, nil, err
	}
	return pre, where, nil
}

// scanDealPage drains one list query's rows: each deal plus, under a
// non-default sort, the row's cursor key (the trailing __cursor_key
// column CursorKeySuffix appended).
func scanDealPage(rows pgx.Rows, active []fieldcatalog.Column, sorted *storekit.ListSort) ([]crmcontracts.Deal, []*string, error) {
	var deals []crmcontracts.Deal
	var cursorKeys []*string
	for rows.Next() {
		var key *string
		extra := []any{}
		if sorted != nil {
			extra = append(extra, &key)
		}
		d, err := scanDeal(rows, active, extra...)
		if err != nil {
			return nil, nil, err
		}
		deals = append(deals, d)
		cursorKeys = append(cursorKeys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return deals, cursorKeys, nil
}

// appendDealFilters translates the caller's list filters — archived
// visibility, full-text query, the column equality filters, and the
// stalled predicate — into WHERE clauses (the cf_ filters and the keyset
// cursor, which depends on the validated sort, stay in ListDeals).
func appendDealFilters(ctx context.Context, where []string, in ListDealsInput, arg func(any) int) ([]string, error) {
	if !in.IncludeArchived {
		where = append(where, "archived_at IS NULL")
	}
	if in.Query != nil && *in.Query != "" {
		where = append(where, storekit.QuickFindClause(arg(*in.Query), dealNameColumn))
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
	for _, ref := range []struct {
		column, table string
		id            *ids.UUID
	}{
		{filterOrganizationID, "organization", uuidOfFilter(in.OrganizationID)},
		{filterProjectID, "project", uuidOfFilter(in.ProjectID)},
		{filterPartnerOrgID, "organization", uuidOfFilter(in.PartnerOrgID)},
	} {
		if ref.id == nil {
			continue
		}
		clause, err := referenceFilterClause(ctx, ref.column, ref.table, *ref.id, arg)
		if err != nil {
			return nil, err
		}
		where = append(where, clause)
	}
	if in.PartnerSourced != nil {
		if *in.PartnerSourced {
			where = append(where, PartnerSourcedSQL(""))
		} else {
			where = append(where, "NOT "+PartnerSourcedSQL(""))
		}
	}
	if in.PartnerAttribution != nil {
		clause, err := partnerAttributionFilterClause(ctx, *in.PartnerAttribution, arg)
		if err != nil {
			return nil, err
		}
		where = append(where, clause)
	}
	if in.Status != nil {
		where = append(where, storekit.SQLf("status = $%d", arg(*in.Status)))
	}
	if in.Stalled != nil {
		if *in.Stalled {
			where = append(where, StalledSQL(""))
		} else {
			where = append(where, "NOT "+StalledSQL(""))
		}
	}
	return where, nil
}

// uuidOfFilter widens one optional typed filter id to the untyped UUID the
// reference table above walks. It is deliberately the only widening here: the
// phantom kind is what stops a project id being probed against organization.
func uuidOfFilter[K ids.EntityKind](id *ids.ID[K]) *ids.UUID {
	if id == nil {
		return nil
	}
	return &id.UUID
}

// referenceFilterClause narrows a filter on a column that NAMES another record
// to the rows whose target the caller may read.
//
// Filtering by an id is asking whether it is there. A bare
// `organization_id = $1` answers that question for an organization the caller
// cannot open — the same existence oracle the projection now withholds — so
// the arm carries the target's own visibility predicate. An empty page is the
// honest answer, and it is indistinguishable from a visible company that has
// no deals, which is what existence-hiding wants.
//
// An empty scope clause means a caller who reads every row of that table;
// there is nothing to narrow and the EXISTS would only confirm what the
// composite foreign key already guarantees.
func referenceFilterClause(ctx context.Context, column, table string, id ids.UUID, arg func(any) int) (string, error) {
	pos := arg(id)
	scope, err := auth.ScopeClauseFor(ctx, table, "ref", arg)
	if err != nil {
		return "", err
	}
	clause := storekit.SQLf("%s = $%d", column, pos)
	if scope == "" {
		return clause, nil
	}
	return clause + storekit.SQLf(
		" AND EXISTS (SELECT 1 FROM %s ref WHERE ref.id = $%d AND %s)", table, pos, scope), nil
}

// partnerAttributionFilterClause narrows to what a partner did for the deal,
// and carries the partner's OWN visibility with it.
//
// Without that second half the filter is an existence oracle for the fact the
// field mask withholds: a caller whose read of a deal masks both partner
// columns could still ask for `partner_attribution=sourced` and learn from the
// row's presence that some partner brought it. The mask and the filter have to
// withhold the same fact, so this arm answers only for deals whose partner the
// caller could open.
func partnerAttributionFilterClause(ctx context.Context, attribution string, arg func(any) int) (string, error) {
	if err := validPartnerAttribution(attribution); err != nil {
		return "", err
	}
	clause := storekit.SQLf("partner_attribution = $%d", arg(attribution))
	scope, err := auth.ScopeClauseFor(ctx, "organization", "pref", arg)
	if err != nil {
		return "", err
	}
	if scope == "" {
		return clause, nil
	}
	return clause + storekit.SQLf(
		" AND EXISTS (SELECT 1 FROM organization pref WHERE pref.id = partner_org_id AND %s)", scope), nil
}

const dealColumns = `id, name, amount_minor, currency, pipeline_id, stage_id,
	organization_id, project_id, owner_id, partner_org_id, partner_attribution, status, lost_reason,
	won_without_contract_reason, won_without_contract_detail,
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

// scanDeal scans core + active custom columns; extra receives any
// trailing expressions the caller's SELECT appended (the sorted list's
// cursor key).
func scanDeal(row pgx.Row, active []fieldcatalog.Column, extra ...any) (crmcontracts.Deal, error) {
	var d crmcontracts.Deal
	var id, pipelineID, stageID ids.UUID
	var orgID, projectID, ownerID, partnerID *ids.UUID
	var status string
	var forecastCat *string
	var expectedClose, waitUntil *time.Time
	var closeDateProvisional bool
	var version int64

	var wonReason *string
	dests := []any{
		&id, &d.Name, &d.AmountMinor, &d.Currency, &pipelineID, &stageID,
		&orgID, &projectID, &ownerID, &partnerID, &d.PartnerAttribution, &status, &d.LostReason,
		&wonReason, &d.WonWithoutContractDetail,
		&expectedClose, &closeDateProvisional, &d.ClosedAt, &forecastCat, &waitUntil, &d.LastActivityAt,
		&d.Source, &d.CapturedBy, &version, &d.CreatedAt, &d.UpdatedAt, &d.ArchivedAt,
	}
	cf := storekit.ScanDests(active)
	if err := row.Scan(append(append(dests, cf...), extra...)...); err != nil {
		return d, err
	}
	if values := storekit.ExtractValues(active, cf); len(values) > 0 {
		d.AdditionalProperties = values
	}
	if forecastCat != nil {
		cat := crmcontracts.DealForecastCategory(*forecastCat)
		d.ForecastCategory = &cat
	}
	if wonReason != nil {
		reason := crmcontracts.DealWonWithoutContractReason(*wonReason)
		d.WonWithoutContractReason = &reason
	}

	d.Id = openapi_types.UUID(id)
	pid := openapi_types.UUID(pipelineID)
	d.PipelineId = &pid
	sid := openapi_types.UUID(stageID)
	d.StageId = &sid
	d.OrganizationId = uuidPtr(orgID)
	d.ProjectId = uuidPtr(projectID)
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
