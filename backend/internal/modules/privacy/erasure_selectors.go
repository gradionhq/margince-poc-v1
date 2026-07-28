// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The activity-selection SQL the Art. 17 person-erase cascade walks: which
// timeline rows are the subject's alone, which of those a person-erase may
// actually destroy (the statutory floor shields the rest), and the unlinked
// captured mail the link-walk cannot see. Kept in one file so the three
// selectors read as one concept.

// notTransitivelyHeld excludes an activity frozen by a legal hold reached
// through its own links — the same transitive freeze the retention engine
// applies (retention.go: "a hold on the subject must cover the evidence about
// them"). Without it, an activity that is erasable to this cascade yet
// transitively held to retention gets destroyed here, spoliating
// litigation-held evidence the nightly evaluator explicitly refuses to touch.
//
// It is a function rather than a copied fragment because EVERY selector this
// file feeds into the erase UPDATE owes the exclusion, and the selectors alias
// the activity differently. A selector that could be written without it would
// re-admit exactly what its sibling just excluded, so the only way to write one
// is to name its activity expression here.
//
// The held-person arm is deliberately absent: a person-linked activity shared
// with another subject is already outside every selector below, and the erased
// subject itself is proven unheld before the cascade runs (ErasePerson's
// own-hold check).
func notTransitivelyHeld(activityID string) string {
	return `
	  AND NOT EXISTS (
	    SELECT 1 FROM activity_link h
	    LEFT JOIN organization org ON org.id = h.organization_id
	    LEFT JOIN deal dl ON dl.id = h.deal_id
	    WHERE h.activity_id = ` + activityID + `
	      AND (coalesce(org.legal_hold, false) OR coalesce(dl.legal_hold, false)))`
}

// subjectOnlyActivities selects timeline rows linked to the erased
// person and to no OTHER person — the emails, call notes and meeting
// bodies whose free text is about the subject alone. Rows shared with
// another person on the thread are excluded on purpose: redacting them
// would erase a different subject's record.
var subjectOnlyActivities = `
	SELECT l.activity_id FROM activity_link l
	WHERE l.person_id = $1
	  AND NOT EXISTS (
	    SELECT 1 FROM activity_link o
	    WHERE o.activity_id = l.activity_id
	      AND o.person_id IS NOT NULL AND o.person_id <> $1)` +
	notTransitivelyHeld("l.activity_id")

// subjectOnlyDestroyable narrows subjectOnlyActivities to the rows a
// person-erase may actually destroy: subject-only AND not shielded by the
// statutory correspondence floor. $1 is the person; $2/$3 are the floor's
// interval and calendar-year-end anchor — the SAME pins the retention
// activity selectors pass — so the person-erase cascade can never destroy a
// Handelsbrief younger than the floor the nightly evaluator refuses to touch
// (a GoBD floor bypass, F-012). With no jurisdiction floor compiled in the
// predicate reduces to a no-op, so a non-DE install erases exactly as before.
var subjectOnlyDestroyable = `
	SELECT a.id FROM activity a
	WHERE a.id IN (` + subjectOnlyActivities + `)
	  ` + correspondenceFloorPredicate(2, 3)

// unlinkedSubjectMail selects mail that is ABOUT the subject by address and
// linked to no OTHER person — the class the link-walk above cannot see, under
// the same exclusion it uses.
//
// It exists because ADR-0072 stopped creating a counterparty for every captured
// message. Under ADR-0063 every mail ensured a person, so a link always
// existed and walking links covered everything; a deferred, noise-dispositioned
// or still-unsure sender now produces activities with no link at all. Erasure
// that only walks links would leave that mail — the address, the subject line
// and the body — sitting in the timeline after the subject exercised Art. 17.
//
// It reaches mail in BOTH directions, and the symmetry is the point. Outbound
// mail this installation SENT reaches the subject the same way: the send path
// gives its activity only the links the anchor already had, so a reply anchored
// on an organization- or deal-linked thread — or on one with no person link at
// all — records the recipient's address and the whole message body while being
// linked to nobody. A direction test would have left exactly the mail we wrote
// to the subject behind, along with the delivery row behind it.
//
// counterparty_email is what makes this selector safe to state that broadly:
// only two paths ever write it — the capture sink and the send path — so a
// non-null value already means "this activity is a message", and no manually
// logged call or note can be caught by it.
//
// Mail also linked to someone else belongs to that person's record too, and
// redacting it would erase a different subject's history.
// $1 is the person; $2 the subject's addresses. The `m` alias keeps this
// selector distinct from the `a`-aliased activity the correspondence floor
// filters when redactSubjectTimeline wraps both id sets in one UPDATE — so
// commercial correspondence younger than the statutory floor is shielded here
// exactly as it is on the link-walk arm.
//
// The legal-hold exclusion matters MORE on this arm than on the link-walk one:
// a send inherits its anchor's organization and deal links, so mail on a held
// deal's thread is the ordinary shape of an activity with no person link at
// all — precisely the position a litigation hold protects.
var unlinkedSubjectMail = `
	SELECT m.id FROM activity m
	WHERE m.counterparty_email = ANY($2)
	  AND NOT EXISTS (
	    SELECT 1 FROM activity_link o
	    WHERE o.activity_id = m.id
	      AND o.person_id IS NOT NULL AND o.person_id <> $1)` +
	notTransitivelyHeld("m.id")
