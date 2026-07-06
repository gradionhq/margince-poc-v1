// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
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

		if err := ensureOpenBirthStage(ctx, tx, in.StageID, in.PipelineID); err != nil {
			return err
		}

		// INV-CLOSE-PAST (formulas §11): deals are born open, and an open
		// deal never claims a past close date — reject at source rather
		// than let the nightly corrector inherit a knowingly-invalid row.
		if err := rejectPastCloseDate(ctx, tx, in.ExpectedClose); err != nil {
			return err
		}

		// An FK argument that names a row-scoped business record is a read
		// of that record: embedding organization_id into a deal the caller
		// will read back discloses the link, so the target must be visible
		// under the caller's row scope — not merely same-workspace (which
		// the composite FK already enforces). Owner references point at
		// app_user, which carries no row scope: any workspace member may be
		// an owner, so the FK check alone governs them.
		if in.OrganizationID != nil {
			if err := auth.EnsureLinkTarget(ctx, tx, "organization", *in.OrganizationID); err != nil {
				return err
			}
		}

		id := ids.NewV7()
		_, err = tx.Exec(ctx,
			`INSERT INTO deal (id, workspace_id, name, amount_minor, currency, pipeline_id, stage_id,
			                   organization_id, owner_id, expected_close_date, source, captured_by)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			id, wsID, in.Name, in.AmountMinor, in.Currency, in.PipelineID, in.StageID,
			in.OrganizationID, in.OwnerID, in.ExpectedClose, in.Source, by)
		if err != nil {
			// Covers the remaining FKs (pipeline, owner); the stage/pipeline
			// pairing and the organization target were pre-checked above.
			if storekit.IsForeignKeyViolation(err) {
				return apperrors.ErrNotFound
			}
			return fmt.Errorf("insert deal: %w", err)
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO deal_stage_history (workspace_id, deal_id, from_stage_id, to_stage_id, changed_by, amount_minor_at_change, currency_at_change)
			 VALUES ($1, $2, NULL, $3, $4, $5, $6)`,
			wsID, id, in.StageID, by, in.AmountMinor, in.Currency); err != nil {
			return fmt.Errorf("record stage history: %w", err)
		}

		auditID, err := storekit.Audit(ctx, tx, "create", "deal", id, nil, map[string]any{"name": in.Name})
		if err != nil {
			return fmt.Errorf("audit deal create: %w", err)
		}
		if err := storekit.Emit(ctx, tx, auditID, "deal.created", "deal", id, map[string]any{"name": in.Name}); err != nil {
			return fmt.Errorf("emit deal.created: %w", err)
		}
		if out, err = readDeal(ctx, tx, id, storekit.LiveOnly); err != nil {
			return fmt.Errorf("read created deal: %w", err)
		}
		return nil
	})
	return out, err
}

// ensureOpenBirthStage guards create: deals are born open — AdvanceDeal
// is the ONE path that derives won/lost and maintains the
// closed_at/lost_reason/FX invariants. Creating straight onto a terminal
// stage would put an "open" deal on a won column — silent forecast
// corruption, no CHECK trips.
func ensureOpenBirthStage(ctx context.Context, tx pgx.Tx, stageID, pipelineID ids.UUID) error {
	var semantic string
	err := tx.QueryRow(ctx,
		`SELECT semantic FROM stage WHERE id = $1 AND pipeline_id = $2 AND archived_at IS NULL`,
		stageID, pipelineID).Scan(&semantic)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperrors.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("resolve target stage: %w", err)
	}
	if semantic == "won" || semantic == "lost" {
		return &TerminalStageOnCreateError{Semantic: semantic}
	}
	return nil
}

type UpdateDealInput struct {
	Name                  *string
	AmountMinor           *int64
	Currency              *string
	OrganizationID        *ids.UUID
	OwnerID               *ids.UUID
	PartnerOrganizationID *ids.UUID
	ExpectedClose         *time.Time
	ForecastCategory      *string
	WaitUntil             *time.Time
	IfVersion             *int64
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
		current, err := readDeal(ctx, tx, id, storekit.LiveOnly)
		if err != nil {
			return fmt.Errorf("read deal before update: %w", err)
		}

		p, err := dealUpdatePatch(ctx, tx, current, in)
		if err != nil {
			return err
		}
		if p.Empty() {
			out = current
			return nil
		}

		if err := applyMoneyInvariants(ctx, tx, current, in, p); err != nil {
			return err
		}

		if err := p.Apply(ctx, tx, "deal", id, in.IfVersion); err != nil {
			return fmt.Errorf("apply deal patch: %w", err)
		}
		if err := recordDealUpdate(ctx, tx, id, current, in, p); err != nil {
			return err
		}
		if out, err = readDeal(ctx, tx, id, storekit.LiveOnly); err != nil {
			return fmt.Errorf("read updated deal: %w", err)
		}
		return nil
	})
	return out, err
}

