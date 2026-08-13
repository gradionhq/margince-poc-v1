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
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// recurringPreviewUnsupportedReason is why a recurs_yearly renewal_reminder
// instance's preview refuses rather than answering — the same
// honest-gap posture previewNotYetSupported (automations_preview.go)
// takes for the two clock handlers whose static shape can't fit either:
// this dynamic previewDef's match is a LITERAL [now, now+days_before]
// snapshot over the column's raw stored value, which for a recurring
// field (a birthday stored as, say, 1990-08-01) almost never falls
// inside any real-world window — silently answering "0 matches" would
// read as "this automation is broken" when the real answer is "preview
// cannot project a recurring occurrence yet" (DESIGN.md's own "what
// stays out of scope"). Fabricating a number here is worse than an
// honest refusal, for the identical reason previewNotYetSupported's own
// doc gives.
const recurringPreviewUnsupportedReason = "preview is not yet supported for a recurring (recurs_yearly) renewal_reminder instance: the literal snapshot preview cannot project what a yearly-recurring window would match"

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
//
// It also checks date_field against the workspace's own LIVE catalog via
// catalog (AutomationStore.WithFieldCatalog, fieldcatalog.Reader — the
// same seam deals.Store/people.Store already consume, so this is not new
// cross-module plumbing): an unknown, retired, or wrong-typed column
// answers a 422 ParamError here, before renewalPreviewDef ever builds SQL
// around it, rather than reaching Postgres and surfacing as a raw 500. A
// nil catalog (the seam not wired — a test, or a role that never mounted
// it) skips this one check, the same graceful-degradation posture every
// other WithFieldCatalog consumer takes for a nil Reader.
func renewalPreviewParams(ctx context.Context, catalog fieldcatalog.Reader, stored Automation, in AutomationPreviewInput) (dateFieldScanParams, error) {
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
			return dateFieldScanParams{}, &ParamError{
				Field:  paramFieldObject,
				Reason: "object and date_field must both be set to preview a renewal reminder",
			}
		}
		return dateFieldScanParams{}, err
	}
	if !renewalReminderObjectSet[p.Object] {
		return dateFieldScanParams{}, &ParamError{
			Field:  paramFieldObject,
			Reason: "must be one of " + strings.Join(renewalReminderObjects, ", "),
		}
	}
	if err := validateRenewalPreviewDateField(ctx, catalog, p.Object, p.Column); err != nil {
		return dateFieldScanParams{}, err
	}
	return p, nil
}

// validateRenewalPreviewDateField refuses a date_field that is not, right
// now, an active date-typed custom field on object — the preview-time twin
// of customfields.Service.DateFieldCandidates' own ErrUnknownDateColumn
// check, run here instead so the refusal is a 422 ParamError rather than
// the raw database error measure() would otherwise surface (42703, an
// undefined column, if date_field names nothing real). A nil catalog skips
// this check entirely; see renewalPreviewParams' own doc for why.
func validateRenewalPreviewDateField(ctx context.Context, catalog fieldcatalog.Reader, object, column string) error {
	if catalog == nil {
		return nil
	}
	cols, err := catalog.ActiveColumns(ctx, object)
	if err != nil {
		return fmt.Errorf("automation: loading %s's active columns for preview: %w", object, err)
	}
	for _, c := range cols {
		if c.Name == column && c.Type == fieldcatalog.TypeDate {
			return nil
		}
	}
	return &ParamError{
		Field:  "params." + paramKeyDateField,
		Reason: fmt.Sprintf("%q is not an active date-typed custom field on %s", column, object),
	}
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
// match is a literal [now, now+days_before] snapshot — correct for a
// one-time instance, which is the only kind that reaches this function:
// the caller (resolvePreviewRecipe) refuses a recurs_yearly instance
// before ever calling this with recurringPreviewUnsupportedReason, since
// a literal snapshot would almost always answer a misleading "0 matches"
// for a field whose stored value is a birth year, not a real-world date
// (DESIGN.md's own "what stays out of scope").
//
// date_field's existence and DATE type are checked BEFORE this function
// ever runs (renewalPreviewParams' catalog validation, above) — by the
// time p reaches here it has already been confirmed against the
// workspace's own live custom-field catalog, so the SQL below is built
// around a column known to be real.
func renewalPreviewDef(now time.Time, p dateFieldScanParams) previewDef {
	quotedCol := pgx.Identifier{p.Column}.Sanitize()
	from := now.Format("2006-01-02")
	to := now.AddDate(0, 0, p.DaysBefore).Format("2006-01-02")
	return previewDef{
		table:     p.Object,
		baseWhere: previewBaseWhereNotArchived,
		fields: map[string]storekit.Field{
			paramKeyDateField: {Expr: "t." + quotedCol, Type: storekit.FieldDate},
		},
		match: storekit.Predicate{And: []storekit.Predicate{
			{Field: paramKeyDateField, Op: storekit.OpGte, Value: from},
			{Field: paramKeyDateField, Op: storekit.OpLte, Value: to},
		}},
		// firedCount: entities whose watched date already fell inside the
		// trailing window — the past-occurrences reading of "would have
		// fired", the same convention every other firedCount closure
		// counts against (a completed event within [since, now]). This
		// deliberately does NOT apply previewBaseWhereNotArchived, matching
		// leadPreviewDefs' assignLeadOwnerName ("every lead created in the
		// window was one firing — including leads since archived or
		// routed"): a firing that already happened is a historical fact
		// about that pass, independent of the row's CURRENT archived
		// status, so an entity archived after its renewal date fired still
		// counts here even though it is excluded from MatchesNow's
		// right-now snapshot above.
		firedCount: func(ctx context.Context, tx pgx.Tx, since time.Time) (int, error) {
			var n int
			err := tx.QueryRow(ctx, storekit.SQLf(
				`SELECT count(*) FROM %s WHERE %s BETWEEN $1 AND $2`, p.Object, quotedCol,
			),
				since.Format("2006-01-02"), now.Format("2006-01-02")).Scan(&n)
			return n, err
		},
	}
}
