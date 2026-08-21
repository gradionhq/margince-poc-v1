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
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// ensureOpenBirthStage guards create: deals are born open — AdvanceDeal
// is the ONE path that derives won/lost and maintains the
// closed_at/lost_reason/FX invariants. Creating straight onto a terminal
// stage would put an "open" deal on a won column — silent forecast
// corruption, no CHECK trips.
func ensureOpenBirthStage(ctx context.Context, tx pgx.Tx, stageID ids.StageID, pipelineID ids.PipelineID) error {
	var semantic string
	err := tx.QueryRow(ctx,
		`SELECT semantic FROM stage WHERE id = $1 AND pipeline_id = $2 AND archived_at IS NULL`+
			lockLiveStageTarget,
		stageID, pipelineID).Scan(&semantic)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperrors.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("resolve target stage: %w", err)
	}
	if StageSemantic(semantic).Terminal() {
		return &TerminalStageOnCreateError{Semantic: semantic}
	}
	return nil
}

// recordDealUpdate lands the write shape's audit row and its paired
// outbox events. The fan-out splits by consumer (events.md §5.3): owner
// reassignment is a first-class fact, so it emits deal.owner_changed for
// the owner transition and deal.updated only for the other fields — both
// on this request's correlation_id when they co-occur.
func recordDealUpdate(ctx context.Context, tx pgx.Tx, id ids.DealID, current crmcontracts.Deal, in UpdateDealInput, p *storekit.Patch) error {
	auditID, err := storekit.Audit(ctx, tx, "update", "deal", id.UUID, p.Before(), p.After())
	if err != nil {
		return fmt.Errorf("audit deal update: %w", err)
	}
	after := p.After()
	ownerChanged := in.OwnerID != nil && (current.OwnerId == nil || ids.UUID(*current.OwnerId) != in.OwnerID.UUID)
	if ownerChanged {
		payload := crmcontracts.PublicEventDealOwnerChanged{ToOwnerId: openapi_types.UUID(in.OwnerID.UUID)}
		if current.OwnerId != nil {
			payload.FromOwnerId = current.OwnerId
		}
		if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, payload); err != nil {
			return fmt.Errorf("emit deal.owner_changed: %w", err)
		}
	}
	if err := recordForecastMovement(ctx, tx, id, current, in, after); err != nil {
		return err
	}
	rest := make(map[string]any, len(after))
	for field, v := range after {
		if ownerChanged && field == "owner_id" {
			continue
		}
		rest[field] = v
	}
	if len(rest) > 0 {
		if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, crmcontracts.PublicEventDealUpdated{ChangedFields: rest}); err != nil {
			return fmt.Errorf("emit deal.updated: %w", err)
		}
	}
	return nil
}