// recordDealUpdate lands the write shape's audit row and its paired
// outbox events. The fan-out splits by consumer (events.md §5.3): owner
// reassignment is a first-class fact, so it emits deal.owner_changed for
// the owner transition and deal.updated only for the other fields — both
// on this request's correlation_id when they co-occur.
func recordDealUpdate(ctx context.Context, tx pgx.Tx, id ids.UUID, current crmcontracts.Deal, in UpdateDealInput, p *storekit.Patch) error {
	auditID, err := storekit.Audit(ctx, tx, "update", "deal", id, p.Before(), p.After())
	if err != nil {
		return fmt.Errorf("audit deal update: %w", err)
	}
	after := p.After()
	ownerChanged := in.OwnerID != nil && (current.OwnerId == nil || ids.UUID(*current.OwnerId) != *in.OwnerID)
	if ownerChanged {
		payload := map[string]any{"to_owner_id": *in.OwnerID}
		if current.OwnerId != nil {
			payload["from_owner_id"] = *current.OwnerId
		}
		if err := storekit.Emit(ctx, tx, auditID, "deal.owner_changed", "deal", id, payload); err != nil {
			return fmt.Errorf("emit deal.owner_changed: %w", err)
		}
	}
	rest := make(map[string]any, len(after))
	for field, v := range after {
		if ownerChanged && field == "owner_id" {
			continue
		}
		rest[field] = v
	}
	if len(rest) > 0 {
		if err := storekit.Emit(ctx, tx, auditID, "deal.updated", "deal", id, rest); err != nil {
			return fmt.Errorf("emit deal.updated: %w", err)
		}
	}
	return nil
}

// dealUpdatePatch folds the caller's sparse update onto the current row
// as a field patch. Re-pointing the deal at an organization (or partner
// organization) is a read of that record, so each link target must be
// visible under the caller's row scope before it lands in the patch.
func dealUpdatePatch(ctx context.Context, tx pgx.Tx, current crmcontracts.Deal, in UpdateDealInput) (*storekit.Patch, error) {
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
		if err := auth.EnsureLinkTarget(ctx, tx, "organization", *in.OrganizationID); err != nil {
			return nil, err
		}
		p.Set("organization_id", current.OrganizationId, *in.OrganizationID)
	}
	if in.OwnerID != nil {
		p.Set("owner_id", current.OwnerId, *in.OwnerID)
	}
	if in.PartnerOrganizationID != nil {
		if err := auth.EnsureLinkTarget(ctx, tx, "organization", *in.PartnerOrganizationID); err != nil {
			return nil, err
		}
		p.Set("partner_org_id", current.PartnerOrgId, *in.PartnerOrganizationID)
	}
	if in.ExpectedClose != nil {
		// INV-CLOSE-PAST (formulas §11): an open deal never claims a past
		// close date. Closed deals keep their historical dates editable.
		if string(current.Status) == "open" {
			if err := rejectPastCloseDate(ctx, tx, in.ExpectedClose); err != nil {
				return nil, err
			}
		}
		p.Set("expected_close_date", current.ExpectedCloseDate, *in.ExpectedClose)
		// A human setting the date IS the §11 confirmation — the machine's
		// provisional guess stops excluding the deal from Commit.
		if current.CloseDateProvisional != nil && *current.CloseDateProvisional {
			p.Set("close_date_provisional", true, false)
		}
	}
	if in.ForecastCategory != nil {
		p.Set("forecast_category", current.ForecastCategory, *in.ForecastCategory)
	}
	if in.WaitUntil != nil {
		p.Set("wait_until", current.WaitUntil, *in.WaitUntil)
	}
	return p, nil
}

