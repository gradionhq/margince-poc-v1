// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The report engine's refusal vocabulary. report.go compiles and runs a plan;
// this names what a plan may not say.

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// reportFieldNotAllowedCode is the ONE 422 code crm.yaml declares for this
// response. Named once because three refusals share it: minting a second in the
// build would put an undocumented code in front of a client that branches on the
// documented one (P3 — the contract wins).
const reportFieldNotAllowedCode = "report_field_not_allowed"

// The plan-argument slots, named once. Each appears in the refusal that says
// which slot rejected a name AND in the list of what this tool takes, and a slot
// spelled differently in the two reads as two different things.
const (
	slotGroupBy    = "group_by"
	slotFilters    = "filters"
	slotAggregates = "aggregates"
)

// FieldNotAllowedError maps to 422 report_field_not_allowed.
type FieldNotAllowedError struct {
	Field string
	// Slot is which plan argument refused the name, spelled EXACTLY as the
	// argument is — `group_by`, `filters`, `aggregates` — so a client reads one
	// token and not two for the same thing.
	//
	// It exists because a report's three vocabularies are NOT the same set, and
	// saying "outside this report's vocabulary" when only one of them refused is
	// a claim the server itself disproves: `owner_id` is not a dimension of
	// deals-by-stage and IS a filter of it, so a caller who believed the
	// unqualified sentence would abandon a query this engine would have run.
	// Empty falls back to the unqualified wording, for the sites that refuse a
	// name belonging to no argument in particular.
	Slot string
	// Allowed is the vocabulary the caller should have drawn from, when the
	// site that refused had it in hand. Empty is honest silence, not a hole:
	// some refusals are about an aggregate FUNCTION or a derivation handle,
	// where there is no per-report list to quote.
	Allowed []string
}

func (e *FieldNotAllowedError) Error() string {
	if e.Slot == "" {
		return fmt.Sprintf("report: field %q is outside this report's vocabulary", e.Field)
	}
	return fmt.Sprintf("report: this report's `%s` does not accept %q", e.Slot, e.Field)
}

// MessageFault carries the 422 verdict on the error itself, so the ONE taxonomy
// (httperr.Classify) answers it on every surface that can reach this engine —
// the MCP tool surface reaches it through run_report and runs no HTTP helper.
//
// MessageFault rather than FieldFault: the rejected name is a VALUE the caller
// placed inside group_by/filters/aggregates, not a contract field path of its
// own. Putting it in a field slot would tell a client to fix an argument by that
// name, which is the mistake MessageFault exists to avoid. The message quotes the
// token, which is what locates it.
//
// The vocabulary itself rides in the message when the refusing site had it.
// Pointing at "the report's documented dimensions, filters and measures" named
// something no tool on this surface yielded, so a caller read that sentence,
// found nothing to read, and guessed names until one worked. A refusal that
// withholds the answer it is holding costs a round trip per guess.
func (e *FieldNotAllowedError) MessageFault() (code, message string) {
	if len(e.Allowed) == 0 {
		return reportFieldNotAllowedCode,
			e.Error() + " — use a name this report declares"
	}
	return reportFieldNotAllowedCode,
		e.Error() + " — expected one of: " + strings.Join(e.Allowed, ", ")
}

// UnknownReportError refuses a prebuilt report key the catalog does not serve,
// naming the keys it does.
//
// It stays a 404 by wrapping apperrors.ErrNotFound, and that is the contract's
// call, not a preference: crm.yaml declares BOTH 404 and 422 on runReport, and
// scopes the 422 to "the plan failed validation (out-of-vocabulary field,
// invalid aggregate)" — about the plan's FIELDS. `report` is the path parameter
// naming the resource, so a key that names nothing is a 404 (P3). An earlier
// pass made this a 422 on the reasoning that a static catalog has no row scope
// to hide behind; the integration lane caught it, and the contract settles it.
//
// What was actually wrong survives the correction: the refusal used to name
// nothing available, so a caller could not tell a typo from a denial. The keys
// ride in the message now.
type UnknownReportError struct {
	Report string
	Served []string
}

func (e *UnknownReportError) Error() string {
	return fmt.Sprintf("report %q is not a report this installation serves; the prebuilt reports are: %s",
		e.Report, strings.Join(e.Served, ", "))
}

// Unwrap puts it on the sentinel table's 404 row. Deliberately NOT a
// MessageFault: that interface forces 422, and Classify reaches it first.
func (e *UnknownReportError) Unwrap() error { return apperrors.ErrNotFound }

// allowedReportNames renders a report vocabulary for a refusal to carry, sorted
// because map iteration is not.
func allowedReportNames(vocabulary map[string]string) []string {
	return slices.Sorted(maps.Keys(vocabulary))
}

// EmptyReportPlanError refuses a plan that would select no column at all —
// neither a grouping dimension nor an aggregate, so there is nothing to compute.
//
// MessageFault: the fix is to ADD an argument, so no supplied one is wrong.
type EmptyReportPlanError struct{}

func (e *EmptyReportPlanError) Error() string {
	return "report: this plan selects nothing"
}

// MessageFault reuses report_field_not_allowed rather than minting a code.
//
// crm.yaml declares exactly one 422 code for this response, and inventing a
// second in the build would put an undocumented code in front of a client that
// branches on the documented one (P3: the contract wins). The MESSAGE is what
// distinguishes the two conditions, and it is the half a caller acts on. A
// dedicated `report_empty_plan` belongs in an upstream contract change.
func (e *EmptyReportPlanError) MessageFault() (code, message string) {
	return reportFieldNotAllowedCode,
		e.Error() + " — name at least one `group_by` dimension or one entry in `aggregates`"
}

// ReservedAliasError refuses an aggregate alias that would collide with the
// column the transport injects into every row.
//
// Its own type rather than a FieldNotAllowedError, because the two refuse
// opposite things. A field name is checked against a CLOSED vocabulary, so
// naming that vocabulary is the fix. An alias is OPEN — the caller invents it,
// and every name but this one is accepted — so a message quoting a list would
// state a rule that does not exist.
type ReservedAliasError struct{ Alias string }

func (e *ReservedAliasError) Error() string {
	return fmt.Sprintf("report: %q is reserved for the drill-through handle this surface adds to every row", e.Alias)
}

// MessageFault reuses the contract's one declared 422 code, for the reason
// EmptyReportPlanError records below.
func (e *ReservedAliasError) MessageFault() (code, message string) {
	return reportFieldNotAllowedCode, e.Error() + " — name the aggregate anything else"
}
