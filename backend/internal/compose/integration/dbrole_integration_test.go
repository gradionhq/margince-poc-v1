// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
)

func TestAssertRuntimeRoleAcceptsTheRuntimePool(t *testing.T) {
	env := Setup(t)

	// Also the absent-ext-schema case: the ext schema lands with the
	// extension migration namespace, so on today's tree the ownership
	// subquery must count zero rather than fail on an unresolvable name.
	if err := compose.AssertRuntimeRole(context.Background(), env.Pool); err != nil {
		t.Fatalf("AssertRuntimeRole rejected the app-role runtime pool: %v", err)
	}
}

func TestAssertRuntimeRoleRefusesTheOwnerPool(t *testing.T) {
	Setup(t)
	owner := SchemaPool(t)
	ctx := context.Background()

	// The refusal this test exists to catch only exists while the fixture
	// owner actually holds an exemption — in dev and CI it is the container
	// superuser. An owner that holds neither would make the assertion below
	// pass or fail for reasons that have nothing to do with the code.
	var super, bypass bool
	if err := owner.QueryRow(ctx,
		`SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`,
	).Scan(&super, &bypass); err != nil {
		t.Fatalf("reading the owner role's attributes: %v", err)
	}
	if !super && !bypass {
		t.Fatal("the fixture owner role holds neither rolsuper nor rolbypassrls, so this test no longer exercises the refusal it names")
	}

	if err := compose.AssertRuntimeRole(ctx, owner); err == nil {
		t.Fatal("AssertRuntimeRole accepted the owner pool; a role RLS does not bind must never serve runtime traffic")
	}
}

func TestAssertRuntimeRoleRefusesARoleOwningExtObjects(t *testing.T) {
	env := Setup(t)
	ctx := context.Background()

	// The DDL/runtime lane separation rests on ownership as well as the two
	// role flags: an owner of an ext relation can drop that relation's RLS
	// policies outright, which no flag on the role would reveal.
	var runtimeRole string
	if err := env.Pool.QueryRow(ctx, `SELECT current_user`).Scan(&runtimeRole); err != nil {
		t.Fatalf("reading the runtime role name: %v", err)
	}
	owner := OwnerConn(t)
	for _, statement := range []string{
		`CREATE SCHEMA IF NOT EXISTS ext`,
		`CREATE TABLE ext.runtime_role_probe (id uuid PRIMARY KEY)`,
		`ALTER TABLE ext.runtime_role_probe OWNER TO ` + runtimeRole,
	} {
		if _, err := owner.Exec(ctx, statement); err != nil {
			t.Fatalf("preparing the ext-owned probe relation (%s): %v", statement, err)
		}
	}
	t.Cleanup(func() {
		// The table goes, the schema stays: it is created IF NOT EXISTS here
		// and by the extension migration namespace alike, and dropping it
		// would remove a schema this test did not necessarily create.
		if _, err := owner.Exec(context.Background(), `DROP TABLE ext.runtime_role_probe`); err != nil {
			t.Errorf("removing the ext-owned probe relation: %v", err)
		}
	})

	if err := compose.AssertRuntimeRole(ctx, env.Pool); err == nil {
		t.Fatal("AssertRuntimeRole accepted a runtime role owning an ext relation")
	}
}
