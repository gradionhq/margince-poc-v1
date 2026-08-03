// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The report engine's refusal vocabulary, split out of report.go when that file
// hit the 500-line cap. What separates the two is direction: report.go compiles
// and runs a plan, this names what a plan may not say.

import "fmt"

// FieldNotAllowedError maps to 422 report_field_not_allowed.
type FieldNotAllowedError struct{ Field string }

func (e *FieldNotAllowedError) Error() string {
	return fmt.Sprintf("report: field %q is outside this report's vocabulary", e.Field)
}

// MessageFault carries the 422 verdict on the error itself, so the ONE taxonomy
// (httperr.Classify) answers it wherever it travels.
//
// It used to live only in writeReportError, an HTTP-only helper. The MCP tool
// surface reaches this engine through run_report and never runs that helper, so
// an unknown `filters` key or `group_by` — precisely what the tool's own
// description promises to police — came back to the agent as "the tool failed
// for an internal reason; retry". The engine had already settled the call.
//
// MessageFault rather than FieldFault, deliberately: the rejected name is a
// VALUE the caller placed inside group_by/filters/aggregates, not a contract
// field path of its own. Putting it in a field slot would tell a client to fix
// an argument by that name, which is the mistake MessageFault exists to avoid.
// The message quotes the token, which is what locates it.
func (e *FieldNotAllowedError) MessageFault() (code, message string) {
	return "report_field_not_allowed",
		e.Error() + " — use a name from the report's documented dimensions, filters and measures"
}
