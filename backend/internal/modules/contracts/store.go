// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package contracts

// The contract store: reads, and the row mapping every path shares.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// contractObject is this record type's RBAC object name, spelled once.
const contractObject = "contract"

// Status values. Asserted by a human or an approved proposal — never derived
// from a date, here or anywhere (ADR-0109 §2).
const (
	StatusDraft      = "draft"
	StatusActive     = "active"
	StatusExpired    = "expired"
	StatusCancelled  = "cancelled"
	StatusSuperseded = "superseded"
)

// BasisTotal and BasisAnnualized say what value_minor measures. An open-ended
// agreement has no finite total, so it records twelve months of billing and
// says so; the two are never summed into one figure (ADR-0109 §5).
const (
	BasisTotal      = "total"
	BasisAnnualized = "annualized_12m"
)

// Store owns the contract table.
type Store struct {
	// db binds the installation this store runs for (ADR-0091 §9 step 3).
	db *database.DB
	// clock is the "today" source for the derived under-contract reading;
	// injected so a date-boundary test is deterministic rather than a race
	// against midnight.
	clock func() time.Time
}

// NewStore builds the contract store.
func NewStore(db *database.DB) *Store {
	return &Store{db: db, clock: time.Now}
}

func (s *Store) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return s.db.Tx(ctx, fn)
}

// today is the date the derived reading is computed against.
func (s *Store) today() time.Time {
	return s.clock().UTC().Truncate(24 * time.Hour)
}

// contractColumns is the select list every read shares, in the order
// scanContract expects. `under_contract` is computed in SQL rather than in Go
// so that a filtered list and a single read cannot drift apart: one expression,
// one meaning of the word (CONTRACT-FORM-1).
const contractColumns = `id, organization_id, deal_id, project_id, contract_number, title,
	value_minor, currency, value_basis, fx_rate_to_base, fx_rate_date,
	starts_on, ends_on, renewal_on, auto_renew, notice_period_days,
	status, signed_on, cancellation_notice_on, cancellation_effective_on,
	superseded_by_id, source, captured_by, version, created_at, updated_at, archived_at`

// underContractSQL is CONTRACT-FORM-1 as a SQL expression, taking the as-of
// date as its one parameter.
//
// The effective end is the EARLIER of the term end and the cancellation
// effective date — LEAST ignores NULLs in Postgres, which is exactly the
// null-safe behaviour wanted here, and an absent value on both means the
// agreement is open-ended. A cancellation never extends a term that already
// ran out.
func underContractSQL(asOfPos int) string {
	return storekit.SQLf(`(
		archived_at IS NULL
		AND status NOT IN ('draft', 'superseded')
		AND (starts_on IS NULL OR starts_on <= $%[1]d)
		AND (LEAST(ends_on, cancellation_effective_on) IS NULL
		     OR $%[1]d <= LEAST(ends_on, cancellation_effective_on))
	)`, asOfPos)
}

func scanContract(row pgx.Row) (crmcontracts.Contract, error) {
	var (
		c             crmcontracts.Contract
		underContract bool
		id            ids.UUID
		orgID         ids.UUID
		dealID        *ids.UUID
		projectID     *ids.UUID
		supersededBy  *ids.UUID
		capturedBy    *ids.UUID
		status        string
		basis         string
		startsOn      *time.Time
		endsOn        *time.Time
		renewalOn     *time.Time
		signedOn      *time.Time
		noticeOn      *time.Time
		effectiveOn   *time.Time
		fxDate        *time.Time
	)
	err := row.Scan(&id, &orgID, &dealID, &projectID, &c.ContractNumber, &c.Title,
		&c.ValueMinor, &c.Currency, &basis, &c.FxRateToBase, &fxDate,
		&startsOn, &endsOn, &renewalOn, &c.AutoRenew, &c.NoticePeriodDays,
		&status, &signedOn, &noticeOn, &effectiveOn,
		&supersededBy, &c.Source, &capturedBy, &c.Version, &c.CreatedAt, &c.UpdatedAt,
		&c.ArchivedAt, &underContract)
	if err != nil {
		return crmcontracts.Contract{}, err
	}
	c.Id = openapi_types.UUID(id)
	c.OrganizationId = openapi_types.UUID(orgID)
	c.DealId = uuidPtr(dealID)
	c.ProjectId = uuidPtr(projectID)
	c.SupersededById = uuidPtr(supersededBy)
	c.CapturedBy = uuidPtr(capturedBy)
	c.ValueBasis = crmcontracts.ContractValueBasis(basis)
	contractStatus := crmcontracts.ContractStatus(status)
	c.Status = &contractStatus
	c.UnderContract = &underContract
	c.FxRateDate = datePtr(fxDate)
	c.StartsOn = datePtr(startsOn)
	c.EndsOn = datePtr(endsOn)
	c.RenewalOn = datePtr(renewalOn)
	c.SignedOn = datePtr(signedOn)
	c.CancellationNoticeOn = datePtr(noticeOn)
	c.CancellationEffectiveOn = datePtr(effectiveOn)
	return c, nil
}

// datePtr converts a nullable SQL date into the contract's date type.
func datePtr(t *time.Time) *openapi_types.Date {
	if t == nil {
		return nil
	}
	return &openapi_types.Date{Time: *t}
}

func uuidPtr(id *ids.UUID) *openapi_types.UUID {
	if id == nil {
		return nil
	}
	converted := openapi_types.UUID(*id)
	return &converted
}

// GetContract reads one agreement.
func (s *Store) GetContract(ctx context.Context, id ids.ContractID) (crmcontracts.Contract, error) {
	if err := auth.Require(ctx, contractObject, principal.ActionRead); err != nil {
		return crmcontracts.Contract{}, err
	}
	var out crmcontracts.Contract
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = readContract(ctx, tx, id, s.today())
		return err
	})
	return out, err
}

// readContract reads one agreement inside the caller's transaction, applying
// the inherited row-scope gate. A row the caller cannot see answers ErrNotFound
// rather than a denial, so a contract's existence stays hidden.
func readContract(ctx context.Context, tx pgx.Tx, id ids.ContractID, asOf time.Time) (crmcontracts.Contract, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	idPos := arg(id)
	asOfPos := arg(asOf)

	scope, err := VisibleClause(ctx, "", arg)
	if err != nil {
		return crmcontracts.Contract{}, err
	}
	where := storekit.SQLf("id = $%d", idPos)
	if scope != "" {
		where += " AND " + scope
	}

	row := tx.QueryRow(ctx, storekit.SQLf(
		`SELECT %s, %s FROM contract WHERE %s`,
		contractColumns, underContractSQL(asOfPos), where), args...)
	out, err := scanContract(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return crmcontracts.Contract{}, apperrors.ErrNotFound
	}
	if err != nil {
		return crmcontracts.Contract{}, fmt.Errorf("read contract: %w", err)
	}
	return out, nil
}
