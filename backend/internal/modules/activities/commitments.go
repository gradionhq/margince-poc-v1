// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// The open-promise read: task activities nobody has ticked off, oldest
// promise first.
//
// WHY IT IS NOT ListActivities WITH TWO MORE FILTERS. The timeline list
// answers "what happened, newest first" and pages on occurred_at. A promise
// is the opposite question in every dimension that matters to the query: it
// is ordered by when it comes DUE, it is bounded to the one kind that has a
// due date, and the rows that matter most are the oldest rather than the
// newest. Bolting an is_done filter onto the timeline would have given the
// right rows in the wrong order behind a cursor that cannot express the
// right one — and would have widened a contract endpoint's vocabulary for a
// caller the contract does not have.
//
// It also has an index of its own: idx_activity_tasks is
// (workspace_id, assignee_id, due_at) WHERE kind = 'task' AND is_done =
// false AND archived_at IS NULL — this read's predicate and this read's
// order, written into core 0008 before anything asked it.
//
// ONE READ, TWO CALLERS. review_commitments asks it workspace-wide or for
// one owner; prepare_handoff asks it for one project. Both want the same
// rows judged the same way, so there is one derivation rather than two that
// agree until someone changes one.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// TaskAbout is one record an open task is about, named as a human would
// recognize it.
type TaskAbout struct {
	EntityType string
	EntityID   ids.UUID
	Name       string
}

// OpenTask is one promise still outstanding: what was undertaken, by when,
// by whom, and about what.
type OpenTask struct {
	ID      ids.UUID
	Subject string
	// DueAt is absent for a task nobody dated. That is a real state and a
	// distinct one — an undated promise is not an overdue one — so it stays
	// a pointer the whole way to the wire rather than collapsing to a zero
	// time somewhere in the middle.
	DueAt *time.Time
	// AssigneeID and AssigneeName are absent together for an unassigned
	// task: a promise with no owner, which is the single most useful thing
	// this read surfaces.
	AssigneeID   *ids.UUID
	AssigneeName string
	CreatedAt    time.Time
	About        []TaskAbout
}

// ListOpenTasksInput narrows one sweep. Every field is optional except the
// bound: an empty input is the whole workspace's open promises, which is
// what "who owes what" asks for.
type ListOpenTasksInput struct {
	AssigneeID *ids.UUID
	// EntityType and EntityID narrow to the promises made about ONE record,
	// spelled with the same vocabulary the timeline filter uses. Both or
	// neither; one alone is ignored, exactly as in the timeline list.
	EntityType *string
	EntityID   *ids.UUID
	// Limit bounds the sweep. A caller passing zero or less gets
	// openTasksDefaultLimit rather than an unbounded read.
	Limit int
}

// openTasksDefaultLimit and openTasksMaxLimit bound one sweep. The read is a
// review set rather than a report, and a caller that asks for more than the
// ceiling is given the ceiling rather than refused — the truncation flag is
// what makes that honest.
const (
	openTasksDefaultLimit = 50
	openTasksMaxLimit     = 200
)

// ListOpenTasks answers one page of open promises under the caller's row
// scope, and whether the sweep stopped at its bound.
//
// The truncation flag is returned rather than a cursor. This read exists to
// be judged as a set — "are we behind on our promises" — and a caller handed
// a cursor would page a ranking whose head is the whole answer. Saying the
// bound was hit is what a reviewer needs; the next page is not.
func (s *Store) ListOpenTasks(ctx context.Context, in ListOpenTasksInput) ([]OpenTask, bool, error) {
	if err := auth.Require(ctx, "activity", principal.ActionRead); err != nil {
		return nil, false, err
	}
	var tasks []OpenTask
	var truncated bool
	err := s.tx(ctx, func(tx pgx.Tx) (err error) {
		tasks, truncated, err = listOpenTasks(ctx, tx, in)
		return err
	})
	return tasks, truncated, err
}

func listOpenTasks(ctx context.Context, tx pgx.Tx, in ListOpenTasksInput) ([]OpenTask, bool, error) {
	limit := openTasksLimit(in.Limit)
	args := []any{}
	arg := func(v any) int { args = append(args, v); return len(args) }
	where, err := openTasksFilter(ctx, in, arg)
	if err != nil {
		return nil, false, err
	}
	rows, err := tx.Query(ctx, `SELECT a.id, coalesce(a.subject, ''), a.due_at,
			a.assignee_id, coalesce(u.display_name, ''), a.created_at
		FROM activity a
		LEFT JOIN app_user u ON u.id = a.assignee_id
		WHERE `+joinAnd(where)+
		// NULLS LAST is the ascending default and is spelled anyway: an
		// undated promise sorts after every dated one, and that is a product
		// decision rather than a property of the index it happens to match.
		sprintf(` ORDER BY a.due_at ASC NULLS LAST, a.id ASC LIMIT %d`, limit+1), args...)
	if err != nil {
		return nil, false, err
	}
	// Collected rather than streamed: the "about" projection runs a second
	// query on this same transaction, which needs the cursor closed first.
	tasks, err := pgx.CollectRows(rows, scanOpenTask)
	if err != nil {
		return nil, false, err
	}
	truncated := len(tasks) > limit
	if truncated {
		tasks = tasks[:limit]
	}
	if err := attachTaskAbout(ctx, tx, tasks); err != nil {
		return nil, false, err
	}
	return tasks, truncated, nil
}

