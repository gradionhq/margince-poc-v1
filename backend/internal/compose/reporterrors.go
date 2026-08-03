// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The report engine's refusal vocabulary. report.go compiles and runs a plan;
// this names what a plan may not say.

import "fmt"

// FieldNotAllowedError maps to 422 report_field_not_allowed.
type FieldNotAllowedError struct{ Field string }

func (e *FieldNotAllowedError) Error() string {
	return fmt.Sprintf("report: field %q is outside this report's vocabulary", e.Field)
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
func (e *FieldNotAllowedError) MessageFault() (code, message string) {
	return "report_field_not_allowed",
		e.Error() + " — use a name from the report's documented dimensions, filters and measures"
}

// EmptyReportPlanError refuses a plan that would select no column at all —
// neither a grouping dimension nor an aggregate, so there is nothing to compute.
//
// MessageFault: the fix is to ADD an argument, so no supplied one is wrong.
type EmptyReportPlanError struct{}

func (e *EmptyReportPlanError) Error() string {
	return "report: this plan selects nothing"
}

func (e *EmptyReportPlanError) MessageFault() (code, message string) {
	return "report_empty_plan",
		e.Error() + " — name at least one `group_by` dimension or one entry in `aggregates`"
}
