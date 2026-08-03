// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// What a relationship edge may not be. relationship.go owns the writes; these
// are the input refusals its constraint mapping answers with.
//
// Each carries its verdict on itself, so one mapping serves every surface, and
// each carries the RIGHT verdict: a FieldFault names an argument the caller can
// change, a MessageFault names a condition no single argument owns.

// RelationshipShapeError refuses an edge whose endpoints do not match its kind
// — an employment without an organization, a deal stakeholder without a deal.
//
// MessageFault, not FieldFault: the mismatch is between the kind and the PAIR of
// endpoints, so no single argument is the wrong one. Naming one would send the
// caller to change a field that may well be correct.
type RelationshipShapeError struct{ Kind string }

func (e *RelationshipShapeError) Error() string {
	return "a " + e.Kind + " relationship does not take this combination of endpoints"
}

// MessageFault names the condition and what to check, with no field: the
// mismatch is between the kind and the endpoint pair, so no single argument is
// the wrong one.
func (e *RelationshipShapeError) MessageFault() (code, message string) {
	return "relationship_shape_invalid",
		e.Error() + " — check which person/organization/deal/project fields this kind requires"
}

// RelationshipDatesError refuses an edge that ended before it started.
type RelationshipDatesError struct{}

func (e *RelationshipDatesError) Error() string {
	return "`ended_at` must not precede `started_at`"
}

// FieldFault names ended_at: it is a real argument, and moving it is the fix.
func (e *RelationshipDatesError) FieldFault() (field, code, message string) {
	return "ended_at", "invalid_date_range", e.Error()
}
