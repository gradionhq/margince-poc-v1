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
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
)

// projectMigration is the table's own definition — the source of truth for
// which CHECK constraints exist. core opens with one baseline file holding every
// table; a later migration that adds a CHECK adds a file of its own, and this
// reads the declaration rather than the amendments.
const projectMigration = "../../../migrations/core/0001_baseline.up.sql"

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

// unreachableChecks are the table's CHECKs no request can violate, so a bespoke
// message for one would be a branch no caller can execute.
//
// A WAIVER, not a fixture: it exempts its subjects from this file's obligation,
// and gatekit.Waive is what makes that exemption say its cost, hold a floor on
// the reason's length, and fail once an entry stops matching live code.
//
// The bar for an entry is that NO WRITER CAN REACH IT — verified against the
// store, not inferred from the contract. The contract declaring `phase` an enum
// is NOT such a reason: httperr.Decode does not validate enums and this
// installation runs no request-validator middleware, so an unknown phase reaches
// the CHECK. That constraint has a real message now (ProjectPhaseError).
var unreachableChecks = gatekit.Waive(map[string]string{
	"project_visibility_check": "no writer names the visibility column — head " +
		"narrowed the CHECK to visibility = 'workspace', the contract exposes no " +
		"project visibility field, and nothing in the store sets one, so no " +
		"request can produce a row that violates it",
})

// raisedCheck is the error Postgres hands the store when a project CHECK
// fires — the input projectCheckError translates, and the value it returns
// untranslated for a rule this module has not described.
func raisedCheck(constraint string) error {
	return fmt.Errorf("apply project patch: %w",
		&pgconn.PgError{Code: "23514", TableName: "project", ConstraintName: constraint})
}

// Every rule the table names must have a message of its own. Falling through
// is not a failure of correctness — httperr's net still answers 422 — but it
// means the caller is told to check their values with no clue which one.
func TestEveryNamedProjectCheckHasItsOwnRefusal(t *testing.T) {
	defer unreachableChecks.AssertAllMatched(t)
	for _, constraint := range projectCheckConstraints(t) {
		if unreachableChecks.Waived(t, constraint) {
			continue
		}
		t.Run(constraint, func(t *testing.T) {
			raised := raisedCheck(constraint)
			err := projectCheckError(raised, constraint, "")
			if errors.Is(err, raised) {
				t.Fatalf("%s falls through untranslated, so the caller is told to check every value it sent",
					constraint)
			}
			if strings.Contains(err.Error(), constraint) {
				t.Errorf("%s reports its own constraint name to the caller: %q", constraint, err.Error())
			}
		})
	}
}

// A rule this module has not described goes to the caller through httperr's
// constraint net, not through a spelling of that net here: still a 422, and
// still without our constraint name in it.
//
// The name is the point. A module-local fallback can only put the CONSTRAINT
// into the sentence — that is the one thing it knows — and the constraint is
// our schema. This asserts the whole way to the wire fault rather than
// stopping at "the error was passed along", because passing it along is only
// correct if what receives it answers well.
func TestAnUnnamedProjectCheckIsAnsweredByTheConstraintNet(t *testing.T) {
	const constraint = "project_some_future_rule"
	raised := raisedCheck(constraint)

	err := projectCheckError(raised, constraint, "")
	if !errors.Is(err, raised) {
		t.Fatalf("an undescribed constraint produced %T, want the raised error untranslated", err)
	}

	fault, ok := httperr.Classify(err)
	if !ok {
		t.Fatal("the constraint net did not classify an undescribed CHECK, so the caller gets a 500 telling them to retry")
	}
	if fault.Status != http.StatusUnprocessableEntity || fault.Code != "value_not_allowed" {
		t.Errorf("status/code = %d/%s, want 422/value_not_allowed", fault.Status, fault.Code)
	}
	if strings.Contains(fault.Detail, constraint) || len(fault.Fields) != 0 {
		t.Errorf("the refusal discloses the constraint: detail=%q fields=%+v", fault.Detail, fault.Fields)
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
		&ProjectPhaseError{},
		&ProjectDateRangeError{},
		&DealProjectOrgMismatchError{},
	} {
		for _, leak := range []string{"uq_", "project_key_shape", "project_closed_reason", "project_dates", "project_phase_check", "SQLSTATE"} {
			if strings.Contains(err.Error(), leak) {
				t.Errorf("%T leaks %q to the caller: %q", err, leak, err.Error())
			}
		}
	}
}
