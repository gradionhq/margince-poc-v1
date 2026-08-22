// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// "Gone quiet" as a predicate on the project row, spelled once. The
// projects-gone-quiet report, the project_gone_quiet signal producer and the
// morning digest's projects section all ask the same question, and three
// spellings of "older than N days" would be three answers the first time one
// of them changed.

import "fmt"

// DefaultProjectQuietDays is how long a project in flight may go without a
// filed activity before it counts as quiet, when the caller names no window.
const DefaultProjectQuietDays = 30

// ProjectInFlightSQL is the phase predicate under the quiet rule: only a
// project being pursued or delivered is expected to show activity. An
// initiative has not started and a closed project has ended, so silence on
// either is not a finding. alias names the project row.
func ProjectInFlightSQL(alias string) string {
	return fmt.Sprintf("%s.phase IN ('%s', '%s')", alias, PhasePursuing, PhaseDelivering)
}

// ProjectQuietSQL is the silence predicate: nothing was filed against the
// project for at least daysPos days, counted back from nowSQL. A project with
// no activity at all is measured from its creation — a body of work opened
// two months ago that nobody has touched is exactly what the rule exists to
// surface, and NULL would otherwise exempt it.
//
// nowSQL is an SQL expression for the instant to measure from — `now()` for a
// request, or a bound parameter for a producer pass that carries its own
// clock — and daysPos is the bind position holding the integer day count.
func ProjectQuietSQL(alias, nowSQL string, daysPos int) string {
	// nowSQL is cast so a bound parameter is typed by the comparison rather
	// than guessed as an interval from the subtraction beside it.
	return fmt.Sprintf("coalesce(%[1]s.last_activity_at, %[1]s.created_at) < (%[2]s)::timestamptz - make_interval(days => $%[3]d)",
		alias, nowSQL, daysPos)
}

// ProjectQuietAnchorSQL is the instant the quiet rule measured from — the
// same coalesce ProjectQuietSQL compares — so a caller reporting "quiet since"
// or keying a finding to one quiet episode reads the value the rule used.
func ProjectQuietAnchorSQL(alias string) string {
	return fmt.Sprintf("coalesce(%[1]s.last_activity_at, %[1]s.created_at)", alias)
}

// OpenDealBaseValueSQL is what one OPEN deal is worth in the installation's
// base currency, with baseSQL the SQL expression carrying that currency — a
// bind position, or a token a caller substitutes one for later. A deal
// already in the base currency contributes its own amount; one in another
// currency contributes its frozen base amount, which is NULL until it closes
// (the rate freezes on close, deal_advance), so it contributes nothing yet.
// ProjectDealTotalsTx and the projects-by-phase report both fold with this.
func OpenDealBaseValueSQL(alias, baseSQL string) string {
	return fmt.Sprintf("CASE WHEN %[1]s.currency = %[2]s THEN %[1]s.amount_minor ELSE %[1]s.amount_minor_base END",
		alias, baseSQL)
}
