// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// What the open-promise read must SPELL, and what it must never spell.
//
// The three predicates that define the set are the whole tool above it: a
// read that lost `is_done = false` would report finished work as outstanding,
// and one that lost the row scope would report a colleague's promises to a
// rep who cannot see the records they were made about. Both failures answer
// successfully, which is why they are asserted here rather than left to the
// integration lane to notice.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// openTasksWhere builds the predicate for one input, failing the test on any
// error, and returns it as one string with its bind arguments.
func openTasksWhere(ctx context.Context, t *testing.T, in ListOpenTasksInput) (string, []any) {
	t.Helper()
	args := []any{}
	arg := func(v any) int { args = append(args, v); return len(args) }
	where, err := openTasksFilter(ctx, in, arg)
	if err != nil {
		t.Fatalf("building the open-task filter: %v", err)
	}
	return joinAnd(where), args
}

func TestTheOpenTaskSetIsExactlyTaskNotDoneNotArchived(t *testing.T) {
	where, args := openTasksWhere(unscopedCtx(), t, ListOpenTasksInput{})

	if !strings.Contains(where, "a.is_done = false") {
		t.Errorf("the filter does not exclude finished work, so a ticked-off task "+
			"would be reported as an open promise: %s", where)
	}
	if !strings.Contains(where, "a.archived_at IS NULL") {
		t.Errorf("the filter does not exclude archived rows: %s", where)
	}
	// The kind is BOUND rather than interpolated, so the assertion is over the
	// argument as well as the placeholder.
	if !strings.Contains(where, "a.kind = $1") {
		t.Errorf("the filter does not pin the task kind: %s", where)
	}
	if len(args) != 1 || args[0] != "task" {
		t.Errorf("the filter bound %v, want the single argument \"task\"", args)
	}
}

func TestABoundedCallerKeepsTheTimelinesRowScope(t *testing.T) {
	where, _ := openTasksWhere(teamScopedCtx(), t, ListOpenTasksInput{})

	// bool_or is the activity row-scope walk's own token: the any-link rule
	// and the link-less-note rule spelled in one pass. Nothing else this
	// filter emits contains it.
	if !strings.Contains(where, "bool_or") {
		t.Errorf("a bounded caller's open-task filter carries no row scope, so it would "+
			"report promises made about records they cannot read: %s", where)
	}
}

func TestNarrowingToOneOwnerMatchesTheAssigneeColumn(t *testing.T) {
	owner := ids.NewV7()
	where, args := openTasksWhere(unscopedCtx(), t, ListOpenTasksInput{AssigneeID: &owner})

	if !strings.Contains(where, "a.assignee_id = $2") {
		t.Errorf("narrowing to one owner does not match assignee_id: %s", where)
	}
	if len(args) != 2 || args[1] != owner {
		t.Errorf("the filter bound %v, want the owner id in position 2", args)
	}
}

// A task can be linked to a project AND to the deal under it. A join would
// then return that one promise twice, which double-counts it against the
// sweep's bound and shows a reviewer the same commitment on two lines.
func TestNarrowingToOneRecordIsAnExistsRatherThanAJoin(t *testing.T) {
	entityType, entity := "project", ids.NewV7()
	where, args := openTasksWhere(unscopedCtx(), t, ListOpenTasksInput{
		EntityType: &entityType, EntityID: &entity,
	})

	if !strings.Contains(where, "EXISTS (") {
		t.Fatalf("narrowing to one record is not an EXISTS: %s", where)
	}
	if strings.Contains(where, "JOIN") {
		t.Errorf("narrowing to one record joins, which duplicates a task linked twice: %s", where)
	}
	if !strings.Contains(where, "l.project_id = $3") {
		t.Errorf("narrowing to a project does not match project_id: %s", where)
	}
	if len(args) != 3 || args[2] != entity {
		t.Errorf("the filter bound %v, want the record id in position 3", args)
	}
}

func TestAnUnknownRecordTypeIsRefusedRatherThanBuiltIntoTaskSQL(t *testing.T) {
	entityType, entity := "invoice", ids.NewV7()
	arg := func(any) int { return 1 }
	_, err := openTasksFilter(unscopedCtx(), ListOpenTasksInput{
		EntityType: &entityType, EntityID: &entity,
	}, arg)

	var badType *InvalidLinkTypeError
	if !errors.As(err, &badType) {
		t.Fatalf("narrowing to an unknown record type → %v, want InvalidLinkTypeError", err)
	}
	if badType.EntityType != entityType {
		t.Errorf("refusal names %q, want %q", badType.EntityType, entityType)
	}
}

func TestOneSweepIsAlwaysBounded(t *testing.T) {
	for _, tc := range []struct {
		name  string
		asked int
		want  int
	}{
		{"a caller who named no bound gets the default", 0, openTasksDefaultLimit},
		{"a negative ask cannot become an unbounded read", -1, openTasksDefaultLimit},
		{"an ask within the ceiling is honoured", 10, 10},
		{"an ask past the ceiling is capped rather than refused", openTasksMaxLimit + 1, openTasksMaxLimit},
	} {
		if got := openTasksLimit(tc.asked); got != tc.want {
			t.Errorf("%s: openTasksLimit(%d) = %d, want %d", tc.name, tc.asked, got, tc.want)
		}
	}
}

// The name projection is derived from the link vocabulary, so a record type
// added to that vocabulary without saying what a human calls it fails here
// rather than rendering an empty name in a view.
func TestEveryLinkArmProjectsADisplayName(t *testing.T) {
	projection := linkNameCoalesce("al")
	for _, target := range linkTargets {
		if target.nameColumn == "" {
			t.Errorf("link arm %q names no display column", target.kind)
			continue
		}
		want := "SELECT t." + target.nameColumn + " FROM " + string(target.kind) + " t WHERE t.id = al." + target.column
		if !strings.Contains(projection, want) {
			t.Errorf("the name projection misses the %q arm (%s): %s", target.kind, want, projection)
		}
	}
}