// applyMoneyInvariants enforces the amount/currency rules on the
// RESULTING row, not just the request. The pair comes together or not at
// all: an amount stranded without a currency would skip the FX freeze at
// close and then violate deal_closed_fx. And re-pricing a CLOSED deal
// must re-freeze FX as of the original close date, or the frozen rate
// goes stale against the new currency (silent base-currency corruption)
// — a deal closed amountless has no frozen rate at all, so adding an
// amount later would trip deal_closed_fx. Same-day rate lookup as at
// close, so roll-ups stay reproducible.
func applyMoneyInvariants(ctx context.Context, tx pgx.Tx, current crmcontracts.Deal, in UpdateDealInput, p *storekit.Patch) error {
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

	if string(current.Status) != "open" && resultingAmount != nil &&
		(in.AmountMinor != nil || in.Currency != nil) {
		// deal_closed_at guarantees ClosedAt on a non-open row.
		rate, rateDate, err := freezeFx(ctx, tx, *resultingCurrency, *current.ClosedAt)
		if err != nil {
			return fmt.Errorf("re-freeze fx for closed deal: %w", err)
		}
		p.Set("fx_rate_to_base", nil, rate)
		p.Set("fx_rate_date", nil, rateDate)
	}
	return nil
}

// rejectPastCloseDate is the write-layer half of INV-CLOSE-PAST: saving
// expected_close_date earlier than today (in the workspace zone,
// data-semantics §2 r4) on an open deal is an invalid state, not a
// hygiene warning. The nightly corrector is the other half — it clears
// rows that age into the past.
func rejectPastCloseDate(ctx context.Context, tx pgx.Tx, expectedClose *time.Time) error {
	if expectedClose == nil {
		return nil
	}
	today, err := workspaceToday(ctx, tx)
	if err != nil {
		return err
	}
	y, m, d := expectedClose.Date()
	if time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Before(today) {
		return &PastCloseDateError{}
	}
	return nil
}

// workspaceToday reads "today" as the workspace's reporting zone sees it
// (data-semantics §2 r4), returned as UTC midnight like every scanned
// date column.
func workspaceToday(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var today time.Time
	err := tx.QueryRow(ctx,
		`SELECT (timezone(timezone, now()))::date FROM workspace WHERE id = $1`,
		storekit.MustWorkspace(ctx)).Scan(&today)
	if err != nil {
		return time.Time{}, fmt.Errorf("resolve workspace-zone today: %w", err)
	}
	return dateOnly(today), nil
}

// PastCloseDateError maps to 422 close_date_past (INV-CLOSE-PAST).
type PastCloseDateError struct{}

func (e *PastCloseDateError) Error() string {
	return "an open deal cannot claim a close date in the past; pick today or later"
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

func (s *Store) ArchiveDeal(ctx context.Context, id ids.UUID) (crmcontracts.Deal, error) {
	if err := auth.Require(ctx, "deal", principal.ActionDelete); err != nil {
		return crmcontracts.Deal{}, err
	}
	var out crmcontracts.Deal
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "deal", id); err != nil {
			return err
		}
		if _, err := readDeal(ctx, tx, id, storekit.LiveOnly); err != nil {
			return err
		}
		now := time.Now().UTC()
		for _, stmt := range []string{
			`UPDATE deal SET archived_at = $2 WHERE id = $1 AND archived_at IS NULL`,
			`UPDATE relationship SET archived_at = $2 WHERE deal_id = $1 AND archived_at IS NULL`,
		} {
			if _, err := tx.Exec(ctx, stmt, id, now); err != nil {
				return fmt.Errorf("archive deal and its relationships: %w", err)
			}
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM list_member WHERE entity_type = 'deal' AND entity_id = $1`, id); err != nil {
			return fmt.Errorf("detach list memberships: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM taggable WHERE entity_type = 'deal' AND entity_id = $1`, id); err != nil {
			return fmt.Errorf("detach tags: %w", err)
		}

		auditID, err := storekit.Audit(ctx, tx, "archive", "deal", id, nil, nil)
		if err != nil {
			return fmt.Errorf("audit deal archive: %w", err)
		}
		if err := storekit.Emit(ctx, tx, auditID, "deal.archived", "deal", id, nil); err != nil {
			return fmt.Errorf("emit deal.archived: %w", err)
		}
		if out, err = readDeal(ctx, tx, id, storekit.IncludeArchived); err != nil {
			return fmt.Errorf("read archived deal: %w", err)
		}
		return nil
	})
	return out, err
}
