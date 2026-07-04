package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/crm-contracts"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

type CreateDealInput struct {
	Name           string
	AmountMinor    *int64
	Currency       *string
	PipelineID     ids.UUID
	StageID        ids.UUID
	OrganizationID *ids.UUID
	OwnerID        *ids.UUID
	ExpectedClose  *time.Time
	Source         string
}

func (s *Store) CreateDeal(ctx context.Context, in CreateDealInput) (crmcontracts.Deal, error) {
	if err := auth.Require(ctx, "deal", principal.ActionCreate); err != nil {
		return crmcontracts.Deal{}, err
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return crmcontracts.Deal{}, err
	}

	var out crmcontracts.Deal
	err = s.tx(ctx, func(tx pgx.Tx) error {
		wsID := storekit.MustWorkspace(ctx)

		// Deals are born open: AdvanceDeal is the ONE path that derives
		// won/lost and maintains the closed_at/lost_reason/FX invariants.
		// Creating straight onto a terminal stage would put an "open" deal
		// on a won column — silent forecast corruption, no CHECK trips.
		var semantic string
		err := tx.QueryRow(ctx,
			`SELECT semantic FROM stage WHERE id = $1 AND pipeline_id = $2 AND archived_at IS NULL`,
			in.StageID, in.PipelineID).Scan(&semantic)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}
		if semantic == "won" || semantic == "lost" {
			return &TerminalStageOnCreateError{Semantic: semantic}
		}

		id := ids.NewV7()
		_, err = tx.Exec(ctx,
			`INSERT INTO deal (id, workspace_id, name, amount_minor, currency, pipeline_id, stage_id,
			                   organization_id, owner_id, expected_close_date, source, captured_by)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			id, wsID, in.Name, in.AmountMinor, in.Currency, in.PipelineID, in.StageID,
			in.OrganizationID, in.OwnerID, in.ExpectedClose, in.Source, by)
		if err != nil {
			// Covers the remaining FKs (pipeline, organization, owner);
			// the stage/pipeline pairing was pre-checked above.
			if storekit.IsForeignKeyViolation(err) {
				return apperrors.ErrNotFound
			}
			return err
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO deal_stage_history (workspace_id, deal_id, from_stage_id, to_stage_id, changed_by, amount_minor_at_change, currency_at_change)
			 VALUES ($1, $2, NULL, $3, $4, $5, $6)`,
			wsID, id, in.StageID, by, in.AmountMinor, in.Currency); err != nil {
			return err
		}

		auditID, err := storekit.Audit(ctx, tx, "create", "deal", id, nil, map[string]any{"name": in.Name})
		if err != nil {
			return err
		}
		if err := storekit.Emit(ctx, tx, auditID, "deal.created", "deal", id, map[string]any{"name": in.Name}); err != nil {
			return err
		}
		out, err = readDeal(ctx, tx, id, false)
		return err
	})
	return out, err
}

func (s *Store) GetDeal(ctx context.Context, id ids.UUID, includeArchived bool) (crmcontracts.Deal, error) {
	if err := auth.Require(ctx, "deal", principal.ActionRead); err != nil {
		return crmcontracts.Deal{}, err
	}
	var out crmcontracts.Deal
	err := s.tx(ctx, func(tx pgx.Tx) (err error) {
		if err := auth.EnsureVisible(ctx, tx, "deal", id); err != nil {
			return err
		}
		out, err = readDeal(ctx, tx, id, includeArchived)
		return err
	})
	return out, err
}

type ListDealsInput struct {
	Cursor          *string
	Limit           *int
	Query           *string
	PipelineID      *ids.UUID
	StageID         *ids.UUID
	OwnerID         *ids.UUID
	OrganizationID  *ids.UUID
	Status          *string
	IncludeArchived bool
}

