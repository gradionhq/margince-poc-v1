// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// FxRateRow is one effective-dated FX rate: the rate that converts
// FromCurrency into the workspace base (ToCurrency) as of RateDate. Rate
// is carried as a decimal string (numeric(20,10)) — never a float.
type FxRateRow struct {
	FromCurrency string
	ToCurrency   string
	Rate         string
	RateDate     time.Time
}

// SetFxRateInput sets one effective-dated rate. EffectiveDate is the UTC
// day the rate takes effect; it may be today or later, never the past
// (strict append-forward — a past-dated row prices historical rollups and
// must never change).
type SetFxRateInput struct {
	FromCurrency  string
	Rate          string
	EffectiveDate time.Time
}

// FxRateValidationError is this module's typed 422 for a rejected rate
// write; writeStoreErr maps it to httperr.Validation on the wire.
type FxRateValidationError struct {
	Field   string
	Code    string
	Message string
}

func (e *FxRateValidationError) Error() string { return e.Message }

// FieldFault carries the rate field and code the caller must correct.
func (e *FxRateValidationError) FieldFault() (field, code, message string) {
	return e.Field, e.Code, e.Error()
}

func fxInvalid(field, code, message string) error {
	return &FxRateValidationError{Field: field, Code: code, Message: message}
}

// baseCurrencyColumn is the workspace column the whole rate sheet converts to.
const baseCurrencyColumn = "base_currency"

// isISO4217 answers whether s is a 3-letter uppercase ISO-4217-shaped code.
func isISO4217(s string) bool {
	if len(s) != 3 {
		return false
	}
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

func (s *Store) todayUTC() time.Time {
	return s.clock().UTC().Truncate(24 * time.Hour)
}

// SetFxRate appends (or corrects, same UTC day) one effective-dated FX
// rate. Admin/ops-gated; append-forward (rejects a past effective date);
// resolves ToCurrency to the workspace base and rejects from == base.
func (s *Store) SetFxRate(ctx context.Context, in SetFxRateInput) (FxRateRow, error) {
	// RBAC + pure shape validation BEFORE acquiring a connection, so a denied
	// (403) or malformed-shape (422) write never depends on pool health or
	// consumes a transaction. The clock-dependent effective-day guard lives in
	// the tx (writeFxRate), sampled at write time.
	from, err := s.prepareFxRate(ctx, in)
	if err != nil {
		return FxRateRow{}, err
	}
	var out FxRateRow
	err = s.tx(ctx, func(tx pgx.Tx) error {
		var e error
		out, e = s.writeFxRate(ctx, tx, from, in)
		return e
	})
	if err != nil {
		return FxRateRow{}, err
	}
	return out, nil
}

// SetFxRateInTx applies one effective-dated FX rate through a caller-owned
// transaction — the approval-effect path, where redeem-and-write must commit
// together (a failed write leaves the approval unconsumed and retryable).
// SetFxRate is the standalone-transaction wrapper.
func (s *Store) SetFxRateInTx(ctx context.Context, tx pgx.Tx, in SetFxRateInput) (FxRateRow, error) {
	from, err := s.prepareFxRate(ctx, in)
	if err != nil {
		return FxRateRow{}, err
	}
	return s.writeFxRate(ctx, tx, from, in)
}

// prepareFxRate runs the connection-free, clock-free gates — RBAC admission and
// currency/rate shape — returning the upper-cased from-currency. The effective-
// day resolution and past-date guard are deferred to writeFxRate so they sample
// the clock at write time.
//
// TWO CHECKS WITH ONE PURPOSE. One endpoint sets a rate, and which grant that
// needs — create for a new (currency, day), update when it replaces an existing
// rate — is only knowable from the sheet. So admission splits: this half asks
// the cheap question a caller can be refused on without a pool connection ("may
// this principal write the sheet AT ALL"), and writeFxRate demands the specific
// grant once it has read the row. Neither half is redundant: drop this one and
// an unauthorized caller costs a connection and a transaction; drop the other
// and `update` is granted by holding `create`.
func (s *Store) prepareFxRate(ctx context.Context, in SetFxRateInput) (from string, err error) {
	if err := auth.RequireAny(ctx, "fx_rate", principal.ActionCreate, principal.ActionUpdate); err != nil {
		return "", err
	}
	return normalizeFxCurrencyRate(in)
}

// replacedFxRate reads the rate this write would overwrite, if any: the second
// admission check needs insert-vs-overwrite, and an overwrite's audit owes the
// ledger the rate it displaced. The before image mirrors the after image's
// shape key for key, so the two diff to exactly the field that moved. Called
// under the currency's write-identity lock, so no concurrent writer can insert
// the row between this read and the upsert that follows.
func replacedFxRate(ctx context.Context, tx pgx.Tx, from, base string, effDate time.Time) (before map[string]any, replacing bool, err error) {
	var rate string
	err = tx.QueryRow(ctx, `
		SELECT rate::text FROM fx_rate
		WHERE from_currency = $1 AND to_currency = $2 AND rate_date = $3`,
		from, base, effDate).Scan(&rate)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read replaced fx_rate: %w", err)
	}
	return fxRateImage(from, base, rate, effDate), true, nil
}

