// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The relationship-edge input refusals, split out of relationship.go when that
// file hit the 500-line cap. relationship.go owns the writes; this names what an
// edge may not be.
//
// Both types replace a RequiredFieldError that carried prose in its Field —
// "kind: <kind> endpoint shape" and "ended_at: must not precede started_at" —
// which is the slot both surfaces publish as the machine-readable field name.

// RelationshipShapeError refuses an edge whose endpoints do not match its kind
// — an employment without an organization, a deal stakeholder without a deal.
//
// It replaces a RequiredFieldError carrying "kind: <kind> endpoint shape" in its
// Field, which is the slot both surfaces publish as the machine-readable field
// name: REST put that sentence in details.errors[].field and the MCP dispatcher
// rendered it as the field token in `validation_error <sentence>=required`.
// Nothing can branch on that, and `required` was false anyway — the endpoints
// were supplied, just not the ones this kind takes.
//
// MessageFault, not FieldFault: the mismatch is between the kind and the pair of
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
	return "ended_at", "invalid_range", e.Error()
}
