// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// The schema's business rules reach a caller through this mapping, so the
// mapping is what decides whether a breach reads as "you broke rule X" or as
// an opaque server fault. The named-constraint list here is DERIVED from the
// migration rather than retyped: a rule added to the table and forgotten in
// the switch shows up as a test failure instead of a 500 on the one path
// nobody exercised.

import (
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// projectMigration is the table's own definition — the source of truth for
// which CHECK constraints exist.
const projectMigration = "../../../migrations/core/0131_project.up.sql"

// namedCheckPattern finds the constraints the migration names explicitly.
// An inline unnamed CHECK gets a generated name and is covered by the
// fallback arm, which is exactly what the fallback is for.
var namedCheckPattern = regexp.MustCompile(`CONSTRAINT\s+(project_[a-z_]+)\s+CHECK`)

func projectCheckConstraints(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(projectMigration)
	if err != nil {
		t.Fatalf("reading the project migration: %v", err)
	}
	var names []string
	for _, m := range namedCheckPattern.FindAllStringSubmatch(string(raw), -1) {
		names = append(names, m[1])
	}
	if len(names) == 0 {
		t.Fatal("found no named CHECK constraints — the pattern no longer reads the migration")
	}
	return names
}

// Every rule the table names must have a message of its own. Falling through
// to the generic arm is not a failure of correctness — it still answers 422 —
// but it means the caller is told a constraint name instead of what to fix.
func TestEveryNamedProjectCheckHasItsOwnRefusal(t *testing.T) {
	for _, constraint := range projectCheckConstraints(t) {
		t.Run(constraint, func(t *testing.T) {
			err := projectCheckError(constraint, "")
			var generic *ProjectConstraintError
			if errors.As(err, &generic) {
				t.Fatalf("%s falls through to the generic arm, so the caller is told %q instead of what to fix",
					constraint, err.Error())
			}
			if strings.Contains(err.Error(), constraint) {
				t.Errorf("%s reports its own constraint name to the caller: %q", constraint, err.Error())
			}
		})
	}
}

// A rule this module has not described yet still answers as a business rule,
// and says which one — an honest gap beats a 500 that says nothing.
func TestAnUnnamedProjectCheckStillReadsAsARuleBreach(t *testing.T) {
	err := projectCheckError("project_some_future_rule", "")
	var generic *ProjectConstraintError
	if !errors.As(err, &generic) {
		t.Fatalf("an undescribed constraint produced %T, want the generic rule breach", err)
	}
	if generic.Constraint != "project_some_future_rule" {
		t.Errorf("the fallback lost the constraint name: %q", generic.Constraint)
	}
}

// projectKeyConflict speaks only for the key index. Claiming another
// constraint's violation would report the wrong rule to the caller and
// swallow the real one.
func TestProjectKeyConflictClaimsOnlyItsOwnIndex(t *testing.T) {
	key := "WHR"
	for name, tc := range map[string]struct {
		err    error
		key    *string
		wantIt bool
	}{
		"its own index":      {&pgconn.PgError{Code: "23505", ConstraintName: "uq_project_key"}, &key, true},
		"another index":      {&pgconn.PgError{Code: "23505", ConstraintName: "uq_rel_project_stakeholder"}, &key, false},
		"not a uniqueness":   {&pgconn.PgError{Code: "23514", ConstraintName: "project_dates"}, &key, false},
		"no key was sent":    {&pgconn.PgError{Code: "23505", ConstraintName: "uq_project_key"}, nil, false},
		"an ordinary error":  {errors.New("connection reset"), &key, false},
		"nothing went wrong": {nil, &key, false},
	} {
		t.Run(name, func(t *testing.T) {
			got := projectKeyConflict(tc.err, tc.key)
			if tc.wantIt {
				var taken *ProjectKeyTakenError
				if !errors.As(got, &taken) {
					t.Fatalf("got %v, want the key-taken refusal", got)
				}
				if taken.Key != key {
					t.Errorf("refusal names key %q, want %q", taken.Key, key)
				}
				// The pre-check names the id when it can; this fallback runs
				// when the row appeared underneath it, so there is none.
				if taken.ExistingID != nil {
					t.Error("the race fallback named an existing id it never read")
				}
				return
			}
			if got != nil {
				t.Fatalf("claimed %v for a refusal that is not its own", got)
			}
		})
	}
}

// No refusal on this surface may hand a client a schema identifier: an index
// or constraint name describes our tables and tells a caller nothing it can
// act on.
func TestProjectRefusalsKeepSchemaNamesOffTheWire(t *testing.T) {
	key := "WHR"
	for _, err := range []error{
		&ProjectKeyTakenError{Key: key},
		&ProjectKeyShapeError{},
		&ClosedReasonRequiredError{},
		&ProjectDateRangeError{},
		&DealProjectOrgMismatchError{},
	} {
		for _, leak := range []string{"uq_", "project_key_shape", "project_closed_reason", "project_dates", "SQLSTATE"} {
			if strings.Contains(err.Error(), leak) {
				t.Errorf("%T leaks %q to the caller: %q", err, leak, err.Error())
			}
		}
	}
}