// dealUpdatePatch folds the caller's sparse update onto the current row
// as a field patch. Re-pointing the deal at an organization (or partner
// organization) is a read of that record, so each link target must be
// visible under the caller's row scope before it lands in the patch.
func (s *Store) dealUpdatePatch(ctx context.Context, tx pgx.Tx, current crmcontracts.Deal, in UpdateDealInput) (*storekit.Patch, error) {
	p := storekit.NewPatch()
	if in.Name != nil {
		p.Set(dealNameColumn, current.Name, *in.Name)
	}
	if in.AmountMinor != nil {
		p.Set("amount_minor", current.AmountMinor, *in.AmountMinor)
	}
	if in.Currency != nil {
		p.Set("currency", current.Currency, *in.Currency)
	}
	if err := applyDealLinkPatches(ctx, tx, current, in, p, s.installation.EnsurePartner); err != nil {
		return nil, err
	}
	if in.ExpectedClose != nil {
		// INV-CLOSE-PAST (formulas §11): an open deal never claims a past
		// close date. Closed deals keep their historical dates editable.
		if string(current.Status) == "open" {
			if err := s.rejectPastCloseDate(ctx, tx, in.ExpectedClose); err != nil {
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

// applyDealLinkPatches sets the fields that point at another record. They are
// grouped because they share an obligation the plain columns do not: a link is
// only settable to a target the caller may see, so each one gates before it
// patches (auth.EnsureLinkTarget), and a miss reads as not-found rather than
// disclosing that the row exists.
func applyDealLinkPatches(ctx context.Context, tx pgx.Tx,
	current crmcontracts.Deal, in UpdateDealInput, p *storekit.Patch,
	ensurePartner EnsurePartner,
) error {
	if in.OrganizationID != nil {
		if err := auth.EnsureLinkTarget(ctx, tx, "organization", in.OrganizationID.UUID); err != nil {
			return err
		}
		p.Set("organization_id", current.OrganizationId, *in.OrganizationID)
	}
	if in.OwnerID != nil {
		p.Set("owner_id", current.OwnerId, *in.OwnerID)
	}
	if in.ProjectID != nil {
		if err := auth.EnsureLinkTarget(ctx, tx, "project", in.ProjectID.UUID); err != nil {
			return err
		}
		p.Set("project_id", current.ProjectId, *in.ProjectID)
	}
	return applyPartnerAttributionPatch(ctx, tx, current, in, p, ensurePartner)
}

// applyPartnerAttributionPatch writes the partner link and what that partner
// did for the deal as ONE fact, because the schema stores them as one: the
// deal_partner_attribution_pairing CHECK rejects either half alone.
//
// Three shapes reach here. Naming a partner with no attribution means
// "sourced" — that is what the link meant for every row written before the
// column existed, so the default keeps old and new callers saying the same
// thing. Naming an attribution with no partner is refused rather than
// defaulted: there is no partner to attribute it to, and inventing one is
// worse than saying no. Re-attributing a deal that already names a partner
// leaves the link alone and moves only the claim.
func applyPartnerAttributionPatch(ctx context.Context, tx pgx.Tx,
	current crmcontracts.Deal, in UpdateDealInput, p *storekit.Patch,
	ensurePartner EnsurePartner,
) error {
	if in.PartnerAttribution != nil {
		if err := validPartnerAttribution(*in.PartnerAttribution); err != nil {
			return err
		}
	}
	if in.PartnerOrganizationID == nil {
		if in.PartnerAttribution == nil {
			return nil
		}
		// An attribution alone is only meaningful when the deal already
		// names the partner it describes.
		if current.PartnerOrgId == nil {
			return &PartnerAttributionUnpairedError{}
		}
		// Re-attributing is a write ABOUT that partner, so it needs the same
		// permission naming them would: a caller who can no longer open the
		// organization — it became capture-private after the link was made —
		// may not change what the deal claims they did.
		if err := auth.EnsureLinkTarget(ctx, tx, "organization", ids.UUID(*current.PartnerOrgId)); err != nil {
			return err
		}
		p.Set("partner_attribution", current.PartnerAttribution, *in.PartnerAttribution)
		return nil
	}
	if err := auth.EnsureLinkTarget(ctx, tx, "organization", in.PartnerOrganizationID.UUID); err != nil {
		return err
	}
	// Visible is not enough: it must actually BE a partner, or the deal reads
	// as credited to somebody the accrual can never price.
	if err := ensurePartner(ctx, tx, *in.PartnerOrganizationID); err != nil {
		return err
	}
	p.Set("partner_org_id", current.PartnerOrgId, *in.PartnerOrganizationID)
	p.Set("partner_attribution", current.PartnerAttribution, resolvedAttribution(current, in))
	return nil
}

// resolvedAttribution decides what a deal that names a partner claims about
// them. An explicit attribution wins.
//
// Otherwise the claim is "sourced", including when the deal already carried a
// different one: an attribution describes a PARTNER, so it does not follow the
// deal to whoever is named next. Carrying "influenced" over from the previous
// partner would quietly decide that the new one — who may well have brought
// the deal — earns nothing, on the strength of a claim made about somebody
// else. Re-attributing without moving the partner is the separate path above.
func resolvedAttribution(current crmcontracts.Deal, in UpdateDealInput) string {
	if in.PartnerAttribution != nil {
		return *in.PartnerAttribution
	}
	if samePartner(current, in) && current.PartnerAttribution != nil {
		// Naming the partner the deal already has is not a change of partner,
		// so the claim already made about them stands.
		return string(*current.PartnerAttribution)
	}
	return attributionSourced
}

// samePartner reports whether the update names the partner the deal already
// carries, rather than pointing it at a different one.
func samePartner(current crmcontracts.Deal, in UpdateDealInput) bool {
	return current.PartnerOrgId != nil && in.PartnerOrganizationID != nil &&
		ids.UUID(*current.PartnerOrgId) == in.PartnerOrganizationID.UUID
}

// validPartnerAttribution keeps the vocabulary refusal in the store, where it
// produces a 422 naming the field, rather than letting the row hit the CHECK
// constraint and surface as an opaque database error.
func validPartnerAttribution(v string) error {
	if v != attributionSourced && v != attributionInfluenced {
		return &PartnerAttributionValueError{Got: v}
	}
	return nil
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
func (s *Store) applyMoneyInvariants(ctx context.Context, tx pgx.Tx,
	current crmcontracts.Deal, in UpdateDealInput, p *storekit.Patch,
) error {
	resultingAmount := current.AmountMinor
	if in.AmountMinor != nil {
		resultingAmount = in.AmountMinor
	}
	resultingCurrency := current.Currency
	if in.Currency != nil {
		resultingCurrency = in.Currency
	}
	if (resultingAmount == nil) != (resultingCurrency == nil) {
		return &AmountCurrencyPairError{Missing: missingMoneyHalf(resultingAmount == nil)}
	}
	if resultingAmount != nil {
		// One spelling of "a valid amount+currency" (values.Money), the
		// same rule the schema CHECKs repeat.
		if _, err := values.NewMoney(*resultingAmount, string(*resultingCurrency)); err != nil {
			return err
		}
	}

	if string(current.Status) != "open" && resultingAmount != nil &&
		(in.AmountMinor != nil || in.Currency != nil) {
		// deal_closed_at guarantees ClosedAt on a non-open row.
		base, err := s.installation.BaseCurrency(ctx, tx)
		if err != nil {
			return err
		}
		rate, rateDate, err := s.freezeFx(ctx, tx, base, *resultingCurrency, *current.ClosedAt)
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
func (s *Store) rejectPastCloseDate(ctx context.Context, tx pgx.Tx, expectedClose *time.Time) error {
	if expectedClose == nil {
		return nil
	}
	today, err := s.installationToday(ctx, tx)
	if err != nil {
		return err
	}
	y, m, d := expectedClose.Date()
	if time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Before(today) {
		return &PastCloseDateError{}
	}
	return nil
}

// installationToday reads "today" as the installation's reporting zone sees it
// (data-semantics §2 r4), returned as UTC midnight like every scanned
// date column.
func (s *Store) installationToday(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	zone, err := s.installation.Timezone(ctx, tx)
	if err != nil {
		return time.Time{}, err
	}
	// Postgres still does the arithmetic: the zone is now a bind parameter
	// instead of a column on the row, so the DST rules and the date boundary
	// stay where they were rather than being re-derived in Go.
	var today time.Time
	if err := tx.QueryRow(ctx, `SELECT (timezone($1, now()))::date`, zone).Scan(&today); err != nil {
		return time.Time{}, fmt.Errorf("resolve the installation's today: %w", err)
	}
	return dateOnly(today), nil
}

// PastCloseDateError maps to 422 close_date_past (INV-CLOSE-PAST).
type PastCloseDateError struct{}

func (e *PastCloseDateError) Error() string {
	return "an open deal cannot claim a close date in the past; pick today or later"
}

// FieldFault refuses an expected close date already in the past.
func (e *PastCloseDateError) FieldFault() (field, code, message string) {
	return "expected_close_date", "close_date_past", e.Error()
}

// AmountCurrencyPairError maps to 422: amount_minor and currency come
// together or not at all (data-model §6 money rules).
// currencyField names the wire field a money-pair refusal points at: amount and
// currency are atomic, and the currency is the half a caller can supply.
const currencyField = "currency"

// amountField is the other half of a money value.
const amountField = "amount_minor"

// closeDateField names the column a slipped forecast moves.
const closeDateField = "expected_close_date"

// missingMoneyHalf names whichever half of the pair was left out.
func missingMoneyHalf(amountMissing bool) string {
	if amountMissing {
		return amountField
	}
	return currencyField
}

// AmountCurrencyPairError refuses a half-specified money value. Missing names
// the half that was NOT supplied, because that is the input the caller adds —
// telling someone who sent a currency to fix the currency is no guidance.
type AmountCurrencyPairError struct{ Missing string }

func (e *AmountCurrencyPairError) Error() string {
	return "amount_minor and currency come together or not at all"
}

// FieldFault refuses an amount without its currency (or the reverse) — the pair is atomic.
func (e *AmountCurrencyPairError) FieldFault() (field, code, message string) {
	field = e.Missing
	if field == "" {
		field = currencyField
	}
	return field, "amount_currency_pair", e.Error()
}

// The two things a partner can have done for a deal. Sourced means they
// brought it; influenced means they helped one we already had. Commission
// accrues on sourced only, which is why the difference is stored and not
// inferred.
const (
	attributionSourced    = "sourced"
	attributionInfluenced = "influenced"
)

// partnerAttributionField names the wire field both attribution refusals
// point at.
const partnerAttributionField = "partner_attribution"

// PartnerAttributionUnpairedError maps to 422: an attribution describes a
// partner, so a deal that names none has nothing to attribute.
type PartnerAttributionUnpairedError struct{}

func (e *PartnerAttributionUnpairedError) Error() string {
	return "partner_attribution needs a partner_org_id — set the partner in the same request, or clear the attribution"
}

// FieldFault refuses an attribution on a deal that names no partner.
func (e *PartnerAttributionUnpairedError) FieldFault() (field, code, message string) {
	return partnerAttributionField, "partner_attribution_unpaired", e.Error()
}

// PartnerAttributionValueError maps to 422: the vocabulary is closed.
type PartnerAttributionValueError struct{ Got string }

func (e *PartnerAttributionValueError) Error() string {
	return "partner_attribution must be " + attributionSourced + " or " + attributionInfluenced
}

// FieldFault refuses an attribution outside the two-value vocabulary.
func (e *PartnerAttributionValueError) FieldFault() (field, code, message string) {
	return partnerAttributionField, "partner_attribution_invalid", e.Error()
}

// TerminalStageOnCreateError maps to 422: create on an open stage, then
// advance — won/lost is derived, never asserted at birth.
type TerminalStageOnCreateError struct{ Semantic string }

func (e *TerminalStageOnCreateError) Error() string {
	return "deals cannot be created on a " + e.Semantic + " stage; create open, then advance"
}

// FieldFault refuses creating a deal directly into a won/lost stage.
func (e *TerminalStageOnCreateError) FieldFault() (field, code, message string) {
	return "stage_id", "terminal_stage_on_create", e.Error()
}

func (s *Store) ArchiveDeal(ctx context.Context, id ids.DealID) (crmcontracts.Deal, error) {
	if err := auth.Require(ctx, "deal", principal.ActionDelete); err != nil {
		return crmcontracts.Deal{}, err
	}
	active, err := s.activeColumns(ctx)
	if err != nil {
		return crmcontracts.Deal{}, err
	}
	var out crmcontracts.Deal
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureWritable(ctx, tx, "deal", id.UUID); err != nil {
			return err
		}
		// A liveness probe, not a wire read — no custom columns needed.
		if _, err := readDeal(ctx, tx, id, storekit.LiveOnly, nil); err != nil {
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

		auditID, err := storekit.Audit(ctx, tx, "archive", "deal", id.UUID, nil, nil)
		if err != nil {
			return fmt.Errorf("audit deal archive: %w", err)
		}
		if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, crmcontracts.PublicEventDealArchived{}); err != nil {
			return fmt.Errorf("emit deal.archived: %w", err)
		}
		if out, err = readDealForCaller(ctx, tx, id, storekit.IncludeArchived, active); err != nil {
			return fmt.Errorf("read archived deal: %w", err)
		}
		return nil
	})
	return out, err
}
