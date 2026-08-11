// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package identity

// The role grant editor over a real migrated Postgres. Four things can only be
// proved here: that the targeted jsonb write leaves the rest of the document
// alone, that the grant it stores is the one the authorization path then reads,
// that the audit row lands, and that each refusal refuses.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// extObject is an extension-shaped object registered for the duration of one
// test. It is the case the whole surface exists for: a name that is NOT in
// policy.coreObjects and is grantable only because a composed unit registered
// it at boot.
const extObject = "ext_rolesit_note"

// registerExtObject widens the vocabulary for one test and clears it after, so
// a registration never leaks into the next test's refusals.
func registerExtObject(t *testing.T) {
	t.Helper()
	if err := RegisterRbacObjects(extObject); err != nil {
		t.Fatalf("registering %s: %v", extObject, err)
	}
	t.Cleanup(ResetRbacObjectsForTest)
}

// The headline: an extension object is grantable, the grant lands in the stored
// document under the keys policy.Parse reads, and EffectiveRBAC — the merge the
// authorization gate consults — then reports it for a member holding the role.
// Anything less than the full chain would pass while the screen still said "you
// do not hold access".
func TestGrantingAnExtensionObjectReachesTheMemberEffectivePermissions(t *testing.T) {
	e := setupRevocationEnv(t, "role-grant-ext")
	registerExtObject(t)
	ctx := e.wsCtx(e.admin)

	// The member holds `rep`, so the grant has to land on the role they hold.
	if err := e.svc.ChangeUserRole(ctx, e.admin, e.member.UserID, "rep"); err != nil {
		t.Fatalf("assigning rep: %v", err)
	}
	before, err := e.svc.EffectiveRBAC(ctx, e.admin.WorkspaceID.UUID, e.member.UserID.UUID)
	if err != nil {
		t.Fatalf("effective rbac before: %v", err)
	}
	if before.Permissions.Objects[extObject].Read {
		t.Fatal("the member already holds read on the extension object; the grant below would prove nothing")
	}

	updated, err := e.svc.SetRoleObjectGrant(ctx, e.admin, "rep", extObject,
		storedGrant{Read: true, Create: true}, nil)
	if err != nil {
		t.Fatalf("SetRoleObjectGrant: %v", err)
	}
	if got := updated.Objects[extObject]; !got.Read || !got.Create || got.Update || got.Delete {
		t.Errorf("the returned role reports %+v, want read+create only", got)
	}

	after, err := e.svc.EffectiveRBAC(ctx, e.admin.WorkspaceID.UUID, e.member.UserID.UUID)
	if err != nil {
		t.Fatalf("effective rbac after: %v", err)
	}
	got := after.Permissions.Objects[extObject]
	if !got.Read || !got.Create || got.Update || got.Delete {
		t.Fatalf("the member's effective grant on %s is %+v, want read+create — "+
			"the write stored keys the merge does not read", extObject, got)
	}
}

