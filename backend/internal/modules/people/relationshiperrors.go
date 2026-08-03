// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// What a relationship edge may not be, and the ONE mapping that turns the
// database's refusal of each rule into a typed one. relationship.go owns the
// writes; every refusal they can produce lives here.
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

// RelationshipConflictError names the uniqueness rule that refused an edge.
//
// It carries the constraint so a caller racing ITSELF — an idempotent attach
// whose insert lost to a concurrent one — can tell which rule fired and
// recover by adopting the winner. Wrapping the driver error would say the same
// thing and cost too much: a *pgconn.PgError anywhere in the chain makes
// httperr treat the refusal as an infrastructure fault (blanking the client's
// detail and logging at ERROR), and the agent runner echoes err.Error() into
// its transcript, which would put SQLSTATE text in a model prompt.
type RelationshipConflictError struct{ Constraint string }

// relationshipConflictDetails says what each rule actually refused, in the
// caller's terms. One shared sentence cannot serve all three: the primary-
// employer index is keyed on the PERSON alone, so its conflict is with a
// different company entirely — telling that caller "this already exists
// between these records" would name the wrong pair and send them looking for
// a row that is not there.
var relationshipConflictDetails = map[string]string{
	"uq_rel_current_primary_employer": "this person already has a current primary employer — end that employment, or add this one without the primary flag",
	"uq_rel_deal_person_role":         "this person already holds that role on the deal",
	projectStakeholderUnique:          "this person is already a stakeholder on the project",
}

// Error says what the caller can act on. The constraint name stays OFF the
// wire: httperr sends a sentinel's own text as the 409 detail, and an index
// name is a database internal — it tells a client nothing it can use and
// describes our schema to anyone probing it.
func (e *RelationshipConflictError) Error() string {
	if detail, ok := relationshipConflictDetails[e.Constraint]; ok {
		return detail
	}
	// A rule added to the switch above but not described here: still a
	// truthful refusal, just a less specific one.
	return "a live relationship already conflicts with this one"
}

// Is reports this as the conflict sentinel, so every transport that maps
// ErrConflict to 409 keeps doing so without knowing this type exists.
func (e *RelationshipConflictError) Is(target error) bool { return target == apperrors.ErrConflict }

// mapRelationshipConstraint turns the insert's constraint failures into
// typed input errors: the rel_* CHECKs are the kind→endpoint shape rules
// (migration 0007) — bad input, not a fault — and the partial unique
// indexes are the edge dedupe rules (a second identical edge conflicts
// with the existing one). Anything else surfaces unchanged.
func mapRelationshipConstraint(err error, kind string) error {
	if constraint, ok := storekit.CheckViolation(err); ok {
		switch constraint {
		case "rel_employment_shape", "rel_stakeholder_shape", "rel_partner_shape", "rel_project_stakeholder_shape":
			return &RelationshipShapeError{Kind: kind}
		case "rel_dates":
			return &RelationshipDatesError{}
		}
	}
	if constraint, ok := storekit.UniqueViolation(err); ok {
		switch constraint {
		case "uq_rel_current_primary_employer", "uq_rel_deal_person_role", projectStakeholderUnique:
			return &RelationshipConflictError{Constraint: constraint}
		}
	}
	return err
}
