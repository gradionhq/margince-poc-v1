// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The activity-selection SQL the Art. 17 person-erase cascade walks: which
// timeline rows are the subject's alone, which of those a person-erase may
// actually destroy (the statutory floor shields the rest), and the unlinked
// captured mail the link-walk cannot see. Kept in one file so the three
// selectors read as one concept.

// subjectOnlyActivities selects timeline rows linked to the erased
// person and to no OTHER person — the emails, call notes and meeting
// bodies whose free text is about the subject alone. Rows shared with
// another person on the thread are excluded on purpose: redacting them
// would erase a different subject's record.
//
// A subject-only row is ALSO excluded when it is linked to an organization
// or deal under legal_hold — the same transitive freeze the retention
// engine applies (retention.go: "a hold on the subject must cover the
// evidence about them"). Without this, an activity that is subject-only to
// erasure yet transitively held to retention gets destroyed here, spoliating
// litigation-held evidence the nightly evaluator explicitly refuses to touch.
// The held-person arm is unnecessary: an activity linked to any other person
// is already not subject-only, and the erased subject itself is proven
// unheld before this runs (ErasePerson's own-hold check).
const subjectOnlyActivities = `
	SELECT l.activity_id FROM activity_link l
	WHERE l.person_id = $1
	  AND NOT EXISTS (
	    SELECT 1 FROM activity_link o
	    WHERE o.activity_id = l.activity_id
	      AND o.person_id IS NOT NULL AND o.person_id <> $1)
	  AND NOT EXISTS (
	    SELECT 1 FROM activity_link h
	    LEFT JOIN organization org ON org.id = h.organization_id
	    LEFT JOIN deal dl ON dl.id = h.deal_id
	    WHERE h.activity_id = l.activity_id
	      AND (coalesce(org.legal_hold, false) OR coalesce(dl.legal_hold, false)))`

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

// unlinkedCapturedMail selects captured mail that is ABOUT the subject by
// address and linked to no OTHER person — the class the link-walk above cannot
// see, under the same exclusion it uses.
//
// It exists because ADR-0072 stopped creating a counterparty for every captured
// message. Under ADR-0063 every mail ensured a person, so a link always
// existed and walking links covered everything; a deferred, noise-dispositioned
// or still-unsure sender now produces activities with no link at all. Erasure
// that only walks links would leave that mail — the address, the subject line
// and the body — sitting in the timeline after the subject exercised Art. 17.
//
// Mail also linked to someone else belongs to that person's record too, and
// redacting it would erase a different subject's history.
// $1 is the person; $2 the subject's addresses. The `m` alias keeps this
// selector distinct from the `a`-aliased activity the correspondence floor
// filters when redactSubjectTimeline wraps both id sets in one UPDATE.
const unlinkedCapturedMail = `
	SELECT m.id FROM activity m
	WHERE m.counterparty_email = ANY($2)
	  AND m.captured_by LIKE 'connector:%'
	  AND NOT EXISTS (
	    SELECT 1 FROM activity_link o
	    WHERE o.activity_id = m.id
	      AND o.person_id IS NOT NULL AND o.person_id <> $1)`
