// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The statutory correspondence floor: the boundary below which a destructive
// retention or erasure action must not touch commercial correspondence. Kept
// in its own file so both the retention selectors and the person-erase cascade
// (erasure.go) share ONE spelling of the floor.

import (
	"fmt"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/jurisdiction"
)

// correspondenceFloorPredicate is the WHERE fragment that shields commercial
// correspondence younger than the jurisdiction floor from a destructive
// action — spelled ONCE, applied by every destructive activity path: the
// retention selectors (which pass it $3/$4) AND the person-erase cascade
// (erasure.go, which passes $2/$3). Without that sharing, erasing the person
// a Handelsbrief hangs off would destroy correspondence the nightly evaluator
// refuses to touch (a GoBD floor bypass). It filters an activity aliased `a`;
// intervalArg/anchorArg say where the interval and calendar-year-end anchor
// sit in the surrounding statement.
//
// Correspondence under GoBD §147 AO is a Handelsbrief: EXTERNAL business
// communication (email, call, meeting, whatsapp, telegram). An internal note
// and a task are not correspondence and carry no statutory floor, so their
// bodies fall to the workspace policy like any other record. That boundary is
// not just prose: TestStatutoryFloorShieldsCorrespondenceFromDestruction pins
// it (a 400-day email survives, a same-age note is erased), so flipping the
// classification fails the build. Archive passes the zero period ("P0D")
// because archiving RETAINS. The interval is an ISO 8601 date interval
// (jurisdiction.Period.String) and the anchor the calendar-year-end flag
// (jurisdiction.Anchor). Postgres does the calendar arithmetic, so a six-YEAR
// statutory floor is never shortened to 2190 days across leap years — and
// under §147(4) AO the clock starts at the END of the record's calendar year,
// so a January Handelsbrief keeps almost seven calendar years, never one day
// less. The two branches deliberately differ in form: clamped interval
// ADDITION loses days at month ends (Jan-31 + 1 month = Feb-28), so the
// occurrence branch keeps the conservative `occurred_at > now() - interval`
// shape (which jurisdiction.Period.Cutoff mirrors); the year-end branch adds
// from Jan 1, where nothing clamps, and matches RetentionClass.ProtectedSince.
// A zero floor stringifies to a zero interval, so the ELSE branch reduces to
// `occurred_at > now()` — nothing is shielded, exactly as before.
func correspondenceFloorPredicate(intervalArg, anchorArg int) string {
	return fmt.Sprintf(`AND NOT (a.kind NOT IN ('task','note')
		  AND CASE WHEN $%[2]d THEN date_trunc('year', a.occurred_at) + interval '1 year' + $%[1]d::interval > now()
		           ELSE a.occurred_at > now() - $%[1]d::interval END)`, intervalArg, anchorArg)
}

// statutoryCorrespondenceFloor is the strictest compiled-in pack's
// commercial-correspondence class — the boundary below which a
// destructive retention action must not touch an email activity. The
// floors are calendar periods with a declared ANCHOR, never day counts:
// a Years*365 conversion would shorten a statutory floor across leap
// years, and ignoring a calendar-year-end anchor (§147(4) AO) would
// erase a January document almost a year early. Strictness is compared
// as ProtectedSince at ref (the pass's evaluation time): mixed-unit
// periods and mixed anchors only order against an instant. The zero
// class means no pack declares one.
func statutoryCorrespondenceFloor(ref time.Time) jurisdiction.RetentionClass {
	floor := jurisdiction.RetentionClass{}
	for _, pack := range jurisdiction.Applicable() {
		retention := pack.Retention()
		if retention == nil {
			continue
		}
		for _, class := range retention.Classes() {
			if class.Name == jurisdiction.CommercialCorrespondence && class.ProtectedSince(ref).Before(floor.ProtectedSince(ref)) {
				floor = class
			}
		}
	}
	return floor
}

// statutoryFloorArgs resolves the strictest compiled-in correspondence floor
// into the two positional args correspondenceFloorPredicate reads — the ISO
// 8601 interval and the calendar-year-end anchor flag. The person-erase
// cascade (erasure.go) passes these so it shields EXACTLY what the retention
// activity selectors do, keeping erasure.go free of the jurisdiction seam.
func statutoryFloorArgs() (interval string, yearEndAnchor bool) {
	floor := statutoryCorrespondenceFloor(time.Now())
	return floor.Keep.String(), floor.Anchor == jurisdiction.AnchorCalendarYearEnd
}