func (s *Store) ListDeals(ctx context.Context, in ListDealsInput) ([]crmcontracts.Deal, storekit.Page, error) {
	if err := auth.Require(ctx, "deal", principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	limit := storekit.ClampLimit(in.Limit)

	where := []string{"1=1"}
	args := []any{}
	arg := func(v any) int { args = append(args, v); return len(args) }

	scope, err := auth.ScopeClause(ctx, arg)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	if scope != "" {
		where = append(where, scope)
	}

	if !in.IncludeArchived {
		where = append(where, "archived_at IS NULL")
	}
	if in.Query != nil && *in.Query != "" {
		where = append(where, sprintf("search_tsv @@ plainto_tsquery('simple', $%d)", arg(*in.Query)))
	}
	if in.PipelineID != nil {
		where = append(where, sprintf("pipeline_id = $%d", arg(*in.PipelineID)))
	}
	if in.StageID != nil {
		where = append(where, sprintf("stage_id = $%d", arg(*in.StageID)))
	}
	if in.OwnerID != nil {
		where = append(where, sprintf("owner_id = $%d", arg(*in.OwnerID)))
	}
	if in.OrganizationID != nil {
		where = append(where, sprintf("organization_id = $%d", arg(*in.OrganizationID)))
	}
	if in.Status != nil {
		where = append(where, sprintf("status = $%d", arg(*in.Status)))
	}
	if in.Cursor != nil && *in.Cursor != "" {
		c, err := storekit.DecodeCursor(*in.Cursor)
		if err != nil {
			return nil, storekit.Page{}, err
		}
		where = append(where, sprintf("(created_at, id) < ($%d, $%d)", arg(c.CreatedAt), arg(c.ID)))
	}

	var deals []crmcontracts.Deal
	var page storekit.Page
	err = s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+dealColumns+` FROM deal WHERE `+strings.Join(where, " AND ")+
				sprintf(` ORDER BY created_at DESC, id DESC LIMIT %d`, limit+1),
			args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanDeal(rows)
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

type UpdateDealInput struct {
	Name           *string
	AmountMinor    *int64
	Currency       *string
	OrganizationID *ids.UUID
	OwnerID        *ids.UUID
	PartnerOrgID   *ids.UUID
	ExpectedClose  *time.Time
	ForecastCat    *string
	WaitUntil      *time.Time
	IfVersion      *int64
}

func (s *Store) UpdateDeal(ctx context.Context, id ids.UUID, in UpdateDealInput) (crmcontracts.Deal, error) {
	if err := auth.Require(ctx, "deal", principal.ActionUpdate); err != nil {
		return crmcontracts.Deal{}, err
	}
	var out crmcontracts.Deal
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "deal", id); err != nil {
			return err
		}
		current, err := readDeal(ctx, tx, id, false)
		if err != nil {
			return err
		}

		p := storekit.NewPatch()
		if in.Name != nil {
			p.Set("name", current.Name, *in.Name)
		}
		if in.AmountMinor != nil {
			p.Set("amount_minor", current.AmountMinor, *in.AmountMinor)
		}
		if in.Currency != nil {
			p.Set("currency", current.Currency, *in.Currency)
		}
		if in.OrganizationID != nil {
			p.Set("organization_id", current.OrganizationId, *in.OrganizationID)
		}
		if in.OwnerID != nil {
			p.Set("owner_id", current.OwnerId, *in.OwnerID)
		}
		if in.PartnerOrgID != nil {
			p.Set("partner_org_id", current.PartnerOrgId, *in.PartnerOrgID)
		}
		if in.ExpectedClose != nil {
			p.Set("expected_close_date", current.ExpectedCloseDate, *in.ExpectedClose)
		}
		if in.ForecastCat != nil {
			p.Set("forecast_category", current.ForecastCategory, *in.ForecastCat)
		}
		if in.WaitUntil != nil {
			p.Set("wait_until", current.WaitUntil, *in.WaitUntil)
		}
		if p.Empty() {
			out = current
			return nil
		}

		// The amount/currency pairing invariant holds on the RESULTING
		// row, not just the request: an amount stranded without a
		// currency would skip the FX freeze at close and then violate
		// deal_closed_fx.
		resultingAmount := current.AmountMinor
		if in.AmountMinor != nil {
			resultingAmount = in.AmountMinor
		}
		resultingCurrency := current.Currency
		if in.Currency != nil {
			resultingCurrency = in.Currency
		}
		if (resultingAmount == nil) != (resultingCurrency == nil) {
			return &AmountCurrencyPairError{}
		}

		// Re-pricing a CLOSED deal must re-freeze FX as of the original
		// close date, or the frozen rate goes stale against the new
		// currency (silent base-currency corruption) — and a deal closed
		// amountless has no frozen rate at all, so adding an amount later
		// would trip deal_closed_fx. Same-day rate lookup as at close, so
		// roll-ups stay reproducible.
		if string(current.Status) != "open" && resultingAmount != nil &&
			(in.AmountMinor != nil || in.Currency != nil) {
			// deal_closed_at guarantees ClosedAt on a non-open row.
			rate, rateDate, err := freezeFx(ctx, tx, *resultingCurrency, *current.ClosedAt)
			if err != nil {
				return err
			}
			p.Set("fx_rate_to_base", nil, rate)
			p.Set("fx_rate_date", nil, rateDate)
		}

		if err := p.Apply(ctx, tx, "deal", id, in.IfVersion); err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "update", "deal", id, p.Before(), p.After())
		if err != nil {
			return err
		}

		// Owner reassignment is a first-class fact with its own
		// consumers (events.md §5.3): emit deal.owner_changed for the
		// owner transition and deal.updated only for the other fields —
		// both on this request's correlation_id when they co-occur.
		ownerChanged := in.OwnerID != nil && (current.OwnerId == nil || ids.UUID(*current.OwnerId) != *in.OwnerID)
		if ownerChanged {
			payload := map[string]any{"to_owner_id": *in.OwnerID}
			if current.OwnerId != nil {
				payload["from_owner_id"] = *current.OwnerId
			}
			if err := storekit.Emit(ctx, tx, auditID, "deal.owner_changed", "deal", id, payload); err != nil {
				return err
			}
		}
		rest := make(map[string]any, len(p.After()))
		for field, v := range p.After() {
			if ownerChanged && field == "owner_id" {
				continue
			}
			rest[field] = v
		}
		if len(rest) > 0 {
			if err := storekit.Emit(ctx, tx, auditID, "deal.updated", "deal", id, rest); err != nil {
				return err
			}
		}
		out, err = readDeal(ctx, tx, id, false)
		return err
	})
	return out, err
}