// fxRateImage renders one audit image of a sheet row. Before and after share
// it, so the pair diffs to exactly the rate that moved rather than to a
// difference in how each side was spelled.
func fxRateImage(from, base, rate string, effDate time.Time) map[string]any {
	return map[string]any{"from": from, "to": base, "rate": rate, "date": effDate}
}

// writeFxRate does the transactional body: resolve and guard the effective day
// against a clock sampled INSIDE the tx (so a write that waited for the pool
// across UTC midnight is stored against the day it commits — append-forward
// stays true at write time), resolve the workspace base, reject from == base,
// require the grant the upsert turns out to need, then upsert the
// append-forward row and audit — all in the caller-owned tx.
func (s *Store) writeFxRate(ctx context.Context, tx pgx.Tx, from string, in SetFxRateInput) (FxRateRow, error) {
	// Serialize writers of this currency's sheet identity BEFORE sampling the
	// clock: a write that waited here for a precondition-holding transaction
	// must judge append-forward against the day it actually runs.
	if err := storekit.LockWriteIdentity(ctx, tx, "fx_rate", from); err != nil {
		return FxRateRow{}, err
	}
	today := s.todayUTC()
	eff := in.EffectiveDate
	if eff.IsZero() {
		eff = today
	}
	// Persist the same UTC-truncated day the past-date guard checks, so a
	// sub-day offset can never store a calendar date different from the validated one.
	effDate := eff.UTC().Truncate(24 * time.Hour)
	if effDate.Before(today) {
		return FxRateRow{}, fxInvalid("effective_date", "fx_rate_past", "effective_date cannot be in the past")
	}
	rate := in.Rate
	var base string
	if err := tx.QueryRow(ctx, `SELECT base_currency FROM workspace WHERE id = $1`,
		storekit.MustWorkspace(ctx)).Scan(&base); err != nil {
		return FxRateRow{}, fmt.Errorf("resolve base currency: %w", err)
	}
	if from == base {
		return FxRateRow{}, fxInvalid("from_currency", "fx_rate_base_self",
			"from_currency equals the base currency (the rate is always 1)")
	}
	before, replacing, err := replacedFxRate(ctx, tx, from, base, effDate)
	if err != nil {
		return FxRateRow{}, err
	}
	// The specific half of the admission pair prepareFxRate opened: now that
	// insert-vs-overwrite is known, demand the grant this write really needs.
	action := auth.UpsertAction(replacing)
	if err := auth.Require(ctx, "fx_rate", action); err != nil {
		return FxRateRow{}, err
	}
	var (
		out  FxRateRow
		fxID ids.UUID
	)
	if err := tx.QueryRow(ctx, `
		INSERT INTO fx_rate (workspace_id, from_currency, to_currency, rate, rate_date)
		VALUES ($1, $2, $3, $4::numeric, $5)
		ON CONFLICT (workspace_id, from_currency, to_currency, rate_date)
		DO UPDATE SET rate = EXCLUDED.rate
		RETURNING id, from_currency, to_currency, rate::text, rate_date`,
		storekit.MustWorkspace(ctx), from, base, rate, effDate,
	).Scan(&fxID, &out.FromCurrency, &out.ToCurrency, &out.Rate, &out.RateDate); err != nil {
		return FxRateRow{}, fmt.Errorf("upsert fx_rate: %w", err)
	}
	// Audit-only by ratification (EVT-NOEVT-3): the closed event catalog
	// defines no fx_rate.* type and the rate sheet is workspace config
	// recomputed price-on-read — the same ruling as the deals-owned
	// product rate-card (CreateProduct is audit-only). Ratified in
	// writeshape_test.go; inventing an fx_rate.* verb on the deal stream
	// would violate the closed catalog (contract-first, P3).
	//
	// The audit verb is the SAME word the gate above demanded, so
	// authorization_rule attributes the grant that actually admitted the write
	// rather than a plausible-looking one.
	// Both images read PERSISTED values: `before` is the displaced row as the
	// column stores it, so the after image has to be the stored row too. Echoing
	// the caller's raw string instead would diff a scale-10 numeric against
	// whatever the request happened to spell — 0.9000000000 against 0.9 — and
	// report a rate change where only the spelling moved.
	if _, err := storekit.Audit(ctx, tx, string(action), "fx_rate", fxID, before,
		fxRateImage(out.FromCurrency, out.ToCurrency, out.Rate, out.RateDate)); err != nil {
		return FxRateRow{}, fmt.Errorf("audit fx_rate %s: %w", action, err)
	}
	return out, nil
}

