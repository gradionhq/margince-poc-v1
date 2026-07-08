// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The designer's dry-run (A72/ADR-0035 Am.1): which records does this
// automation's When/If match RIGHT NOW, and how often would it have
// fired lately — WITHOUT applying anything. The match runs through the
// same canonical predicate engine the filter surfaces use
// (storekit.CompilePredicate, B-E15.10a), read-only end to end: a
// preview is a read, so it writes no domain, audit, or outbox row, but
// it IS gated like a read — the automation object gate plus the target
// table's own read gate and row scope.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The contract's window default and the sanity bound on it: the
// would-have-fired estimate is a designer aid over recent history, not a
// reporting query.
const (
	previewDefaultWindowDays = 30
	previewMaxWindowDays     = 365
	previewSampleLimit       = 5
)

// AutomationPreviewInput carries the optional draft override: nil fields
// preview the stored instance as-is; a key/params pair previews an
// edited or not-yet-saved recipe (the editor's preview-before-save).
type AutomationPreviewInput struct {
	Key        *string
	Params     map[string]any
	WindowDays *int
}

// AutomationPreviewResult is the blast radius: visible matches, the
// honest count of matches the caller may NOT see (masked, never silently
// dropped), a small visible sample, and the trailing-window firing
// estimate (nil when the window count is not computable for the type).
type AutomationPreviewResult struct {
	MatchesNow           int
	ExcludedByPermission int
	Sample               []ids.UUID
	WindowDays           int
	WouldHaveFired       *int
}

// previewDef is one catalog type's dry-run definition: the record table
// its When/If ranges over, the closed field vocabulary + predicate that
// IS the match, and the trailing-window firing count.
type previewDef struct {
	table     string
	baseWhere string
	fields    map[string]storekit.Field
	match     storekit.Predicate
	// firedCount counts trigger occurrences since the window start —
	// workspace-level (RLS-bounded), an estimate of event volume rather
	// than a per-row visibility question.
	firedCount func(ctx context.Context, tx pgx.Tx, since time.Time) (int, error)
}

// previewDefs maps every catalog key to its dry-run definition; the
// catalog is closed, so a key without a preview is a programming error a
// fitness test catches, never a silent empty preview.
func previewDefs() map[string]previewDef {
	return map[string]previewDef{
		"route_lead": {
			table:     "lead",
			baseWhere: "t.archived_at IS NULL",
			fields: map[string]storekit.Field{
				"status":   {Expr: "t.status", Type: storekit.FieldPicklist},
				"owner_id": {Expr: "t.owner_id", Type: storekit.FieldID},
			},
			// When: lead.created. If: the router only assigns where no
			// owner is set — so the blast radius now is the open, unrouted
			// lead pool.
			match: storekit.Predicate{And: []storekit.Predicate{
				{Field: "status", Op: storekit.OpIn, Value: []any{"new", "working"}},
				{Field: "owner_id", Op: storekit.OpExists, Value: false},
			}},
			firedCount: func(ctx context.Context, tx pgx.Tx, since time.Time) (int, error) {
				// Every lead created in the window was one firing —
				// including leads since archived or routed.
				var n int
				err := tx.QueryRow(ctx,
					`SELECT count(*) FROM lead WHERE created_at >= $1`, since).Scan(&n)
				return n, err
			},
		},
		"stage_change_create_task": {
			table:     "deal",
			baseWhere: "t.archived_at IS NULL",
			fields: map[string]storekit.Field{
				"status": {Expr: "t.status", Type: storekit.FieldPicklist},
			},
			// When: deal.stage_changed. If: only OPEN destinations mint a
			// follow-up — so the records in range now are the open deals.
			match: storekit.Predicate{Field: "status", Op: storekit.OpEq, Value: "open"},
			firedCount: func(ctx context.Context, tx pgx.Tx, since time.Time) (int, error) {
				var n int
				err := tx.QueryRow(ctx, `
					SELECT count(*) FROM deal_stage_history h
					JOIN stage s ON s.id = h.to_stage_id
					WHERE h.changed_at >= $1 AND s.semantic = 'open'`, since).Scan(&n)
				return n, err
			},
		},
	}
}

