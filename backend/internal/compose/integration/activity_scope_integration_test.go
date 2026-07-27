// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Anything that returns a record is a read, and an activity's row scope
// is the link-walk — not RLS, which binds only the workspace, and not an
// owner column, which the table does not have. So the three lifecycle
// mutators are reads too: update, archive and relink each hand back the
// full row (subject and body included) and relink additionally
// TRANSPLANTS the activity into the caller's own timeline. Each assertion
// here pins one of those against another team's activity; the positive
// controls prove the gate narrows scope rather than breaking the feature.

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// activityLifecyclePerms is a bounded rep who holds every object grant
// the three mutators ask for. The flaw is inverted — an unbounded admin
// short-circuits the scope clause — so the fixture must be team-scoped.
var activityLifecyclePerms = principal.Permissions{
	RoleKeys: []string{"rep"},
	Objects: map[string]principal.ObjectGrant{
		"person":   {Create: true, Read: true, Update: true},
		"activity": {Create: true, Read: true, Update: true, Delete: true},
	},
	RowScope: principal.RowScopeTeam,
}

func TestActivityLifecycleMutatorsHonorRowScope(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)

	// Team2's activity: linked to a person Rep3 owns, so Rep1's team scope
	// hides it.
	theirPerson := e.SeedPerson(t, "Their Contact", &e.Rep3)
	theirActivity := SeedRow(t, owner, `INSERT INTO activity (id, workspace_id, kind, subject, body, occurred_at, source, captured_by)
		VALUES ($1, $2, 'email', 'Q3 renewal terms', 'confidential body', now(), 'manual', 'human:x')`, e.WS)
	LinkActivity(t, owner, e.WS, theirActivity, "person", theirPerson)
	theirID := ids.From[ids.ActivityKind](theirActivity)

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, activityLifecyclePerms)
	myPerson := e.SeedPerson(t, "My Contact", &e.Rep1)

	// The control: the plain read already refuses.
	if _, err := e.Activities.GetActivity(rep, theirID, storekit.LiveOnly); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("GetActivity out of row scope → %v, want ErrNotFound", err)
	}
	// A no-op patch coalesces every column, so it changes nothing and still
	// returns the row: the disclosure is the response, not the write.
	if _, err := e.Activities.UpdateActivity(rep, theirID, activities.UpdateActivityInput{}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("UpdateActivity out of row scope → %v, want ErrNotFound", err)
	}
	// Relink is the persistent scope hijack: it would move another team's
	// email onto a record the caller owns, and with ReplaceExistingOfType
	// delete the victim's own link on the way.
	if _, err := e.Activities.RelinkActivity(rep, theirID, activities.RelinkActivityInput{
		EntityType: "person", EntityID: myPerson, ReplaceExistingOfType: true,
	}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("RelinkActivity out of row scope → %v, want ErrNotFound", err)
	}
	if _, err := e.Activities.ArchiveActivity(rep, theirID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("ArchiveActivity out of row scope → %v, want ErrNotFound", err)
	}

	// The victim's link survived every refusal.
	if links := e.WsCount(t, `SELECT count(*) FROM activity_link WHERE activity_id = $1 AND person_id = $2`,
		theirActivity, theirPerson); links != 1 {
		t.Errorf("victim link rows = %d, want 1 — a refused relink must not delete it", links)
	}

	assertOwnTeamActivityStillMutable(t, e, rep, myPerson)
}

// assertOwnTeamActivityStillMutable is the positive control: the same
// three mutators keep working on an activity the caller's row scope does
// reach.
func assertOwnTeamActivityStillMutable(t *testing.T, e *Env, rep context.Context, myPerson ids.UUID) {
	t.Helper()
	mine, _, err := e.Activities.LogActivity(rep, activities.LogActivityInput{
		Kind: "note", Subject: strPtr("Mine"), Source: "manual",
		Links: []activities.ActivityLinkInput{{EntityType: "person", EntityID: myPerson}},
	})
	if err != nil {
		t.Fatalf("seeding an in-scope activity: %v", err)
	}
	mineID := ids.From[ids.ActivityKind](ids.UUID(mine.Id))

	if _, err := e.Activities.UpdateActivity(rep, mineID, activities.UpdateActivityInput{Subject: strPtr("Mine, edited")}); err != nil {
		t.Errorf("UpdateActivity in row scope → %v, want ok", err)
	}
	if _, err := e.Activities.RelinkActivity(rep, mineID, activities.RelinkActivityInput{
		EntityType: "person", EntityID: myPerson,
	}); err != nil {
		t.Errorf("RelinkActivity in row scope → %v, want ok", err)
	}
	if _, err := e.Activities.ArchiveActivity(rep, mineID); err != nil {
		t.Errorf("ArchiveActivity in row scope → %v, want ok", err)
	}
}