// normalizeFxCurrencyRate validates and upper-cases the currency and checks the
// rate is a positive plain decimal — the connection-free, clock-free shape gates
// (no DB, no time). The effective-day resolution and past-date guard live in
// writeFxRate so they sample the clock at write time, and the from == base check
// needs the base currency and lives in the tx.
func normalizeFxCurrencyRate(in SetFxRateInput) (from string, err error) {
	from = strings.ToUpper(strings.TrimSpace(in.FromCurrency))
	if !isISO4217(from) {
		return "", fxInvalid("from_currency", "fx_rate_currency", "from_currency must be a 3-letter ISO code")
	}
	rate := strings.TrimSpace(in.Rate)
	// rate != in.Rate rejects surrounding whitespace the anchored contract
	// pattern also rejects, so the server's accepted domain matches it exactly.
	if rate != in.Rate || !values.PlainDecimal(rate, 10, 10) {
		return "", fxInvalid("rate", "fx_rate_positive",
			"rate must be a plain decimal (up to 10 integer and 10 fractional digits)")
	}
	if r, _ := new(big.Rat).SetString(rate); r.Sign() <= 0 {
		return "", fxInvalid("rate", "fx_rate_positive", "rate must be greater than zero")
	}
	return from, nil
}

// ListLatestFxRates returns the head of the price sheet — the latest-dated
// row per foreign currency, which MAY be a future-scheduled rate. This is
// the editor's "sheet head" view, deliberately distinct from RateFor's
// as-of-day effective rate (effective_date <= day). Admin/ops read gate.
func (s *Store) ListLatestFxRates(ctx context.Context) ([]FxRateRow, error) {
	if err := auth.Require(ctx, "fx_rate", principal.ActionRead); err != nil {
		return nil, err
	}
	var rows []FxRateRow
	err := s.tx(ctx, func(tx pgx.Tx) error {
		r, err := tx.Query(ctx, `
			SELECT DISTINCT ON (from_currency) from_currency, to_currency, rate::text, rate_date
			FROM fx_rate
			ORDER BY from_currency, rate_date DESC`)
		if err != nil {
			return fmt.Errorf("list fx_rate: %w", err)
		}
		defer r.Close()
		rows, err = scanFxRows(r)
		return err
	})
	return rows, err
}

// ListEffectiveFxRates returns the rate in force TODAY per foreign currency —
// the latest row with rate_date <= today (store clock), the list form of
// freezeFx's as-of resolution. Deliberately distinct from ListLatestFxRates
// (sheet head, which may be future-scheduled): a refresh diff and an apply
// precondition compare against what is in force, not what is scheduled.
// Admin/ops read gate.
func (s *Store) ListEffectiveFxRates(ctx context.Context) ([]FxRateRow, error) {
	if err := auth.Require(ctx, "fx_rate", principal.ActionRead); err != nil {
		return nil, err
	}
	var rows []FxRateRow
	err := s.tx(ctx, func(tx pgx.Tx) error {
		// Sample "today" inside the transaction: a wait for a pooled
		// connection across UTC midnight must not list yesterday's cutoff.
		r, err := tx.Query(ctx, `
			SELECT DISTINCT ON (from_currency) from_currency, to_currency, rate::text, rate_date
			FROM fx_rate WHERE rate_date <= $1
			ORDER BY from_currency, rate_date DESC`, s.todayUTC())
		if err != nil {
			return fmt.Errorf("list effective fx_rate: %w", err)
		}
		defer r.Close()
		rows, err = scanFxRows(r)
		return err
	})
	return rows, err
}

