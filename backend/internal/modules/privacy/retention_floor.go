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
// communication (email, call, meeting, message). The rule is stated as an
// EXCLUSION, so the narrowing at ADR-0107/A158 carried every channel message
// into the floor automatically where telegram and whatsapp used to enter it by
// name — and that decision ratifies the outcome rather than tolerating it: a
// message to a customer is external business correspondence whichever transport
// carried it, and a transport arriving later should not have to be remembered
// here to be protected. An internal note and a task are not correspondence and
// carry no statutory floor, so their
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
// less. The window is spelled as ONE instant, floorWindowEnd, read against
// now(): the same expression the erasure pins as restricted_until, so what
// this predicate shields today is exactly what the restriction holds until.
// A zero floor stringifies to a zero interval, so the ELSE branch reduces to
// `occurred_at > now()` — nothing is shielded, exactly as before.
func correspondenceFloorPredicate(intervalArg, anchorArg int) string {
	// An already-restricted row is out of every destructive path's reach,
	// unconditionally and before the floor is even considered. The data-layer
	// guard refuses a write to one, and a refusal inside the nightly pass
	// fails the whole policy — so a single restricted row left in a selector
	// would stop every later scope, every night, and a compliance engine that
	// has silently stopped running looks exactly like one with nothing to do
	// (A167/ADR-0116). The expiry sweep reaches these rows by its own
	// selector, which is the only path that may.
	return `AND a.restricted_at IS NULL
		  AND NOT (` + handelsbriefShielded(intervalArg, anchorArg) + `)`
}

// handelsbriefShielded is the floor's positive form: the activity aliased `a`
// is a Handelsbrief whose statutory window is still open. correspondenceFloorPredicate
// negates it to keep such rows out of a destructive statement; the erasure's
// restrict step selects BY it, because those are exactly the rows it must hold
// rather than destroy. One spelling, so what one path shields is what the
// other restricts — a row that fell between the two would be neither erased
// nor held.
func handelsbriefShielded(intervalArg, anchorArg int) string {
	return `a.kind NOT IN ('task','note')
		  AND (` + handelsbriefArm + `)
		  AND ` + floorWindowEnd(intervalArg, anchorArg) + ` > now()`
}

// floorWindowEnd is the instant the statutory window over the activity
// aliased `a` closes — the value the restrict step PINS as restricted_until,
// so a later change to the configured period never shortens an obligation
// already recorded (A165/ADR-0114). The year-end branch adds from Jan 1,
// where nothing clamps, and matches RetentionClass.ProtectedSince; the
// occurrence branch adds to the record's own instant, which is what
// jurisdiction.Period.Cutoff mirrors. Both are the SAME instant the shield
// reads, so a row can never sit shielded from destruction yet outside the
// window the restriction would pin — that gap would be a record neither
// erased nor held.
func floorWindowEnd(intervalArg, anchorArg int) string {
	return fmt.Sprintf(`CASE WHEN $%[2]d THEN date_trunc('year', a.occurred_at) + interval '1 year' + $%[1]d::interval
		           ELSE a.occurred_at + $%[1]d::interval END`, intervalArg, anchorArg)
}

// handelsbriefArm answers whether an activity is a Handelsbrief — correspondence
// about an actual commercial transaction, which is the only thing §257 HGB and
// §147 AO oblige anybody to keep. It filters an activity aliased `a` and takes
// no placeholders, so it renumbers nothing at the four call sites.
//
// THE STAMP FIRST, the derived rule only as a fallback, and that order is the
// decision rather than an optimisation (A165/ADR-0114). Qualification is
// reversible in the product: reopening a won deal clears its terminal fields,
// and relinking an activity deletes its existing link of that type. A rule
// that asks the question at erasure time asks it of a record whose evidence
// may have moved, and answers "ordinary mail" about a genuine Handelsbrief —
// which destroys it. Over-retention is an argument to have with a supervisory
// authority; destruction is irreversible.
//
// The derived arm stays for rows captured before the stamp existed, where the
// links are the only evidence there is. It is a floor for legacy data, not the
// rule: every row a qualifying deal touches from now on carries the stamp
// (activities.StampCorrespondenceForDeal), and the stamp decides.
//
// A deal QUALIFIES when it is won, or carries an offer past draft — a sent
// Angebot documents the preparation of a Handelsgeschäft whether or not it
// closed, which is why DEPACK-PARAM-5 prices sent offers at six years
// alongside accepted ones. An ORGANIZATION link is deliberately not enough: an
// organization is a party, not a transaction, and a Handelsbrief hangs off the
// transaction.
//
// A PROJECT link qualifies on its own (D5), and unlike a deal it needs no
// further condition: a project is a commercial engagement from the moment it
// exists, so the correspondence filed under it is about an actual transaction
// whether or not any deal on it has closed. That is also why the deal rule
// alone was too narrow — it missed correspondence from a negotiation that was
// lost and from delivery work years after the deal that started it.
//
// Like the deal arm, this is the FALLBACK for rows that predate the stamp.
// Every project link written from now on carries the class
// (activities.StampCorrespondenceForProject), and the class decides.
const handelsbriefArm = `a.retention_class IS NOT NULL
		    OR (a.retention_class IS NULL AND EXISTS (
		          SELECT 1 FROM activity_link hl
		          JOIN deal hd ON hd.id = hl.deal_id
		          WHERE hl.activity_id = a.id AND hl.entity_type = 'deal'
		            AND (hd.status = 'won'
		                 OR EXISTS (SELECT 1 FROM offer o
		                             WHERE o.deal_id = hd.id AND o.status <> 'draft'))))
		    OR (a.retention_class IS NULL AND EXISTS (
		          SELECT 1 FROM activity_link pl
		          WHERE pl.activity_id = a.id AND pl.entity_type = 'project'))`

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
