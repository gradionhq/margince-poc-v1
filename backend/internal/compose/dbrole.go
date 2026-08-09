// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// runtimeRoleQuery reads the connected role's exemptions and its ownership
// inside the extension schema in ONE round trip. current_user is resolved in
// the same statement rather than fetched separately: a pooler can hand a
// second query a different session, and the answer would then describe a role
// this pool never serves under.
//
// The ownership arms are catalog joins, not a cast of 'ext' to regnamespace:
// the schema is created by the extension migration namespace and is
// legitimately absent on an installation that has none, where the honest answer
// is zero objects owned rather than a boot failure.
//
// Both arms are read, and the schema arm is not redundant with the relation
// one: on an installation with no units yet composed, `ext` holds no relations,
// so a pool pointed at the owner DSN would pass on an empty count while holding
// the schema itself — and a schema owner can CREATE, and therefore later own,
// every unit table that lands in it. The empty tree is exactly the
// configuration this check must still protect.
const runtimeRoleQuery = `
	SELECT r.rolname, r.rolsuper, r.rolbypassrls,
	       (SELECT count(*) FROM pg_class c
	          JOIN pg_namespace n ON n.oid = c.relnamespace
	         WHERE n.nspname = 'ext' AND c.relowner = r.oid),
	       EXISTS (SELECT 1 FROM pg_namespace n
	                WHERE n.nspname = 'ext' AND n.nspowner = r.oid)
	  FROM pg_roles r WHERE r.rolname = current_user`

// AssertRuntimeRole refuses to serve when the runtime pool carries an
// exemption the tier's guarantees assume it lacks. Extension code reaches the
// database through this pool, so a deployment that points it at the migration
// owner would void the DDL/runtime lane separation silently — FORCE row-level
// security does not bind a superuser or a BYPASSRLS role, and an owner of an
// ext relation can drop that relation's tenant-isolation policies outright.
//
// It cannot run before extension code: both binaries register the composed
// extension set while assembling their configuration, which is earlier than
// the pool exists. The claim it makes is the checkable one — no extension
// gets runtime database access under a role holding any of the three.
func AssertRuntimeRole(ctx context.Context, pool *pgxpool.Pool) error {
	var role string
	var super, bypass, ownsExtSchema bool
	var ownsExt int
	if err := pool.QueryRow(ctx, runtimeRoleQuery).Scan(&role, &super, &bypass, &ownsExt, &ownsExtSchema); err != nil {
		return fmt.Errorf("reading the runtime role's attributes: %w", err)
	}
	if super || bypass || ownsExt > 0 || ownsExtSchema {
		return fmt.Errorf("the runtime pool connects as %q (superuser=%t bypassrls=%t, owns schema ext=%t, owns %d object(s) in schema ext) — "+
			"the app role must hold none of these; point it at the app DSN, not the migration owner's",
			role, super, bypass, ownsExtSchema, ownsExt)
	}
	return nil
}
