// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The cutover lifecycle's back half (OVA-AC-6 c/d): a completed flip is
// retired by disconnect — the mirror and its derivatives purge, native
// data + audit + the pre-flip export survive, the incumbent is
// byte-for-byte untouched throughout — and the pre-flip export
// reconstructs a clean native instance with zero incumbent calls
// (reversibility is rebuild, never rollback).
//
// Use-case derivations this lane discharges:
//   - E2E-UC-E18-05.happy — steps 1–6: native reads, downloadable +
//     re-importable bundle, confirm-first disconnect revokes + tears
//     down, purge-past-window with audit retained, incumbent unmodified,
//     no further incumbent call.
//   - E2E-UC-E18-05.incumbent-untouched — the before/after deep-compare.
//   - E2E-UC-J-05.happy hand-offs 3→6 — flip → native serving →
//     lock-in-proof rebuild → retirement (hand-offs 1→3, connect →
//     hydrate → flip, are the flip lane's, overlay_flip_integration_
//     test.go).

import (
	"bytes"
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/fake"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// snapshotIncumbent deep-copies the fake incumbent's whole record state
// (via its own Backfill read) — the before/after byte-identity check
// behind "the incumbent is never modified at any step".
func snapshotIncumbent(t *testing.T, inc *fake.Adapter) map[string][]overlay.Record {
	t.Helper()
	classes := append([]string{
		overlay.IncumbentClassCompanies, overlay.IncumbentClassContacts,
		overlay.IncumbentClassDeals, overlay.IncumbentClassLeads,
	}, overlay.IncumbentEngagementClasses()...)
	snap := map[string][]overlay.Record{}
	for _, class := range classes {
		page, err := inc.Backfill(context.Background(), class, "")
		if err != nil {
			t.Fatalf("snapshotting %s: %v", class, err)
		}
		snap[class] = page.Records
	}
	return snap
}

func TestOverlayCutoverRetirementAndReconstruction(t *testing.T) {
	f := setupFlipEstate(t)
	e := f.e
	incumbentBefore := snapshotIncumbent(t, f.fakeInc)

	// --- flip (fresh-sync), with the pre-flip bundle CAPTURED — it is
	// the reconstruction input asserted on below.
	var bundle bytes.Buffer
	if _, err := compose.NewExportWriter(f.pool).WriteBundle(f.adminCtx, &bundle); err != nil {
		t.Fatalf("writing the pre-flip export: %v", err)
	}
	var verdict crmcontracts.OverlayFlipPreflight
	if code := e.call(t, "POST", "/v1/overlay/flip:preflight", anyMap{}, nil, &verdict); code != http.StatusOK || !verdict.Ready {
		t.Fatalf("preflight = %d ready=%v blocking=%v", code, verdict.Ready, verdict.Blocking)
	}
	if code := e.call(t, "POST", "/v1/overlay/flip", anyMap{
		"confirmation_phrase": "FLIP TO SOR",
	}, nil, nil); code != http.StatusAccepted {
		t.Fatalf("execute = %d", code)
	}

	// --- retire: disconnect-after-flip (OVA-AC-6 c). The workspace is
	// already native; the disconnect revokes the connection and purges
	// the mirror + derivatives, while native data, audit, and the
	// export bundle remain.
	if code := e.call(t, "DELETE", "/v1/overlay/connection", nil, nil, nil); code != http.StatusAccepted {
		t.Fatalf("disconnect after flip = %d, want 202", code)
	}

	if status := f.connectionStatus(t); status != "revoked" {
		t.Errorf("connection after retirement = %s, want revoked", status)
	}
	if mode, incumbent := f.workspaceMode(t); mode != "native" || incumbent != nil {
		t.Errorf("workspace after retirement = %s/%v, want native with no incumbent", mode, incumbent)
	}
	// No incumbent-derived data remains queryable (OVA-AC-1's purge
	// list), and tombstones outlive the purge.
	for _, q := range []struct{ name, query string }{
		{"overlay_mirror", `SELECT count(*) FROM overlay_mirror`},
		{"overlay_association", `SELECT count(*) FROM overlay_association`},
		{"mirror_visibility", `SELECT count(*) FROM mirror_visibility`},
		{"mirror_user_map", `SELECT count(*) FROM mirror_user_map`},
	} {
		var n int
		f.inWorkspaceTx(t, func(tx pgx.Tx) error {
			return tx.QueryRow(f.adminCtx, q.query).Scan(&n)
		})
		if n != 0 {
			t.Errorf("%s rows after retirement = %d, want 0", q.name, n)
		}
	}
	var tombstones int
	f.inWorkspaceTx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(f.adminCtx, `SELECT count(*) FROM overlay_tombstone`).Scan(&tombstones)
	})
	if tombstones == 0 {
		t.Error("retirement wrote no tombstones — a stray in-flight sweep could resurrect purged rows")
	}

	// Native data and the audit spine survive retirement.
	counts := f.nativeEstateRows(t)
	for object, n := range map[string]int{"person": 2, "organization": 2, "deal": 2, "lead": 1, "activity": 1} {
		if counts[object] != n {
			t.Errorf("native %s rows after retirement = %d, want %d", object, counts[object], n)
		}
	}
	var auditRows int
	f.inWorkspaceTx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(f.adminCtx,
			`SELECT count(*) FROM audit_log WHERE entity_type = 'incumbent_connection'`).Scan(&auditRows)
	})
	if auditRows == 0 {
		t.Error("the connection lifecycle audit trail must survive retirement")
	}

	// The incumbent is byte-for-byte untouched across connect → backfill
	// → flip → retirement: zero writes, zero deletes.
	if incumbentAfter := snapshotIncumbent(t, f.fakeInc); !reflect.DeepEqual(incumbentBefore, incumbentAfter) {
		t.Error("the incumbent's record state changed across the cutover lifecycle — it must be byte-for-byte untouched")
	}

	// With the overlay retired, no further incumbent call is a
	// STRUCTURAL fact here: the fake was only ever driven by this test's
	// own Backfill calls, and nothing composed holds it anymore.

	// --- reconstruction (OVA-AC-6 d): the pre-flip export rebuilds a
	// CLEAN native instance — a fresh workspace — through the same
	// engine, with zero incumbent calls (this path holds no adapter).
	cleanCtx := seedCleanWorkspace(t, f)
	rep, err := compose.ReconstructForTest(cleanCtx, f.pool, bundle.Bytes())
	if err != nil {
		t.Fatalf("reconstruction: %v", err)
	}
	if rep.Imported == 0 {
		t.Fatal("reconstruction imported nothing")
	}
	assertCount := func(name, query string, want int) {
		t.Helper()
		var n int
		if err := database.WithWorkspaceTx(cleanCtx, f.pool, func(tx pgx.Tx) error {
			return tx.QueryRow(cleanCtx, query).Scan(&n)
		}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if n != want {
			t.Errorf("%s = %d, want %d", name, n, want)
		}
	}
	assertCount("reconstructed persons", `SELECT count(*) FROM person WHERE source LIKE 'hubspot:%'`, 2)
	assertCount("reconstructed organizations", `SELECT count(*) FROM organization WHERE source LIKE 'hubspot:%'`, 2)
	assertCount("reconstructed deals", `SELECT count(*) FROM deal WHERE source LIKE 'hubspot:%'`, 2)
	assertCount("reconstructed leads", `SELECT count(*) FROM lead WHERE source_system = 'mirror:hubspot'`, 1)
	assertCount("reconstructed activities", `SELECT count(*) FROM activity WHERE source_system = 'mirror:hubspot'`, 1)
	assertCount("reconstructed deal→org FK", `
		SELECT count(*) FROM deal d JOIN organization o ON o.id = d.organization_id AND o.workspace_id = d.workspace_id
		WHERE d.source = 'hubspot:deal:d-open' AND o.source = 'hubspot:organization:org-1'`, 1)
	assertCount("reconstructed employment", `
		SELECT count(*) FROM relationship r JOIN person p ON p.id = r.person_id AND p.workspace_id = r.workspace_id
		WHERE r.kind = 'employment' AND p.source = 'hubspot:person:p-1'`, 1)
	// The bundle's owner map named the SOURCE workspace's admin, who does
	// not exist in this clean instance — so ownership falls to the
	// rebuild's own operator rather than landing ownerless (an ownerless
	// native row is visible to every seat; the mirror row was not).
	rebuildOperator, ok := principal.Actor(cleanCtx)
	if !ok {
		t.Fatal("the clean-instance context carries no actor")
	}
	var ownedByOperator int
	if err := database.WithWorkspaceTx(cleanCtx, f.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(cleanCtx,
			`SELECT count(*) FROM person WHERE source LIKE 'hubspot:%' AND owner_id = $1`, rebuildOperator.UserID).Scan(&ownedByOperator)
	}); err != nil {
		t.Fatalf("counting rebuilt persons by owner: %v", err)
	}
	if ownedByOperator != 2 {
		t.Errorf("rebuilt persons owned by the operator = %d, want 2 — an unmapped owner must not leave the row workspace-visible", ownedByOperator)
	}

	// The incumbent still untouched after reconstruction — the rebuild
	// needed no live incumbent access at all.
	if incumbentAfter := snapshotIncumbent(t, f.fakeInc); !reflect.DeepEqual(incumbentBefore, incumbentAfter) {
		t.Error("reconstruction touched the incumbent — it must run with zero incumbent calls")
	}
}

// seedCleanWorkspace mints a second, empty workspace (its own tenant,
// zero records) with a seeded default pipeline — the clean instance the
// reconstruction lands in.
func seedCleanWorkspace(t *testing.T, f flipEstate) context.Context {
	t.Helper()
	ctx := context.Background()
	ws := ids.NewV7()
	if _, err := f.e.owner.Exec(ctx,
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Clean Rebuild', $2, 'EUR')`,
		ws, "clean-rebuild-"+ws.String()); err != nil {
		t.Fatalf("seeding the clean workspace: %v", err)
	}
	user := ids.New[ids.UserKind]()
	if _, err := f.e.owner.Exec(ctx,
		`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'Rebuild Admin')`,
		user, ws, "rebuild-"+user.String()+"@clean.test"); err != nil {
		t.Fatalf("seeding the clean workspace admin: %v", err)
	}
	cleanCtx := flipAdminCtx(ws, user.UUID)
	if err := deals.NewHandlers(f.pool).SeedWorkspaceDefaults(cleanCtx); err != nil {
		t.Fatalf("seeding the clean workspace's default pipeline: %v", err)
	}
	return cleanCtx
}
