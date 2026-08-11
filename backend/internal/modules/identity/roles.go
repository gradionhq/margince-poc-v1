// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The role grant editor (/roles, /roles/{key}/objects/{object}).
//
// Until this landed, `role.permissions` was writable by hand-written SQL and by
// nothing else. That was survivable while the vocabulary was the compiled-in
// core set — every workspace is seeded with grants on all of it — and stopped
// being survivable the moment an extension unit could register its own object
// (compose/extrbac.go). A unit's objects are registered at boot and granted by
// nobody, so the unit ships enabled and every member is refused by its own
// gate: the reference extension's screen rendering "you do not hold access to
// this extension's notes" for the installation's own admin was this gap, not a
// bug in the unit.
//
// policy.Parse's own comment named this surface as the place the strictness it
// gave up on the READ path has to be taken: an unknown object in a STORED
// document is dropped and logged, because refusing it would cost every holder
// their session, while an unknown object in a WRITE costs an admin one
// correction. setRoleObjectGrant is that write, and it refuses.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/identity/internal/policy"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// errUnknownObject is the write-path refusal for an object outside the
// grantable vocabulary. It wraps ErrNotFound so it renders 404 like
// errUnknownRole; the handler separates the two by problem `code`, because an
// admin who mistyped a role and one who mistyped an object need to look in
// different places.
var errUnknownObject = fmt.Errorf("%w: no RBAC object with this name is defined", apperrors.ErrNotFound)

// storedGrant is one object's CRUD as `role.permissions` SPELLS it — lower-case
// json keys, all four verbs always present.
//
// It is not principal.ObjectGrant, which is the same four booleans without the
// tags: that type models a MERGED effective grant and is never serialized, so
// marshalling it here would have written `{"Create":true,…}` into the document
// and every later read — policy.Parse's included — would have decoded four
// falses. A grant that silently evaluates to no access is the one failure this
// whole surface exists to remove, so the document shape gets its own type
// rather than borrowing a runtime one that happens to have the same fields.
type storedGrant struct {
	Create bool `json:"create"`
	Read   bool `json:"read"`
	Update bool `json:"update"`
	Delete bool `json:"delete"`
}

// roleRow is one role as the editor reads it. Deliberately NOT the whole `role`
// table: id, workspace_id and the timestamps decide nothing on this surface,
// and row_scope is not editable here (see the contract's deferred-stubs note),
// so carrying them would publish fields no caller can act on.
type roleRow struct {
	Key      string
	Name     string
	IsSystem bool
	// Version is the optimistic-concurrency version (data-model §1.3a) the
	// client echoes in If-Match. The whole document is ONE jsonb value, so two
	// admins editing two DIFFERENT objects is a lost write rather than a merge
	// — which is why the row carries a version at all (migration 0211).
	Version int64
	// Objects is the stored document's grant map, VERBATIM — including any
	// object this installation cannot name. See ListRoles for why.
	Objects map[string]storedGrant
}

