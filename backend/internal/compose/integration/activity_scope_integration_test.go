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

	assertOwnTeamActivityStillMutable(rep, t, e, myPerson)
}

// TestRelinkActivityBumpsVersion pins F-001's fix: a relink that actually
// changes who the activity reaches must move activity.version, the same as
// UpdateActivity and ArchiveActivity already do. A staged approval pins this
// version (versionTables includes objectActivity in the approvals module),
// and that pin is the only thing standing between an approved "send this
// body on this conversation" and the conversation being silently repointed
// to someone else before the approval is redeemed — before this fix, relink
// only touched activity_link, so the pin never went stale and a relinked
// conversation's approval kept redeeming as if nothing had changed.
func TestRelinkActivityBumpsVersion(t *testing.T) {
	e := Setup(t)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, activityLifecyclePerms)
	first := e.SeedPerson(t, "First Contact", &e.Rep1)
	second := e.SeedPerson(t, "Second Contact", &e.Rep1)

	logged, _, err := e.Activities.LogActivity(rep, activities.LogActivityInput{
		Kind: "note", Subject: strPtr("Conversation"), Source: "manual",
		Links: []activities.ActivityLinkInput{{EntityType: "person", EntityID: first}},
	})
	if err != nil {
		t.Fatalf("seeding the activity: %v", err)
	}
	if logged.Version == nil {
		t.Fatal("a freshly logged activity carries no version — the versionTables pin this test defends would never bind")
	}
	before := *logged.Version
	id := ids.From[ids.ActivityKind](ids.UUID(logged.Id))

	relinked, err := e.Activities.RelinkActivity(rep, id, activities.RelinkActivityInput{
		EntityType: "person", EntityID: second, ReplaceExistingOfType: true,
	})
	if err != nil {
		t.Fatalf("relink: %v", err)
	}
	if relinked.Version == nil || *relinked.Version <= before {
		t.Fatalf("version after relink = %v, want strictly greater than the pre-relink version %d — "+
			"a relink that changes the conversation's counterparty must invalidate any approval pinned to the old version",
			relinked.Version, before)
	}

	// A no-op relink (same entity, no replace) touches nothing and must not
	// burn a version for a caller who changed nothing.
	noop, err := e.Activities.RelinkActivity(rep, id, activities.RelinkActivityInput{
		EntityType: "person", EntityID: second,
	})
	if err != nil {
		t.Fatalf("no-op relink: %v", err)
	}
	if noop.Version == nil || *noop.Version != *relinked.Version {
		t.Fatalf("version after a no-op relink = %v, want unchanged from %d", noop.Version, *relinked.Version)
	}
}

// assertOwnTeamActivityStillMutable is the positive control: the same
// three mutators keep working on an activity the caller's row scope does
// reach.
func assertOwnTeamActivityStillMutable(rep context.Context, t *testing.T, e *Env, myPerson ids.UUID) {
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
