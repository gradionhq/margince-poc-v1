// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// A uniqueness refusal on a relationship carries two facts, and callers need
// both: the SENTINEL, so the transport answers 409, and the CONSTRAINT, so a
// caller that races itself can tell which rule refused it and recover.
//
// It must carry them WITHOUT the driver error. A *pgconn.PgError anywhere in
// the chain makes httperr read the refusal as an infrastructure fault — the
// client's detail is blanked and every ordinary duplicate logs at ERROR — and
// the agent runner echoes err.Error() into its transcript, which would put
// SQLSTATE text into a model prompt.

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

func TestRelationshipUniquenessRefusalKeepsBothTheSentinelAndTheConstraint(t *testing.T) {
	for _, constraint := range []string{
		"uq_rel_current_primary_employer",
		"uq_rel_deal_person_role",
		"uq_rel_project_stakeholder",
	} {
		t.Run(constraint, func(t *testing.T) {
			mapped := mapRelationshipConstraint(
				&pgconn.PgError{Code: "23505", ConstraintName: constraint}, "project_stakeholder")

			if !errors.Is(mapped, apperrors.ErrConflict) {
				t.Fatalf("mapped error %v does not carry ErrConflict — the transport would not answer 409", mapped)
			}
			var conflict *RelationshipConflictError
			if !errors.As(mapped, &conflict) {
				t.Fatal("the constraint was dropped — a caller cannot tell which rule refused it, so no recovery path can fire")
			}
			if conflict.Constraint != constraint {
				t.Fatalf("constraint = %q, want %q", conflict.Constraint, constraint)
			}
			// The driver error must NOT survive: httperr reads a PgError in the
			// chain as an infrastructure fault, and the agent runner would echo
			// its SQLSTATE text into a model prompt.
			var pgErr *pgconn.PgError
			if errors.As(mapped, &pgErr) {
				t.Fatalf("the driver error rode along: %v", mapped)
			}
			if strings.Contains(mapped.Error(), "SQLSTATE") || strings.Contains(mapped.Error(), "23505") {
				t.Fatalf("the message carries Postgres internals: %q", mapped.Error())
			}
		})
	}
}

// A refusal that is not a uniqueness violation must pass through untouched, so
// the recovery probe above cannot misfire on an unrelated failure.
func TestANonUniquenessErrorIsNotDressedAsAConflict(t *testing.T) {
	cause := errors.New("connection reset")
	if mapped := mapRelationshipConstraint(cause, "project_stakeholder"); !errors.Is(mapped, cause) {
		t.Fatalf("mapped = %v, want the original error preserved", mapped)
	}
	if errors.Is(mapRelationshipConstraint(cause, "project_stakeholder"), apperrors.ErrConflict) {
		t.Fatal("an unrelated failure was reported as a conflict")
	}
}