// Preview evaluates the automation's When/If against current workspace
// data without applying anything. The stored instance anchors RBAC and
// existence-hiding even for a draft override: previewing under a foreign
// id answers 404 exactly like Get.
func (s *AutomationStore) Preview(ctx context.Context, id ids.AutomationID, in AutomationPreviewInput) (AutomationPreviewResult, error) {
	stored, err := s.Get(ctx, id)
	if err != nil {
		return AutomationPreviewResult{}, err
	}
	def, window, err := resolvePreviewRecipe(stored, in)
	if err != nil {
		return AutomationPreviewResult{}, err
	}
	// The dry-run reads the target records, so it carries their read
	// gate — the same admission a list over the same table demands.
	if err := auth.Require(ctx, def.table, principal.ActionRead); err != nil {
		return AutomationPreviewResult{}, err
	}

	since := s.now().UTC().AddDate(0, 0, -window)
	res := AutomationPreviewResult{WindowDays: window}
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		return def.measure(ctx, tx, since, &res)
	})
	if err != nil {
		return AutomationPreviewResult{}, err
	}
	return res, nil
}

// resolvePreviewRecipe picks the recipe under preview — the stored
// instance, or the request's draft override — and validates it exactly
// the way a save would, so the editor's preview 422s match its save 422s.
func resolvePreviewRecipe(stored Automation, in AutomationPreviewInput) (previewDef, int, error) {
	key := stored.Key
	if in.Key != nil {
		key = *in.Key
	}
	entry, ok := CatalogEntryByKey(key)
	if !ok {
		return previewDef{}, 0, &ParamError{Field: "key", Reason: "not a catalog automation type"}
	}
	if in.Params != nil {
		if err := entry.Validate(in.Params); err != nil {
			return previewDef{}, 0, err
		}
	}
	window := previewDefaultWindowDays
	if in.WindowDays != nil {
		window = *in.WindowDays
		if window < 1 || window > previewMaxWindowDays {
			return previewDef{}, 0, &ParamError{Field: "window_days",
				Reason: fmt.Sprintf("must be between 1 and %d days", previewMaxWindowDays)}
		}
	}
	def, ok := previewDefs()[key]
	if !ok {
		return previewDef{}, 0, fmt.Errorf("crmagents: catalog key %q has no preview definition", key)
	}
	return def, window, nil
}

// measure computes the blast radius inside the caller's workspace-bound
// read transaction: total matches, visible matches + sample under the
// caller's row scope, and the trailing-window firing count.
func (def previewDef) measure(ctx context.Context, tx pgx.Tx, since time.Time, res *AutomationPreviewResult) error {
	// Workspace-wide matches: the honest denominator behind
	// excluded_by_permission (RLS still bounds the tenant).
	var totalArgs []any
	matchSQL, err := storekit.CompilePredicate(def.match, def.fields, registerArg(&totalArgs))
	if err != nil {
		return err
	}
	var total int
	if err := tx.QueryRow(ctx, storekit.SQLf(
		`SELECT count(*) FROM %s t WHERE %s AND %s`, def.table, def.baseWhere, matchSQL),
		totalArgs...).Scan(&total); err != nil {
		return err
	}

	// Visible matches + sample: the same predicate AND the caller's row
	// scope — a preview never widens what its caller may see.
	var args []any
	visibleSQL, err := storekit.CompilePredicate(def.match, def.fields, registerArg(&args))
	if err != nil {
		return err
	}
	scope, err := auth.ScopeClauseFor(ctx, def.table, "t", registerArg(&args))
	if err != nil {
		return err
	}
	visibleWhere := def.baseWhere + " AND " + visibleSQL
	if scope != "" {
		visibleWhere += " AND " + scope
	}
	if err := tx.QueryRow(ctx, storekit.SQLf(
		`SELECT count(*) FROM %s t WHERE %s`, def.table, visibleWhere), args...).Scan(&res.MatchesNow); err != nil {
		return err
	}
	res.ExcludedByPermission = total - res.MatchesNow

	rows, err := tx.Query(ctx, storekit.SQLf(
		`SELECT t.id FROM %s t WHERE %s ORDER BY t.id LIMIT %d`,
		def.table, visibleWhere, previewSampleLimit), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var sid ids.UUID
		if err := rows.Scan(&sid); err != nil {
			return err
		}
		res.Sample = append(res.Sample, sid)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	fired, err := def.firedCount(ctx, tx, since)
	if err != nil {
		return err
	}
	res.WouldHaveFired = &fired
	return nil
}

// registerArg is the CompilePredicate/ScopeClauseFor bind-registration
// convention over a caller-owned slice.
func registerArg(args *[]any) func(any) int {
	return func(v any) int {
		*args = append(*args, v)
		return len(*args)
	}
}
