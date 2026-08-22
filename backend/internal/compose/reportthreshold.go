// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Two kinds of filter the equality engine cannot spell on its own.
//
// A THRESHOLD compares the row against a number the caller sends ("quiet for
// at least 30 days"): there is no column the row must equal, so the spec
// renders the predicate itself over the bound value, and the value has a
// default so the report answers with no plan at all.
//
// A SCOPED filter names a row-scoped record by id ("the activities filed under
// this project"). Filtering by a record is a read of it — the page answers
// "these rows belong to that record", which a caller with no right to the
// record may not learn, and a caller outside its row scope may not learn it
// exists — so the engine runs the record's own read gate before the filter
// binds. Without it a report is an oracle: zero rows for a project you cannot
// see and three for one you can tells you both exist.

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// reportThreshold is one numeric filter: the predicate it renders over the
// bind position holding the caller's number, and the number used when the
// caller sends none.
type reportThreshold struct {
	clause       func(pos int) string
	defaultValue int
}

// withThresholdDefaults fills in every threshold the caller left out, so the
// plan echo and the derivation handles minted from it carry the number that
// actually ran rather than an absence a reader has to know the default of.
//
//craft:ignore naked-any filter values are the decoded JSON plan, schemaless by design
func withThresholdDefaults(spec reportSpec, filters map[string]any) map[string]any {
	if len(spec.thresholds) == 0 {
		return filters
	}
	out := make(map[string]any, len(filters)+len(spec.thresholds))
	maps.Copy(out, filters)
	for key, threshold := range spec.thresholds {
		if _, ok := out[key]; !ok {
			out[key] = threshold.defaultValue
		}
	}
	return out
}

// thresholdValue admits a caller's number. JSON hands a whole number over as
// float64, the tool path the same, and a derivation handle as the decimal
// string it carried through the URL; all three mean the same integer.
//
//craft:ignore naked-any the value is the decoded JSON plan's, schemaless by design
func thresholdValue(key string, value any) (int, error) {
	switch v := value.(type) {
	case int:
		return v, nil
	case float64:
		if v != math.Trunc(v) || v < 0 {
			return 0, &ThresholdValueError{Filter: key}
		}
		return int(v), nil
	case json.Number:
		n, err := v.Int64()
		if err != nil || n < 0 {
			return 0, &ThresholdValueError{Filter: key}
		}
		return int(n), nil
	case string:
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return 0, &ThresholdValueError{Filter: key}
		}
		return n, nil
	default:
		return 0, &ThresholdValueError{Filter: key}
	}
}

// ThresholdValueError refuses a threshold value that is not a whole,
// non-negative number.
type ThresholdValueError struct{ Filter string }

func (e *ThresholdValueError) Error() string {
	return fmt.Sprintf("report: this report's `%s` filter `%s` takes a whole number of days", slotFilters, e.Filter)
}

// MessageFault reuses the contract's one declared 422 code, for the reason
// EmptyReportPlanError records.
func (e *ThresholdValueError) MessageFault() (code, message string) {
	return reportFieldNotAllowedCode, e.Error() + " — send it as a number, like 30"
}

// requireFilterScopes runs the read gate of every scoped filter the caller
// set: the object grant, then the live row probe — the same two steps
// activities.RequireProjectScope takes before a timeline is narrowed by a
// project, spelled over the spec's table so a second scoped filter needs no
// second gate. Object denial is a 403; an invisible, archived or missing
// record — and an id that is not one — is the existence-hiding 404 a direct
// read gives.
//
//craft:ignore naked-any filter values are the decoded JSON plan, schemaless by design
func requireFilterScopes(ctx context.Context, tx pgx.Tx, spec reportSpec, filters map[string]any) error {
	for _, key := range slices.Sorted(maps.Keys(spec.filterScopes)) {
		value, ok := filters[key]
		if !ok || value == nil {
			continue
		}
		table := spec.filterScopes[key]
		if err := auth.Require(ctx, table, principal.ActionRead); err != nil {
			return err
		}
		text, ok := value.(string)
		if !ok {
			return &FilterValueNotAllowedError{Filter: key, Kind: jsonShapeOf(value)}
		}
		id, err := ids.Parse(text)
		if err != nil {
			return fmt.Errorf("report filter %s names no %s: %w", key, table, apperrors.ErrNotFound)
		}
		if err := auth.EnsureVisibleLive(ctx, tx, table, id); err != nil {
			return err
		}
	}
	return nil
}

// predicatesAsFilters lifts a derivation handle's string predicates into the
// plan-filter shape the scope gate reads, so both doors onto a report run the
// same gate.
//
//craft:ignore naked-any the filter shape is the decoded JSON plan's, schemaless by design
func predicatesAsFilters(predicates map[string]string) map[string]any {
	out := make(map[string]any, len(predicates))
	for key, value := range predicates {
		out[key] = value
	}
	return out
}