// ListRoles returns every role the workspace defines, ordered by key so a
// re-render never reshuffles the editor. Admin-only: this is the same authority
// ChangeUserRole carries, and an admin is the only caller who can act on it.
//
// The grant map is decoded straight off the jsonb rather than through
// policy.Parse, and that difference is load-bearing. Parse DROPS a grant on an
// object the installation does not know (a removed unit, a typo predating this
// endpoint) so that stale data can never authorize anything. Here the caller is
// an operator looking at the document in order to fix it — hiding the entry
// would leave them with no way to see, let alone clear, a grant that is still
// stored. Nothing on this path feeds an authorization decision, so showing it
// grants nothing.
func (s *Service) ListRoles(ctx context.Context, actor Identity) ([]roleRow, error) {
	if !actor.hasRole(roleAdmin) {
		return nil, apperrors.ErrPermissionDenied
	}
	ctx = actorCtx(ctx, actor)
	var out []roleRow
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT key, name, is_system, version, permissions
			   FROM role WHERE archived_at IS NULL ORDER BY key`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			row, err := scanRoleRow(rows)
			if err != nil {
				return err
			}
			out = append(out, row)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SetRoleObjectGrant replaces one role's CRUD on one object and returns the
// role as stored afterwards. Admin-only.
//
// Two refusals precede any write, in this order: the caller must be an admin,
// and the object must be one a principal could ever hold — a core object or one
// a composed extension registered at boot. The vocabulary check is the whole
// reason this method is not a two-line UPDATE: a typo'd object stores cleanly,
// grants nothing, is dropped on every subsequent read, and presents to the
// operator as "I granted it and it did not work".
//
// A system role is editable, deliberately. Nothing in this codebase treats a
// seeded role's document as immutable — the RBAC backfill migrations write into
// exactly those rows, and each guards its statement with
// `NOT permissions->'objects' ? '<name>'` so that an operator's own edit is
// preserved rather than clobbered. That guard only makes sense if operator
// edits to system roles were always the expectation. Refusing them here would
// also make the feature useless: a fresh installation has five system roles and
// nothing else, so no role could ever hold an extension grant.
func (s *Service) SetRoleObjectGrant(ctx context.Context, actor Identity, roleKey, object string, grant storedGrant, ifVersion *int64) (roleRow, error) {
	if !actor.hasRole(roleAdmin) {
		return roleRow{}, apperrors.ErrPermissionDenied
	}
	if !policy.IsGrantableObject(object) {
		return roleRow{}, errUnknownObject
	}
	ctx = actorCtx(ctx, actor)
	var updated roleRow
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		updated, err = applyRoleObjectGrant(ctx, tx, roleKey, object, grant, ifVersion)
		return err
	})
	if err != nil {
		return roleRow{}, err
	}
	return updated, nil
}

// applyRoleObjectGrant is the transactional body: lock the role, check the
// caller's If-Match against it, write the one object's grant, audit both images.
//
// TWO concurrency controls, and they answer different questions. The FOR UPDATE
// lock serializes concurrent writers so the read-modify-write cannot interleave
// — that is what protects the write even when no If-Match was sent, and it is
// the same posture storekit's ApplyGuarded takes for a caller who omits the
// header. The version check protects against a STALE READ, which no lock can:
// an admin who loaded the screen a minute ago and grants `create` would
// otherwise re-send a document that silently un-does another admin's revoke of
// `delete`, because the document is one jsonb value and every write carries all
// of it.
//
// No event is emitted, and that is a considered omission rather than an
// oversight: the closed public catalog (events.md §5) offers `role.changed`,
// whose payload is a MEMBER's assignment move (`{user_id, from_role, to_role}`)
// and cannot express "this role's grants moved" for the unknown set of members
// holding it. Inventing a verb build-side is forbidden. The audit row carries
// the whole fact — actor, role, object, before and after — which is what a
// permission-change investigation reads.
func applyRoleObjectGrant(ctx context.Context, tx pgx.Tx, roleKey, object string, grant storedGrant, ifVersion *int64) (roleRow, error) {
	var roleID ids.UUID
	var version int64
	var before map[string]storedGrant
	var rawBefore []byte
	err := tx.QueryRow(ctx,
		`SELECT id, version, permissions FROM role WHERE key = $1 AND archived_at IS NULL FOR UPDATE`,
		roleKey).Scan(&roleID, &version, &rawBefore)
	if errors.Is(err, pgx.ErrNoRows) {
		return roleRow{}, errUnknownRole
	}
	if err != nil {
		return roleRow{}, err
	}
	// The version is compared INSIDE the lock rather than as a CAS predicate on
	// the UPDATE (storekit's ApplyWithVersion shape). Same refusal, and strictly
	// tighter: the row is already held, so no writer can slip between the check
	// and the write, and the "did it vanish, or was it merely stale" question a
	// zero-rows CAS has to go back and ask is answered here by construction.
	if ifVersion != nil && *ifVersion != version {
		return roleRow{}, apperrors.ErrVersionSkew
	}
	if before, err = decodeRoleObjects(rawBefore); err != nil {
		return roleRow{}, err
	}
	encoded, err := json.Marshal(grant)
	if err != nil {
		return roleRow{}, err
	}
	// A TARGETED write, not a parse-and-rewrite of the document. Re-serializing
	// a parsed document would silently erase every grant this installation
	// cannot name (a removed unit's objects) and every field the parse does not
	// model — data the operator never asked to touch. The inner jsonb_set
	// materializes `objects` when the document has none, because jsonb_set on a
	// two-step path whose parent is missing is a no-op that reports success.
	row := tx.QueryRow(ctx,
		`UPDATE role
		    SET permissions = jsonb_set(
		          jsonb_set(permissions, '{objects}', COALESCE(permissions->'objects', '{}'::jsonb), true),
		          ARRAY['objects', $2::text], $3::jsonb, true)
		  WHERE id = $1
		  RETURNING key, name, is_system, version, permissions`,
		roleID, object, encoded)
	updated, err := scanRoleRow(row)
	if err != nil {
		return roleRow{}, err
	}
	// entity_id is the role, not the object: the audit index is keyed on
	// (entity_type, entity_id), so an investigation asking "what happened to
	// the rep role" gets every grant change in one scan. WHICH object moved is
	// in the images, which name only the edited key — a full-document image
	// would bury a one-verb change under thirty unchanged ones.
	_, err = storekit.Audit(ctx, tx, "update", "role", roleID,
		map[string]any{"objects": map[string]storedGrant{object: before[object]}},
		map[string]any{"objects": map[string]storedGrant{object: grant}})
	if err != nil {
		return roleRow{}, err
	}
	return updated, nil
}

// scanRoleRow reads the editor's columns off any row-ish source (the list
// query, or the UPDATE's RETURNING) so both spell the decode once.
func scanRoleRow(row pgx.Row) (roleRow, error) {
	var out roleRow
	var raw []byte
	if err := row.Scan(&out.Key, &out.Name, &out.IsSystem, &out.Version, &raw); err != nil {
		return roleRow{}, err
	}
	objects, err := decodeRoleObjects(raw)
	if err != nil {
		return roleRow{}, err
	}
	out.Objects = objects
	return out, nil
}

// decodeRoleObjects reads `permissions.objects` off the stored jsonb. A
// document with no objects reads as the empty map rather than nil, so a caller
// ranging it needs no nil check and the wire carries `{}` — "this role grants
// nothing" — rather than `null`, which reads as "unknown".
//
// A document that is not an object at all is an error rather than an empty map:
// the caller is about to be shown, or to edit, what the row says, and answering
// "no grants" for bytes nobody can read would be a lie in the direction of
// safety on a screen whose whole job is to tell the truth about the document.
func decodeRoleObjects(raw []byte) (map[string]storedGrant, error) {
	out := map[string]storedGrant{}
	if len(raw) == 0 {
		return out, nil
	}
	var doc struct {
		Objects map[string]storedGrant `json:"objects"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("identity: role permissions document is unreadable: %w", err)
	}
	for object, grant := range doc.Objects {
		out[object] = grant
	}
	return out, nil
}