// EffectiveFxRateInTx resolves the rate in force for one currency through a
// caller-owned transaction — the approval-effect precondition read, which
// must see the same state the apply writes into. It takes the currency's
// write-identity lock (so no standalone write can commit between this read
// and the dependent write) and returns the day it sampled: the caller pins
// its write to that SAME day, so a transaction that crosses UTC midnight
// fails the append-forward guard instead of overwriting the new day's
// scheduled row. found=false means no rate is in force, a materially
// different answer from any value. Admin/ops read gate.
func (s *Store) EffectiveFxRateInTx(ctx context.Context, tx pgx.Tx, fromCurrency string) (rate string, asOf time.Time, found bool, err error) {
	if err := auth.Require(ctx, "fx_rate", principal.ActionRead); err != nil {
		return "", time.Time{}, false, err
	}
	from := strings.ToUpper(strings.TrimSpace(fromCurrency))
	if err := storekit.LockWriteIdentity(ctx, tx, "fx_rate", from); err != nil {
		return "", time.Time{}, false, err
	}
	asOf = s.todayUTC()
	err = tx.QueryRow(ctx, `
		SELECT rate::text FROM fx_rate
		WHERE from_currency = $1 AND rate_date <= $2
		ORDER BY rate_date DESC LIMIT 1`, from, asOf).Scan(&rate)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", asOf, false, nil
	}
	if err != nil {
		return "", time.Time{}, false, fmt.Errorf("effective fx_rate for %s: %w", from, err)
	}
	return rate, asOf, true, nil
}

