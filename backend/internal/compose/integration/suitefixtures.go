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

// CraftCursor forges the opaque page token a hostile client could send: the Cursor
// JSON shape, base64url-encoded — bypassing the store's own minting so the sort key
// can carry arbitrary text.
//
// Every caller uses it to prove a malformed cursor answers 422 rather than 500, so
// replaying a cursor the API actually issued would remove the untrusted input the
// assertion is about.
func CraftCursor(t *testing.T, c storekit.Cursor) string {
	t.Helper()
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshaling crafted cursor: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// CustomFieldAdminPerms is full custom_field config authority plus the person
// grants the value-preservation assertions need.
//
// It is not AdminPerms narrowed — AdminPerms carries no custom_field grant at all,
// so neither contains the other. It exists because AdminPerms cannot drive the
// catalog, and the custom_field grant here is load-bearing in the direction that
// reads backwards: the cross-tenant suites assert that tenant B's identical query
// returns ZERO rows, and that is evidence of RLS only because B is permitted to
// ask. Take the grant away and the empty answer becomes an RBAC refusal wearing
// the same shape.
var CustomFieldAdminPerms = principal.Permissions{
	RoleKeys: []string{"admin"},
	Objects: map[string]principal.ObjectGrant{
		"custom_field": {Create: true, Read: true, Update: true, Delete: true},
		"person":       {Create: true, Read: true, Update: true, Delete: true},
	},
	RowScope: principal.RowScopeAll,
}

// ExtractStagedApprovalID pulls the staged approval's id out of the 403
// approval_required detail — the same reference the human inbox lists.
//
// Reading it out of the message is deliberate: it is the only reference a 🟡
// caller is given, so a suite that resolved the id any other way would stop
// proving that the refusal hands back something actionable.
//
// TWO markers, because a 🟡 refusal names an approval for two different reasons
// and both hand back a live id: the call was just staged, or a human had already
// approved this exact call and the gate is pointing at that decision rather than
// asking again (approvals.StageAgentCall). A suite that could only read the first
// would report the second as "no approval reference", which is the opposite of
// what happened.
func ExtractStagedApprovalID(t *testing.T, detail string) string {
	t.Helper()
	const marker = "staged as approval "
	i := strings.Index(detail, marker)
	if i < 0 {
		const approved = "already approved this exact "
		if j := strings.Index(detail, approved); j >= 0 {
			return approvalIDAfter(t, detail, detail[j:], "as approval ")
		}
		t.Fatalf("no staged approval reference in %q", detail)
	}
	// Fields rather than a scan to the next space, so a marker with nothing after
	// it fails HERE. Returning the empty remainder would send the caller to
	// /v1/approvals/ with no id, and the 404 that came back would be reported as
	// the approval not existing — which is the one thing the suite is trying to
	// find out.
	return approvalIDAfter(t, detail, detail[i:], marker)
}

// approvalIDAfter reads the id that follows marker in segment, reporting against
// the whole detail so a failure names what the caller actually received.
//
// Fields rather than a scan to the next space, so a marker with nothing after it
// fails HERE. Returning the empty remainder would send the caller to
// /v1/approvals/ with no id, and the 404 that came back would be reported as the
// approval not existing — which is the one thing the suite is trying to find out.
func approvalIDAfter(t *testing.T, detail, segment, marker string) string {
	t.Helper()
	i := strings.Index(segment, marker)
	if i < 0 {
		t.Fatalf("the approval reference in %q does not name an approval", detail)
	}
	rest := strings.Fields(segment[i+len(marker):])
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
			INSERT INTO retention_policy (object_type, category, retain_days, action)
			SELECT v.o, v.c, v.d, v.a
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
