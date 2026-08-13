// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// renewal_reminder's own slice of the preview surface (automations_preview.go):
// unlike every other catalog key, its table and watched column are
// per-instance (the workspace's own object/date_field params), so it has
// no static previewDefs() entry — resolvePreviewRecipe calls into this
// file instead to build one at request time. Split into its own file
// because it is a distinct concept from the static preview registry, not
// because of size alone.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
)

// renewalPreviewParams decodes and validates the params in effect for a
// renewal_reminder preview — the draft override's if given, else the
// stored instance's own — reusing renewalDateFieldScanParams
// (timescan.go) rather than writing a third decoder for the same
// object/date_field/days_before/recurs_yearly shape (timescan.go's own
// reader, handlers_clock.go's runtime Match re-check).
//
// This validates MORE strictly than a save does:
// validateRenewalReminderParams (automations_catalog.go) only checks
// whichever keys are actually PRESENT in a save, so a stored instance can
// exist with object/date_field entirely unset — the scan's own honest
// no-op for exactly that case (scanDateFieldInstanceCandidates's doc). A
// preview must never guess a table, so here the identical condition is a
// hard refusal instead of a silent skip, and object is re-checked against
// the closed renewalReminderObjectSet even though renewalDateFieldScanParams
// itself only checks non-emptiness (a save-time Validate call may not
// have covered this exact value, e.g. a value from before the vocabulary
// was extended).
func renewalPreviewParams(stored Automation, in AutomationPreviewInput) (dateFieldScanParams, error) {
	raw := stored.Params
	if in.Params != nil {
		encoded, err := json.Marshal(in.Params)
		if err != nil {
			return dateFieldScanParams{}, fmt.Errorf("automation: encoding preview params override: %w", err)
		}
		raw = encoded
	}
	p, err := renewalDateFieldScanParams(raw)
	if err != nil {
		if errors.Is(err, errRenewalScanParamsMissing) {
			return dateFieldScanParams{}, &ParamError{Field: "params.object",
				Reason: "object and date_field must both be set to preview a renewal reminder"}
		}
		return dateFieldScanParams{}, err
	}
	if !renewalReminderObjectSet[p.Object] {
		return dateFieldScanParams{}, &ParamError{Field: "params.object",
			Reason: "must be one of " + strings.Join(renewalReminderObjects, ", ")}
	}
	return p, nil
}

// renewalPreviewDef builds one renewal_reminder instance's previewDef at
// request time: table is the instance's own validated object (one of
// renewalReminderObjects, all five of which carry archived_at — verified
// against migrations/core's own DDL for person/organization/deal/lead/
// project, not assumed), and the one field is the instance's own
// date_field column, quoted via pgx.Identifier — the SAME quoting
// pgx.Identifier{}.Sanitize() customfields/engine.go's quoteIdentifier
// wraps, reached directly here rather than through customfields (a
// module never imports a sibling, ADR-0054 §9).
//
// match is a literal [now, now+days_before] snapshot regardless of
// recurs_yearly: projecting "would this fire again next year" into a
// preview is a second, separate UX question this ticket does not answer
// (DESIGN.md's own "what stays out of scope") — a recurring instance's
// preview answers today's literal window exactly like a one-time one.
//
// date_field's existence and DATE type are NOT re-checked here: that
// needs customfields.Service.ActiveColumns, a call this function has no
// path to without importing customfields (forbidden) or compose growing
// new cross-module plumbing this ticket does not ask for. An instance
// naming a retired or wrong-typed column simply fails the SQL below with
// a clear database error when measure() runs it — propagated as an
// ordinary error, never a fabricated zero-match result.
func renewalPreviewDef(now time.Time, p dateFieldScanParams) previewDef {
	quotedCol := pgx.Identifier{p.Column}.Sanitize()
	from := now.Format("2006-01-02")
	to := now.AddDate(0, 0, p.DaysBefore).Format("2006-01-02")
	return previewDef{
		table:     p.Object,
		baseWhere: "t.archived_at IS NULL",
		fields: map[string]storekit.Field{
			"date_field": {Expr: "t." + quotedCol, Type: storekit.FieldDate},
		},
		match: storekit.Predicate{And: []storekit.Predicate{
			{Field: "date_field", Op: storekit.OpGte, Value: from},
			{Field: "date_field", Op: storekit.OpLte, Value: to},
		}},
		// firedCount: entities whose watched date already fell inside the
		// trailing window — the past-occurrences reading of "would have
		// fired", the same convention every other firedCount closure
		// counts against (a completed event within [since, now]).
		firedCount: func(ctx context.Context, tx pgx.Tx, since time.Time) (int, error) {
			var n int
			err := tx.QueryRow(ctx, storekit.SQLf(
				`SELECT count(*) FROM %s WHERE %s BETWEEN $1 AND $2`, p.Object, quotedCol),
				since.Format("2006-01-02"), now.Format("2006-01-02")).Scan(&n)
			return n, err
		},
	}
}