// WorkspaceBaseCurrency returns the workspace base currency — the ToCurrency
// every fx_rate row converts into. The FX refresh producer needs it to price
// an empty sheet (which has no row to read the base off), so it carries the
// same admin/ops read gate as the sheet itself; the base IS part of the
// fx_rate read surface (every row's ToCurrency).
func (s *Store) WorkspaceBaseCurrency(ctx context.Context) (string, error) {
	if err := auth.Require(ctx, "fx_rate", principal.ActionRead); err != nil {
		return "", err
	}
	var base string
	err := s.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT base_currency FROM workspace WHERE id = $1`,
			storekit.MustWorkspace(ctx)).Scan(&base)
	})
	if err != nil {
		return "", fmt.Errorf("resolve base currency: %w", err)
	}
	return base, nil
}

// FxRateHistory returns every effective-dated row for one pair, newest
// first (read-only history). Admin/ops read gate.
func (s *Store) FxRateHistory(ctx context.Context, fromCurrency string) ([]FxRateRow, error) {
	if err := auth.Require(ctx, "fx_rate", principal.ActionRead); err != nil {
		return nil, err
	}
	from := strings.ToUpper(strings.TrimSpace(fromCurrency))
	var rows []FxRateRow
	err := s.tx(ctx, func(tx pgx.Tx) error {
		r, err := tx.Query(ctx, `
			SELECT from_currency, to_currency, rate::text, rate_date
			FROM fx_rate WHERE from_currency = $1
			ORDER BY rate_date DESC`, from)
		if err != nil {
			return fmt.Errorf("fx_rate history: %w", err)
		}
		defer r.Close()
		rows, err = scanFxRows(r)
		return err
	})
	return rows, err
}

func scanFxRows(r pgx.Rows) ([]FxRateRow, error) {
	var out []FxRateRow
	for r.Next() {
		var row FxRateRow
		if err := r.Scan(&row.FromCurrency, &row.ToCurrency, &row.Rate, &row.RateDate); err != nil {
			return nil, fmt.Errorf("scan fx_rate: %w", err)
		}
		out = append(out, row)
	}
	if err := r.Err(); err != nil {
		return nil, fmt.Errorf("iterate fx_rate: %w", err)
	}
	return out, nil
}

// BaseCurrencyState is the workspace base currency together with whether it can
// still change. `Locked` is served rather than derived by the caller so a
// surface can state the constraint up front instead of only discovering it on a
// refused write (AAD-AC-N-3).
type BaseCurrencyState struct {
	BaseCurrency string
	Locked       bool
	FrozenDeals  int
}

// SetBaseCurrency adopts a new workspace base currency, while that is still
// honest to do.
//
// Every closed deal freezes `fx_rate_to_base` against the CURRENT base
// ([[data-model#DM-FX-4]]), so changing the base afterwards would silently
// reinterpret what those deals were worth — a financial claim this product has
// no authority to restate, and the reason the column's own migration comment
// calls it immutable after the first deal. Before that point it is freely
// changeable, which is the case this exists for: an installation that put the
// wrong currency in its configuration file on day one.
func (s *Store) SetBaseCurrency(ctx context.Context, code string) (BaseCurrencyState, error) {
	var out BaseCurrencyState
	// The same grant that governs the rate sheet: one authority over the
	// currency substrate rather than two that can disagree.
	if err := auth.Require(ctx, "fx_rate", principal.ActionUpdate); err != nil {
		return out, err
	}
	normalized := strings.ToUpper(strings.TrimSpace(code))
	if !isISO4217(normalized) {
		return out, &values.ParseError{
			Field: baseCurrencyColumn, Code: "base_currency_malformed",
			Message: "a base currency is a 3-letter ISO-4217 code",
		}
	}

	err := s.tx(ctx, func(tx pgx.Tx) error {
		frozen, err := frozenRateCount(ctx, tx)
		if err != nil {
			return err
		}
		if frozen > 0 {
			// The count travels with the refusal so the message can say why
			// rather than only saying no.
			out.FrozenDeals = frozen
			return fmt.Errorf("%d closed deal(s) already froze a rate against the current base: %w",
				frozen, apperrors.ErrBaseCurrencyLocked)
		}
		var before string
		if err = tx.QueryRow(ctx,
			`SELECT base_currency FROM workspace WHERE id = $1`,
			storekit.MustWorkspace(ctx)).Scan(&before); err != nil {
			return fmt.Errorf("read current base currency: %w", err)
		}
		if before == normalized {
			out.BaseCurrency = before
			return nil
		}
		if _, err := tx.Exec(ctx,
			`UPDATE workspace SET base_currency = $2 WHERE id = $1`,
			storekit.MustWorkspace(ctx), normalized); err != nil {
			return fmt.Errorf("write base currency: %w", err)
		}
		// Audit-only by ratification (EVT-NOEVT-3): the closed event catalog
		// defines no workspace-config verb, and the base currency is config on
		// the same surface as the rate sheet, which is ruled the same way.
		if _, err := storekit.Audit(ctx, tx, "update", "workspace",
			storekit.MustWorkspace(ctx),
			map[string]any{baseCurrencyColumn: before},
			map[string]any{baseCurrencyColumn: normalized}); err != nil {
			return fmt.Errorf("audit base currency change: %w", err)
		}
		out.BaseCurrency = normalized
		return nil
	})
	return out, err
}

// BaseCurrencyStatus answers the current base and whether it is still movable.
func (s *Store) BaseCurrencyStatus(ctx context.Context) (BaseCurrencyState, error) {
	var out BaseCurrencyState
	if err := auth.Require(ctx, "fx_rate", principal.ActionRead); err != nil {
		return out, err
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT base_currency FROM workspace WHERE id = $1`,
			storekit.MustWorkspace(ctx)).Scan(&out.BaseCurrency); err != nil {
			return fmt.Errorf("read base currency: %w", err)
		}
		frozen, err := frozenRateCount(ctx, tx)
		if err != nil {
			return err
		}
		out.FrozenDeals = frozen
		out.Locked = frozen > 0
		return nil
	})
	return out, err
}

// frozenRateCount counts the deals whose worth is already expressed against the
// current base. One is enough to lock it; the count exists for the message.
func frozenRateCount(ctx context.Context, tx pgx.Tx) (int, error) {
	var n int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM deal WHERE fx_rate_to_base IS NOT NULL`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count frozen conversion rates: %w", err)
	}
	return n, nil
}