// AmountCurrencyPairError maps to 422: amount_minor and currency come
// together or not at all (data-model §6 money rules).
type AmountCurrencyPairError struct{}

func (e *AmountCurrencyPairError) Error() string {
	return "amount_minor and currency come together or not at all"
}

// TerminalStageOnCreateError maps to 422: create on an open stage, then
// advance — won/lost is derived, never asserted at birth.
type TerminalStageOnCreateError struct{ Semantic string }

func (e *TerminalStageOnCreateError) Error() string {
	return "deals cannot be created on a " + e.Semantic + " stage; create open, then advance"
}

type AdvanceDealInput struct {
	ToStageID  ids.UUID
	LostReason *string
	IfVersion  *int64
}

// StagePipelineMismatchError maps to 422: the target stage exists but
// belongs to another pipeline.
type StagePipelineMismatchError struct{ StageID ids.UUID }

func (e *StagePipelineMismatchError) Error() string {
	return "stage " + e.StageID.String() + " does not belong to the deal's pipeline"
}

// LostReasonRequiredError maps to 422 on advancing to a lost stage
// without a reason (deal_lost_reason CHECK, features/01 §3.1).
type LostReasonRequiredError struct{}

func (e *LostReasonRequiredError) Error() string { return "lost_reason is required to close as lost" }

