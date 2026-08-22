// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Mass mis-attribution has a remedy: a whole conversation or a named set of
// activities moves onto one record in one transaction, through the same
// guarded write the single relink performs per row. These tests drive the
// store's real batch doors and read back what the write shape must leave: an
// audit row per moved activity, an event per moved activity, and — for a
// project destination — the retention evidence each one earns.

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// threadFixture is one conversation of three mails, two of them Rep1's own
// and one on another team's capture-private contact, plus a project to file
// the conversation under.
type threadFixture struct {
	key     string
	mine    [2]ids.UUID
	theirs  ids.UUID
	project ids.UUID
}

func seedThreadFixture(t *testing.T, e *Env, owner *pgx.Conn) threadFixture {
	t.Helper()
	f := threadFixture{key: "thread:" + ids.NewV7().String()}
	org := e.SeedOrg(t, "Acme GmbH", nil)
	f.project = ids.NewV7()
	e.WsExec(t, `INSERT INTO project (id, name, key, organization_id, phase, source, captured_by)
		VALUES ($1, 'ERP rollout', 'ERP27', $2, 'delivering', 'manual', 'human:x')`, f.project, org)

	myPerson := e.SeedPerson(t, "My Contact", &e.Rep1)
	for i := range f.mine {
		f.mine[i] = SeedIDRow(t, owner, `INSERT INTO activity (id, kind, subject, body, occurred_at, source, captured_by, thread_key)
			VALUES ($1, 'email', 'Re: milestone', 'body', now(), 'manual', 'human:x', $2)`, f.key)
		LinkActivity(t, owner, f.mine[i], "person", myPerson)
	}
	theirPerson := e.SeedPerson(t, "Their Contact", &e.Rep3)
	e.MakeCapturePrivate(t, "person", theirPerson, e.Rep3)
	f.theirs = SeedIDRow(t, owner, `INSERT INTO activity (id, kind, subject, body, occurred_at, source, captured_by, thread_key)
		VALUES ($1, 'email', 'Re: milestone', 'body', now(), 'manual', 'human:x', $2)`, f.key)
	LinkActivity(t, owner, f.theirs, "person", theirPerson)
	return f
}

func projectLinks(t *testing.T, e *Env, activity, project ids.UUID) int {
	t.Helper()
	return e.WsCount(t, `SELECT count(*) FROM activity_link WHERE activity_id = $1 AND project_id = $2`, activity, project)
}

// Moving a thread onto a project moves every member the caller may write, and
// each one commits with its own audit row, its own activity.updated event and
// its own project_linked evidence. The member on another team's private
// contact stays where it is, and the count says so.
func TestRelinkingAThreadMovesEveryWritableMemberWithItsOwnWriteShape(t *testing.T) {
	e := Setup(t)
	f := seedThreadFixture(t, e, OwnerConn(t))
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, activityLifecyclePerms)

	out, err := e.Activities.RelinkThread(rep, f.key, activities.RelinkActivityInput{EntityType: "project", EntityID: f.project})
	if err != nil {
		t.Fatalf("relinking the thread: %v", err)
	}
	if out.Relinked != 2 {
		t.Fatalf("relinked = %d, want exactly Rep1's two mails", out.Relinked)
	}
	for _, id := range f.mine {
		if projectLinks(t, e, id, f.project) != 1 {
			t.Errorf("activity %s is not filed under the project", id)
		}
		if n := e.WsCount(t, `SELECT count(*) FROM audit_log WHERE action = 'activity_relink' AND entity_id = $1`, id); n != 1 {
			t.Errorf("audit rows for %s = %d, want 1 — every moved row is its own audited act", id, n)
		}
		if n := e.WsCount(t, `SELECT count(*) FROM event_outbox WHERE envelope->>'type' = 'activity.updated' AND envelope->'entity'->>'id' = $1::text`, id); n != 1 {
			t.Errorf("activity.updated events for %s = %d, want 1", id, n)
		}
		if got := readProjectStamp(t, e, id); got.evidence != 1 || got.class == nil {
			t.Errorf("activity %s: project_linked evidence = %d, class = %v — the stamp must commit with the link", id, got.evidence, got.class)
		}
	}
	if projectLinks(t, e, f.theirs, f.project) != 0 {
		t.Error("another team's mail moved with the thread — the per-row write check was skipped")
	}
	if got := readProjectStamp(t, e, f.theirs); got.class != nil {
		t.Error("another team's mail was stamped without being moved")
	}

	// Replaying the move is a no-op per row: nothing new is counted and no
	// second audit row appears.
	again, err := e.Activities.RelinkThread(rep, f.key, activities.RelinkActivityInput{EntityType: "project", EntityID: f.project})
	if err != nil {
		t.Fatalf("replaying the thread move: %v", err)
	}
	if again.Relinked != 0 {
		t.Errorf("replay relinked %d, want nothing", again.Relinked)
	}
}