// The targeted write must not disturb the rest of the document. A
// parse-and-rewrite would pass every assertion about the edited object and
// silently drop the row_scope and every grant this installation cannot name.
func TestSettingOneGrantLeavesTheRestOfTheDocumentIntact(t *testing.T) {
	e := setupRevocationEnv(t, "role-grant-intact")
	registerExtObject(t)
	ctx := e.wsCtx(e.admin)

	// A grant on an object nothing knows about, planted the way a removed unit
	// would leave one behind.
	if _, err := e.owner.Exec(context.Background(),
		`UPDATE role SET permissions = jsonb_set(permissions, '{objects,ext_gone_thing}', '{"read":true}'::jsonb, true)
		  WHERE workspace_id = $1 AND key = 'rep'`, e.admin.WorkspaceID); err != nil {
		t.Fatalf("planting the orphaned grant: %v", err)
	}

	if _, err := e.svc.SetRoleObjectGrant(ctx, e.admin, "rep", extObject, storedGrant{Read: true}, nil); err != nil {
		t.Fatalf("SetRoleObjectGrant: %v", err)
	}

	var raw []byte
	if err := e.owner.QueryRow(context.Background(),
		`SELECT permissions FROM role WHERE workspace_id = $1 AND key = 'rep'`,
		e.admin.WorkspaceID).Scan(&raw); err != nil {
		t.Fatalf("reading the document back: %v", err)
	}
	var doc struct {
		RowScope string                 `json:"row_scope"`
		Objects  map[string]storedGrant `json:"objects"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// row_scope is not this endpoint's to touch and is not modelled by the
	// grant map; a rewrite would have dropped it and silently narrowed the role.
	if doc.RowScope != "team" {
		t.Errorf("row_scope = %q, want the seeded rep scope 'team' — the write rewrote the document", doc.RowScope)
	}
	if !doc.Objects["ext_gone_thing"].Read {
		t.Error("the orphaned grant was erased; the write is not targeted")
	}
	// And the seeded core grants are still there.
	if !doc.Objects["deal"].Read {
		t.Error("the seeded deal grant was erased")
	}
	if !doc.Objects[extObject].Read {
		t.Errorf("the object being edited did not land: %v", doc.Objects[extObject])
	}
}

// A permission change with no audit trail is the failure mode this feature is
// most dangerous without: the row names the actor, the role, and both images of
// the one object that moved.
func TestSettingAGrantWritesAnAuditRowNamingTheActorAndBothImages(t *testing.T) {
	e := setupRevocationEnv(t, "role-grant-audit")
	registerExtObject(t)
	ctx := e.wsCtx(e.admin)

	if _, err := e.svc.SetRoleObjectGrant(ctx, e.admin, "rep", extObject, storedGrant{Read: true}, nil); err != nil {
		t.Fatalf("first grant: %v", err)
	}
	// A second write so the BEFORE image is a real prior grant rather than the
	// zero value, which is the only version that proves before is read at all.
	if _, err := e.svc.SetRoleObjectGrant(ctx, e.admin, "rep", extObject,
		storedGrant{Read: true, Update: true}, nil); err != nil {
		t.Fatalf("second grant: %v", err)
	}

	var actorID string
	var actorType string
	var before, after []byte
	var roleID ids.UUID
	if err := e.owner.QueryRow(context.Background(),
		`SELECT actor_type, actor_id, entity_id, before, after FROM audit_log
		  WHERE workspace_id = $1 AND entity_type = 'role' AND action = 'update'
		  ORDER BY occurred_at DESC, id DESC LIMIT 1`,
		e.admin.WorkspaceID).Scan(&actorType, &actorID, &roleID, &before, &after); err != nil {
		t.Fatalf("reading the audit row: %v", err)
	}
	if actorType != "human" || actorID != "human:"+e.admin.UserID.String() {
		t.Errorf("audit actor = %s/%s, want the acting admin", actorType, actorID)
	}
	var beforeDoc, afterDoc struct {
		Objects map[string]storedGrant `json:"objects"`
	}
	if err := json.Unmarshal(before, &beforeDoc); err != nil {
		t.Fatalf("before image: %v", err)
	}
	if err := json.Unmarshal(after, &afterDoc); err != nil {
		t.Fatalf("after image: %v", err)
	}
	if !beforeDoc.Objects[extObject].Read || beforeDoc.Objects[extObject].Update {
		t.Errorf("before image = %+v, want the prior read-only grant", beforeDoc.Objects[extObject])
	}
	if !afterDoc.Objects[extObject].Update {
		t.Errorf("after image = %+v, want the new grant", afterDoc.Objects[extObject])
	}
	// Only the edited object rides the images: a whole-document image would
	// bury a one-verb change under thirty unchanged ones.
	if len(afterDoc.Objects) != 1 {
		t.Errorf("after image carries %d objects, want only the edited one", len(afterDoc.Objects))
	}
}

// The read: every seeded role, sorted, with is_system set and the grant map
// populated. Admin-only, and a non-admin is refused before any row is read.
func TestListRolesReturnsEverySeededRoleAndRefusesANonAdmin(t *testing.T) {
	e := setupRevocationEnv(t, "role-list")
	ctx := e.wsCtx(e.admin)

	rows, err := e.svc.ListRoles(ctx, e.admin)
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	byKey := map[string]roleRow{}
	for _, r := range rows {
		byKey[r.Key] = r
	}
	for _, key := range []string{"admin", "manager", "ops", "read_only", "rep"} {
		row, ok := byKey[key]
		if !ok {
			t.Errorf("seeded role %q is missing from the directory", key)
			continue
		}
		if !row.IsSystem {
			t.Errorf("role %q reports is_system=false; the bootstrap seeds it", key)
		}
		if len(row.Objects) == 0 {
			t.Errorf("role %q came back with no grants; the seeded document lists every core object", key)
		}
	}
	// Sorted by key so a re-render never reshuffles the editor.
	for i := 1; i < len(rows); i++ {
		if rows[i-1].Key > rows[i].Key {
			t.Fatalf("roles are not sorted by key: %q before %q", rows[i-1].Key, rows[i].Key)
		}
	}

	// e.member holds no admin role: the refusal is the caller's standing, taken
	// before the query runs.
	if _, err := e.svc.ListRoles(e.wsCtx(e.member), e.member); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("a non-admin read the role directory: err = %v", err)
	}
}

// The three refusals, each proving nothing was written.
func TestSetRoleObjectGrantRefusesANonAdminAnUnknownRoleAndAnUnknownObject(t *testing.T) {
	e := setupRevocationEnv(t, "role-grant-refusals")
	registerExtObject(t)

	// 1. A non-admin. Checked before the vocabulary, so a member cannot even
	// probe which objects exist by watching which refusal comes back.
	_, err := e.svc.SetRoleObjectGrant(e.wsCtx(e.member), e.member, "rep", extObject, storedGrant{Read: true}, nil)
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("a non-admin set a grant: err = %v", err)
	}

	ctx := e.wsCtx(e.admin)
	// 2. An object outside the vocabulary. This is the refusal policy.Parse's
	// own comment asked this surface to take: a typo stored cleanly would grant
	// nothing forever and read as a bug in the role.
	if _, err := e.svc.SetRoleObjectGrant(ctx, e.admin, "rep", "ext_notes_nte", storedGrant{Read: true}, nil); !errorIs(err, errUnknownObject) {
		t.Errorf("a mistyped object was accepted: err = %v", err)
	}
	// A core object's typo is refused by the same check, not only ext_ names.
	if _, err := e.svc.SetRoleObjectGrant(ctx, e.admin, "rep", "dealz", storedGrant{Read: true}, nil); !errorIs(err, errUnknownObject) {
		t.Errorf("a mistyped core object was accepted: err = %v", err)
	}

	// 3. An unknown role, refused distinctly from an unknown object.
	if _, err := e.svc.SetRoleObjectGrant(ctx, e.admin, "reps", extObject, storedGrant{Read: true}, nil); !errorIs(err, errUnknownRole) {
		t.Errorf("an unknown role was accepted: err = %v", err)
	}

	// Nothing was written by any of the four attempts.
	var count int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE workspace_id = $1 AND entity_type = 'role'`,
		e.admin.WorkspaceID).Scan(&count); err != nil {
		t.Fatalf("counting audit rows: %v", err)
	}
	if count != 0 {
		t.Errorf("%d role audit rows exist after four refusals; a refusal wrote", count)
	}
}