// AdvanceDeal moves a deal one stage, deriving won/lost from the target
// stage's semantic (never from client-supplied status), appending the
// stage history snapshot and emitting the first-class deal.stage_changed
// event — never a generic deal.updated (events.md §1).
func (s *Store) AdvanceDeal(ctx context.Context, id ids.UUID, in AdvanceDealInput) (crmcontracts.Deal, error) {
	if err := auth.Require(ctx, "deal", principal.ActionUpdate); err != nil {
		return crmcontracts.Deal{}, err
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return crmcontracts.Deal{}, err
	}

	var out crmcontracts.Deal
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "deal", id); err != nil {
			return err
		}
		current, err := readDeal(ctx, tx, id, false)
		if err != nil {
			return err
		}

		var semantic string
		var stagePipeline ids.UUID
		var winProbability int
		err = tx.QueryRow(ctx,
			`SELECT semantic, pipeline_id, win_probability FROM stage WHERE id = $1 AND archived_at IS NULL`,
			in.ToStageID).Scan(&semantic, &stagePipeline, &winProbability)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}
		if stagePipeline != ids.UUID(current.PipelineId) {
			return &StagePipelineMismatchError{StageID: in.ToStageID}
		}

		status := "open"
		var closedAt *time.Time
		switch semantic {
		case "won", "lost":
			status = semantic
			now := time.Now().UTC()
			closedAt = &now
			if semantic == "lost" && (in.LostReason == nil || *in.LostReason == "") {
				return &LostReasonRequiredError{}
			}
		}

		p := storekit.NewPatch()
		p.Set("stage_id", current.StageId, in.ToStageID)
		if status != string(current.Status) {
			p.Set("status", current.Status, status)
		}
		if closedAt != nil {
			p.Set("closed_at", current.ClosedAt, *closedAt)
		}
		// lost_reason only exists on a lost deal — never on won or open
		// (on a reopen the terminal-field sweep below clears it; setting
		// it twice would be a malformed UPDATE anyway).
		if status == "lost" && in.LostReason != nil {
			p.Set("lost_reason", current.LostReason, *in.LostReason)
		}
		// Closing with an amount freezes today's FX rate so base-currency
		// roll-ups stay reproducible (deal_closed_fx).
		if status != "open" && current.AmountMinor != nil && current.Currency != nil {
			rate, rateDate, err := freezeFx(ctx, tx, *current.Currency, time.Now().UTC())
			if err != nil {
				return err
			}
			p.Set("fx_rate_to_base", nil, rate)
			p.Set("fx_rate_date", nil, rateDate)
		}
		// Reopening a won/lost deal must clear every terminal field —
		// the DB CHECKs are one-directional, so a stale closed_at or
		// lost_reason on an open deal would silently corrupt forecast
		// and won-lost reporting.
		if status == "open" && string(current.Status) != "open" {
			p.Set("closed_at", current.ClosedAt, nil)
			p.Set("lost_reason", current.LostReason, nil)
			p.Set("fx_rate_to_base", nil, nil)
			p.Set("fx_rate_date", nil, nil)
		}
		if err := p.Apply(ctx, tx, "deal", id, in.IfVersion); err != nil {
			return err
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO deal_stage_history (workspace_id, deal_id, from_stage_id, to_stage_id, changed_by, amount_minor_at_change, currency_at_change)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			storekit.MustWorkspace(ctx), id, ids.UUID(current.StageId), in.ToStageID, by,
			current.AmountMinor, current.Currency); err != nil {
			return err
		}

		auditID, err := storekit.Audit(ctx, tx, "advance_stage", "deal", id, p.Before(), p.After())
		if err != nil {
			return err
		}
		// The §5.3 payload carries the amount snapshot so as-of-date
		// pipeline reports and the overnight stalled/forecast sweep react
		// without a read-back; to_status records the 🟡 won/lost class.
		if err := storekit.Emit(ctx, tx, auditID, "deal.stage_changed", "deal", id, map[string]any{
			"from_stage_id":          current.StageId,
			"to_stage_id":            in.ToStageID,
			"from_status":            current.Status,
			"to_status":              status,
			"amount_minor_at_change": current.AmountMinor,
			"currency_at_change":     current.Currency,
			"win_probability":        winProbability,
		}); err != nil {
			return err
		}
		out, err = readDeal(ctx, tx, id, false)
		return err
	})
	return out, err
}

// MissingFxRateError maps to 422: closing a foreign-currency deal needs a
// same-day-or-earlier fx_rate row to freeze.
type MissingFxRateError struct{ From, To string }

func (e *MissingFxRateError) Error() string {
	return "no fx_rate from " + e.From + " to " + e.To + " to freeze at close"
}