// openTasksFilter builds the predicate: the two columns that define an open
// promise, the caller's row scope, and whatever the input narrowed to.
func openTasksFilter(ctx context.Context, in ListOpenTasksInput, arg func(any) int) (where []string, err error) {
	where = []string{
		// The definition of the set, and the partial index's own predicate.
		sprintf("a.kind = $%d", arg(string(crmcontracts.ActivityKindTask))),
		"a.is_done = false",
		"a.archived_at IS NULL",
	}
	// The timeline's own scope rule: an activity carries no owner, so who may
	// read one is decided by the records it links to. A task is the most
	// personal row on that table — it names what a colleague undertook — so
	// it is scoped exactly as the timeline is, through auth rather than here.
	scope, err := auth.ActivityScopeClause(ctx, "a", arg)
	if err != nil {
		return nil, err
	}
	if scope != "" {
		where = append(where, scope)
	}
	if in.AssigneeID != nil {
		where = append(where, sprintf("a.assignee_id = $%d", arg(*in.AssigneeID)))
	}
	if in.EntityType != nil && in.EntityID != nil {
		column := linkColumn(*in.EntityType)
		if column == "" {
			return nil, &InvalidLinkTypeError{EntityType: *in.EntityType}
		}
		// EXISTS rather than a join: a task linked to both a project and its
		// deal must stay ONE row, or the bound above would count it twice and
		// the reviewer would read the same promise as two.
		where = append(where, sprintf(
			`EXISTS (SELECT 1 FROM activity_link l WHERE l.activity_id = a.id
				AND l.entity_type = $%d AND l.%s = $%d)`,
			arg(*in.EntityType), column, arg(*in.EntityID),
		))
	}
	return where, nil
}

// attachTaskAbout fills the records each promise is about, in ONE query for
// the whole page.
//
// A link whose target this caller may not read is DROPPED rather than
// projected as a bare id, which is the rule the timeline's own link
// projection keeps and for the same reason: "may I read this task" is an
// any-link question, and answering it yes does not license naming every
// other record the task touches. The rule has one spelling —
// auth.LinkTargetVisibleClause — and both readers ask it.
func attachTaskAbout(ctx context.Context, tx pgx.Tx, tasks []OpenTask) error {
	if len(tasks) == 0 {
		return nil
	}
	taskIDs := make([]ids.UUID, len(tasks))
	at := make(map[ids.UUID]int, len(tasks))
	for i, t := range tasks {
		taskIDs[i] = t.ID
		at[t.ID] = i
	}
	args := []any{taskIDs}
	arg := func(v any) int { args = append(args, v); return len(args) }
	visible, err := auth.LinkTargetVisibleClause(ctx, "al", arg)
	if err != nil {
		return err
	}
	if visible == "" {
		visible = scopeUnbounded
	}
	rows, err := tx.Query(ctx, `
		SELECT al.activity_id, al.entity_type, `+linkIDCoalesceQualified("al")+`,
			coalesce(`+linkNameCoalesce("al")+`, '')
		FROM activity_link al
		WHERE al.activity_id = ANY($1) AND `+visible+`
		ORDER BY al.activity_id, al.entity_type, al.id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var taskID ids.UUID
		var about TaskAbout
		if err := rows.Scan(&taskID, &about.EntityType, &about.EntityID, &about.Name); err != nil {
			return err
		}
		if i, ok := at[taskID]; ok {
			tasks[i].About = append(tasks[i].About, about)
		}
	}
	return rows.Err()
}

func scanOpenTask(row pgx.CollectableRow) (OpenTask, error) {
	var t OpenTask
	err := row.Scan(&t.ID, &t.Subject, &t.DueAt, &t.AssigneeID, &t.AssigneeName, &t.CreatedAt)
	return t, err
}

// openTasksLimit resolves one sweep's bound: the caller's ask when it is
// within the ceiling, the default when they named none, the ceiling when
// they asked for more than this read serves.
func openTasksLimit(asked int) int {
	switch {
	case asked <= 0:
		return openTasksDefaultLimit
	case asked > openTasksMaxLimit:
		return openTasksMaxLimit
	default:
		return asked
	}
}

// joinAnd renders the WHERE terms. The terms are always non-empty here — the
// two that define the set are unconditional — so there is no empty-predicate
// case to invent a TRUE for.
func joinAnd(terms []string) string {
	out := terms[0]
	for _, term := range terms[1:] {
		out += " AND " + term
	}
	return out
}