// A SYSTEM role is editable, and that is the whole point: a fresh installation
// has five seeded roles and no custom one, so refusing them would mean no role
// could ever hold an extension grant. Nothing in this codebase treats a seeded
// document as immutable — the RBAC backfill migrations write into these very
// rows, guarding each statement so an operator's edit survives.
func TestASystemRoleGrantIsEditable(t *testing.T) {
	e := setupRevocationEnv(t, "role-grant-system")
	registerExtObject(t)
	ctx := e.wsCtx(e.admin)

	updated, err := e.svc.SetRoleObjectGrant(ctx, e.admin, "admin", extObject,
		storedGrant{Create: true, Read: true, Update: true, Delete: true}, nil)
	if err != nil {
		t.Fatalf("editing a system role: %v", err)
	}
	if !updated.IsSystem {
		t.Fatal("the admin role reports is_system=false; this test proves nothing about system roles")
	}
	if got := updated.Objects[extObject]; !got.Create || !got.Read || !got.Update || !got.Delete {
		t.Errorf("grant = %+v, want full CRUD", got)
	}

	// Revocation is the same call with all four false — the supported way to
	// take a grant back, and it must leave an explicit all-false entry rather
	// than deleting the key, so the audit trail shows what the role now holds.
	revoked, err := e.svc.SetRoleObjectGrant(ctx, e.admin, "admin", extObject, storedGrant{}, nil)
	if err != nil {
		t.Fatalf("revoking: %v", err)
	}
	got, present := revoked.Objects[extObject]
	if !present {
		t.Fatal("revoking removed the key entirely; an explicit zero grant is the honest record")
	}
	if got.Create || got.Read || got.Update || got.Delete {
		t.Errorf("grant after revoke = %+v, want all false", got)
	}
}