// A named set is all or nothing. One id outside the caller's sight answers the
// existence-hiding 404 the single relink answers, and the ids beside it —
// already written inside the same transaction — are rolled back.
func TestRelinkingANamedSetWithAnInvisibleIdMovesNothing(t *testing.T) {
	e := Setup(t)
	f := seedThreadFixture(t, e, OwnerConn(t))
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, activityLifecyclePerms)

	_, err := e.Activities.RelinkActivities(rep, []ids.UUID{f.mine[0], f.mine[1], f.theirs},
		activities.RelinkActivityInput{EntityType: "project", EntityID: f.project})
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("bulk relink naming an invisible id → %v, want ErrNotFound", err)
	}
	for _, id := range []ids.UUID{f.mine[0], f.mine[1], f.theirs} {
		if projectLinks(t, e, id, f.project) != 0 {
			t.Errorf("activity %s moved although the set was refused — the transaction did not roll back", id)
		}
	}
	if n := e.WsCount(t, `SELECT count(*) FROM audit_log WHERE action = 'activity_relink'`); n != 0 {
		t.Errorf("audit rows after a refused set = %d, want 0", n)
	}

	// The same set without the foreign id moves, and each row is audited.
	out, err := e.Activities.RelinkActivities(rep, []ids.UUID{f.mine[0], f.mine[1], f.mine[0]},
		activities.RelinkActivityInput{EntityType: "project", EntityID: f.project})
	if err != nil {
		t.Fatalf("bulk relink of the caller's own mails: %v", err)
	}
	if out.Relinked != 2 {
		t.Errorf("relinked = %d, want the two distinct ids once each", out.Relinked)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM audit_log WHERE action = 'activity_relink'`); n != 2 {
		t.Errorf("audit rows after the set moved = %d, want 2", n)
	}
}

// A blank thread key and an oversized set are refused before any row is read.
func TestBatchRelinkRefusesAnEmptySelection(t *testing.T) {
	e := Setup(t)
	f := seedThreadFixture(t, e, OwnerConn(t))
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, activityLifecyclePerms)
	in := activities.RelinkActivityInput{EntityType: "project", EntityID: f.project}

	var detailed *httperr.DetailedError
	if _, err := e.Activities.RelinkThread(rep, "", in); !errors.As(err, &detailed) || detailed.Status != http.StatusUnprocessableEntity {
		t.Errorf("blank thread_key → %v, want a 422 naming the field", err)
	}
	if _, err := e.Activities.RelinkActivities(rep, nil, in); !errors.As(err, &detailed) || detailed.Status != http.StatusUnprocessableEntity {
		t.Errorf("empty activity_ids → %v, want a 422 naming the field", err)
	}
	if _, err := e.Activities.RelinkActivities(context.Background(), []ids.UUID{f.mine[0]}, in); err == nil {
		t.Error("a context with no principal relinked a set")
	}
}
