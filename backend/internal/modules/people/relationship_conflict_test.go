// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// A uniqueness refusal on a relationship carries two facts, and callers need
// both: the SENTINEL, so the transport answers 409, and the CONSTRAINT, so a
// caller that races itself can tell which rule refused it and recover.
//
// Wrapping only the sentinel drops the pg error out of the chain and makes the
// second fact unreachable — which silently disables every recovery path built
// on it, with nothing failing to say so.

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
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
			got, ok := storekit.UniqueViolation(mapped)
			if !ok {
				t.Fatal("the pg error was dropped from the chain — a caller cannot tell which constraint refused it")
			}
			if got != constraint {
				t.Fatalf("constraint = %q, want %q", got, constraint)
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