// The lost-write this surface would otherwise have. Two admins hold reads of
// the same role; the first revokes a verb, the second — from the stale read —
// grants an unrelated one. The document is ONE jsonb value, so without the
// version check the second write would carry a whole document that still says
// the revoked verb is granted, and the revoke would vanish with nothing to say
// it ever happened.
func TestASecondAdminWritingFromAStaleReadIsRefusedRatherThanClobbering(t *testing.T) {
	e := setupRevocationEnv(t, "role-grant-skew")
	registerExtObject(t)
	ctx := e.wsCtx(e.admin)

	// Both admins load the screen.
	roles, err := e.svc.ListRoles(ctx, e.admin)
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	var stale int64
	for _, r := range roles {
		if r.Key == "rep" {
			stale = r.Version
		}
	}
	if stale == 0 {
		t.Fatal("the rep role reported version 0; every row starts at 1 and the guard would be vacuous")
	}

	// The first admin writes. The trigger bumps the version.
	fresh, err := e.svc.SetRoleObjectGrant(ctx, e.admin, "rep", extObject, storedGrant{Read: true}, &stale)
	if err != nil {
		t.Fatalf("the first write, with a current version: %v", err)
	}
	if fresh.Version <= stale {
		t.Fatalf("version did not advance: %d then %d — the bump trigger is not attached, "+
			"so every If-Match would pass forever", stale, fresh.Version)
	}

	// The second admin writes from the read they took BEFORE that.
	_, err = e.svc.SetRoleObjectGrant(ctx, e.admin, "rep", "deal", storedGrant{Read: true}, &stale)
	if !errors.Is(err, apperrors.ErrVersionSkew) {
		t.Fatalf("a write from a stale read was accepted: err = %v", err)
	}

	// And it wrote nothing: the object the stale write named is untouched, and
	// the first admin's grant survives.
	after, err := e.svc.ListRoles(ctx, e.admin)
	if err != nil {
		t.Fatalf("ListRoles after: %v", err)
	}
	for _, r := range after {
		if r.Key != "rep" {
			continue
		}
		if r.Version != fresh.Version {
			t.Errorf("version moved to %d after a refused write; the refusal wrote", r.Version)
		}
		if !r.Objects[extObject].Read {
			t.Error("the first admin's grant was lost")
		}
	}

	// Omitting If-Match is still last-write-wins, exactly as the contract's own
	// IfMatch parameter documents for every versioned write here. Asserted so
	// the behaviour is a decision on record rather than something a reader has
	// to infer from the absence of a test.
	if _, err := e.svc.SetRoleObjectGrant(ctx, e.admin, "rep", "deal", storedGrant{Read: true}, nil); err != nil {
		t.Errorf("a write with no If-Match was refused: %v", err)
	}
}

// errorIs is errors.Is under this file's import set; the sentinels wrap
// apperrors.ErrNotFound, so a bare taxonomy match would not tell the two apart.
func errorIs(err, target error) bool {
	for e := err; e != nil; {
		if e == target { //nolint:errorlint // sentinel identity is exactly what is being asserted
			return true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}