// freezeFx resolves the frozen currency→base conversion for a closed
// deal: the latest fx_rate on or before asOf. Used at close (asOf = now)
// and when a closed deal is re-priced (asOf = its close date), so the
// frozen rate always reflects the deal's close, never the edit.
func freezeFx(ctx context.Context, tx pgx.Tx, currency string, asOf time.Time) (string, time.Time, error) {
	asOfDate := asOf.UTC().Truncate(24 * time.Hour)
	var base string
	if err := tx.QueryRow(ctx,
		`SELECT base_currency FROM workspace WHERE id = $1`, storekit.MustWorkspace(ctx)).Scan(&base); err != nil {
		return "", time.Time{}, err
	}
	if currency == base {
		return "1", asOfDate, nil
	}
	var rate string
	err := tx.QueryRow(ctx,
		`SELECT rate::text FROM fx_rate
		 WHERE from_currency = $1 AND to_currency = $2 AND rate_date <= $3
		 ORDER BY rate_date DESC LIMIT 1`,
		currency, base, asOfDate).Scan(&rate)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", time.Time{}, &MissingFxRateError{From: currency, To: base}
	}
	if err != nil {
		return "", time.Time{}, err
	}
	return rate, asOfDate, nil
}

func (s *Store) ArchiveDeal(ctx context.Context, id ids.UUID) (crmcontracts.Deal, error) {
	if err := auth.Require(ctx, "deal", principal.ActionDelete); err != nil {
		return crmcontracts.Deal{}, err
	}
	var out crmcontracts.Deal
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "deal", id); err != nil {
			return err
		}
		if _, err := readDeal(ctx, tx, id, false); err != nil {
			return err
		}
		now := time.Now().UTC()
		for _, stmt := range []string{
			`UPDATE deal SET archived_at = $2 WHERE id = $1 AND archived_at IS NULL`,
			`UPDATE relationship SET archived_at = $2 WHERE deal_id = $1 AND archived_at IS NULL`,
		} {
			if _, err := tx.Exec(ctx, stmt, id, now); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM list_member WHERE entity_type = 'deal' AND entity_id = $1`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM taggable WHERE entity_type = 'deal' AND entity_id = $1`, id); err != nil {
			return err
		}

		auditID, err := storekit.Audit(ctx, tx, "archive", "deal", id, nil, nil)
		if err != nil {
			return err
		}
		if err := storekit.Emit(ctx, tx, auditID, "deal.archived", "deal", id, nil); err != nil {
			return err
		}
		out, err = readDeal(ctx, tx, id, true)
		return err
	})
	return out, err
}

const dealColumns = `id, workspace_id, name, amount_minor, currency, pipeline_id, stage_id,
	organization_id, owner_id, partner_org_id, status, lost_reason,
	expected_close_date, closed_at, forecast_category, wait_until, last_activity_at,
	source, captured_by, version, created_at, updated_at, archived_at`

func readDeal(ctx context.Context, tx pgx.Tx, id ids.UUID, includeArchived bool) (crmcontracts.Deal, error) {
	q := `SELECT ` + dealColumns + ` FROM deal WHERE id = $1`
	if !includeArchived {
		q += ` AND archived_at IS NULL`
	}
	d, err := scanDeal(tx.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return crmcontracts.Deal{}, apperrors.ErrNotFound
	}
	return d, err
}

func scanDeal(row pgx.Row) (crmcontracts.Deal, error) {
	var d crmcontracts.Deal
	var id, wsID, pipelineID, stageID ids.UUID
	var orgID, ownerID, partnerID *ids.UUID
	var status string
	var forecastCat *string
	var expectedClose, waitUntil *time.Time
	var version int64

	err := row.Scan(&id, &wsID, &d.Name, &d.AmountMinor, &d.Currency, &pipelineID, &stageID,
		&orgID, &ownerID, &partnerID, &status, &d.LostReason,
		&expectedClose, &d.ClosedAt, &forecastCat, &waitUntil, &d.LastActivityAt,
		&d.Source, &d.CapturedBy, &version, &d.CreatedAt, &d.UpdatedAt, &d.ArchivedAt)
	if err != nil {
		return d, err
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
	if waitUntil != nil {
		d.WaitUntil = &openapi_types.Date{Time: *waitUntil}
	}
	d.Version = &version
	return d, nil
}
