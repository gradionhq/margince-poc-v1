// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/jurisdiction"
)

// Fixtures shared between this package's suites and the suite packages split out
// of it. They live in a NON-test file on purpose: a subpackage can import
// identifiers from this package's ordinary files, and nothing at all from its
// _test.go files, so a helper two suite packages need has to sit here or be
// copied into each — and a copied seeder drifts from the one it was copied from.
//
// The bar for moving a helper here is a caller on BOTH sides of a package
// boundary. A helper only one suite uses belongs in that suite, next to the test
// that reads it.
//
// This file must not import internal/compose/integration/apptest: apptest
// imports compose, and compose's white-box tests import this package, so an
// ordinary file here that reaches apptest closes an import cycle. A fixture that
// takes an *apptest.AppEnv therefore belongs in apptest, not here.

// CraftCursor encodes a keyset cursor the way the wire does, so a suite can hand
// a list endpoint a cursor it never issued.
//
// Crafting it rather than replaying one the API returned is the point: the
// pagination contract has to hold for a cursor pointing at a row that has since
// moved or gone, and only a hand-made one can name that position.
func CraftCursor(t *testing.T, c storekit.Cursor) string {
	t.Helper()
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshaling crafted cursor: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// CFAdminPerms is the admin RBAC posture narrowed to full custom_field config
// authority plus the person grants the value-preservation assertions need. It is
// narrower than AdminPerms on purpose: a suite proving a custom-field refusal
// must not be holding grants the refusal could be mistaken for.
var CFAdminPerms = principal.Permissions{
	RoleKeys: []string{"admin"},
	Objects: map[string]principal.ObjectGrant{
		"custom_field": {Create: true, Read: true, Update: true, Delete: true},
		"person":       {Create: true, Read: true, Update: true, Delete: true},
	},
	RowScope: principal.RowScopeAll,
}

// SeedSecondWorkspace inserts a second tenant with its own admin user and returns
// an admin-shaped context bound to it, for the cross-tenant suites.
//
// It carries CFAdminPerms rather than AdminPerms, which is not incidental: the
// suites that read this context assert what a caller in ANOTHER workspace cannot
// see, and widening the grants would let a passing assertion mean less than it
// says.
func SeedSecondWorkspace(t *testing.T, owner *pgx.Conn) (ids.UUID, context.Context) {
	t.Helper()
	ws, user := ids.NewV7(), ids.NewV7()
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO workspace (id, slug) VALUES ($1, $2)`,
		ws, "tenant-b-"+ws.String()[:8]); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'B Admin')`,
		user, ws, "b@tenant-b.test"); err != nil {
		t.Fatal(err)
	}
	ctx := principal.WithWorkspaceID(context.Background(), ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(),
		UserID: user, Permissions: CFAdminPerms,
	})
	return ws, ctx
}

// ExtractStagedApprovalID pulls the staged approval's id out of the 403
// approval_required detail — the same reference the human inbox lists.
//
// Reading it out of the message is deliberate: it is the only reference a 🟡
// caller is given, so a suite that resolved the id any other way would stop
// proving that the refusal hands back something actionable.
func ExtractStagedApprovalID(t *testing.T, detail string) string {
	t.Helper()
	const marker = "staged as approval "
	i := strings.Index(detail, marker)
	if i < 0 {
		t.Fatalf("no staged approval reference in %q", detail)
	}
	// Fields rather than a scan to the next space, so a marker with nothing after
	// it fails HERE. Returning the empty remainder would send the caller to
	// /v1/approvals/ with no id, and the 404 that came back would be reported as
	// the approval not existing — which is the one thing the suite is trying to
	// find out.
	rest := strings.Fields(detail[i+len(marker):])
	if len(rest) == 0 {
		t.Fatalf("the staged-approval reference in %q names no id", detail)
	}
	return rest[0]
}

// GoBDFloorPack is the six-calendar-year correspondence floor the retention
// suites test against — a stand-in jurisdiction under a reserved code, not the
// shipped de pack, so a suite asserts the seam rather than one country's law.
type GoBDFloorPack struct{}

// Code is a reserved two-letter code, so this pack can never collide with a
// shipped jurisdiction or be mistaken for one in a failure message.
func (GoBDFloorPack) Code() jurisdiction.Code { return "zq" }

// Retention declares the one class these suites turn on — the commercial
// correspondence floor.
//
//nolint:ireturn // jurisdiction.Pack declares this method returning the interface; a concrete return type would not satisfy it.
func (GoBDFloorPack) Retention() jurisdiction.Retention { return goBDFloorClasses{} }

type goBDFloorClasses struct{}

func (goBDFloorClasses) Classes() []jurisdiction.RetentionClass {
	return []jurisdiction.RetentionClass{
		{Name: jurisdiction.CommercialCorrespondence, Keep: jurisdiction.Period{Years: 6}, Anchor: jurisdiction.AnchorCalendarYearEnd},
	}
}

// RegisterGoBDFloorPack arms the floor the way the composed boot does. The
// registry is process-global and one package is one binary, so every suite
// package whose tests depend on the floor must call this from its own init —
// leaving it behind does not fail to compile, it makes a destructive pass over
// correspondence go green precisely because the floor that shields it is absent.
func RegisterGoBDFloorPack() {
	jurisdiction.Register(GoBDFloorPack{})
}

// SeedRetentionPolicies installs the policy set the retention engine acts on:
// one row per branch the sweep must take, so a suite asserting an outcome does
// not also have to state the policy that produced it.
func SeedRetentionPolicies(t *testing.T, e *Env) {
	t.Helper()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO retention_policy (workspace_id, object_type, category, retain_days, action)
			SELECT NULLIF(current_setting('app.workspace_id', true), '')::uuid, v.o, v.c, v.d, v.a
			FROM (VALUES
			  ('lead', 'unconverted', 365, 'anonymize'),
			  ('activity', NULL, 1095, 'archive'),
			  ('activity', 'transcript', 365, 'erase'),
			  ('person', 'no_consent_no_deal', 730, 'anonymize'),
			  ('deal', 'lost', 1825, 'archive')
			) AS v(o, c, d, a)`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
