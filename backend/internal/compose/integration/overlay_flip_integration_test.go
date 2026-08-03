// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The overlay→native flip lane (OVA-AC-6 a/b + B-E18.26/27), over the
// real composed HTTP stack + a real migrated Postgres, with the fake
// incumbent seeding the mirror exactly as the read-side e2e does:
//
//   - the preflight blocks honestly — a missing pre-flip export, then a
//     revoked incumbent connection (the OVA-AC-6a case: not-ready with
//     the incumbent-unreachable reason, workspace stays in overlay on
//     its last mirror, zero native rows written, the direct importer's
//     guard refusing with the SAME reason constant);
//   - the execute op answers 409 overlay_flip_blocked while unsatisfied
//     and 422 on a wrong typed phrase (AC-mode-flip-4/5);
//   - a green preflight seals the frozen snapshot and previews parity
//     with zero writes (AC-mode-flip-2/7);
//   - the fresh-sync execute migrates the estate with counts,
//     relationships, and provenance preserved, flips the workspace to
//     native, and the overlay lifecycle ops fall back to
//     mode_not_overlay (AC-OV-10; AC-mode-flip-6);
//   - the emergency cutover is refused while the incumbent is reachable,
//     requires the export, and returns the disclosed-lossy staleness +
//     unverifiable-parity notice when it runs (OVA-AC-6 b);
//   - the pre-flip export's own endpoint streams a bundle carrying the
//     mirror snapshot and the honest-scope manifest (AC-OV-9), audits it
//     only once complete, and an aborted stream leaves the flip's
//     export_missing gate shut.
//
// Use-case derivations this lane discharges:
//   - E2E-UC-E18-04.preflight-fail / .confirm-gate / .honest-skips —
//     the blocker matrix, the typed-phrase gate, disclosed skips.
//   - E2E-UC-E18-05.access-lapsed-before-flip and
//     E2E-UC-J-05.access-lapsed — the ordering hazard: revoked before
//     the flip → not-ready with incumbent_unreachable, workspace stays
//     on its last mirror, zero native rows, the direct importer refused
//     by the same constant, the emergency cutover offered only on the
//     explicit mode + typed phrase and returning the disclosure.
//   - E2E-UC-J-05.happy hand-offs 1→3 — connect → hydrate → flip with
//     counts/relationships preserved and reads served native after.
//
// UC-E18-05 F2 (disconnecting an UN-flipped overlay workspace) and F3
// (teardown partial-failure recovery) are named spec gaps — "do not
// invent behavior in the test" — so no case here asserts them; raised
// for upstream reconciliation instead.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/migration"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/fake"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// flipEstate is the seeded fixture the flip tests share: a connected
// overlay workspace whose mirror holds two organizations, two persons,
// two deals (one open on an unmatched incumbent stage, one closedwon),
// one lead, and one email activity — plus the association edges the
// detangling asserts on.
type flipEstate struct {
	e       *env
	wsID    ids.UUID
	adminID ids.UUID
	mirror  *overlay.MirrorStore
	pool    *pgxpool.Pool
	fakeInc *fake.Adapter
	// adminCtx is a fully-granted admin principal bound to the workspace
	// — the direct-seam context (mirror seeding, export writer, and the
	// RLS-scoped assertion reads).
	adminCtx context.Context
}

// inWorkspaceTx runs fn on the app pool under the estate's workspace GUC
// — every RLS-forced table read/write in these tests goes through it.
func (f flipEstate) inWorkspaceTx(t *testing.T, fn func(pgx.Tx) error) {
	t.Helper()
	if err := database.WithWorkspaceTx(f.adminCtx, f.pool, fn); err != nil {
		t.Fatalf("workspace tx: %v", err)
	}
}

// flipAdminPerms is the admin grant set the flip's direct-seam calls
// need: the record objects the export walks, overlay_connection (the
// flip gate), and import_run (the engine's run records).
func flipAdminPerms() principal.Permissions {
	crud := principal.ObjectGrant{Create: true, Read: true, Update: true, Delete: true}
	return principal.Permissions{
		RoleKeys: []string{"admin"},
		Objects: map[string]principal.ObjectGrant{
			"person": crud, "organization": crud, "deal": crud, "lead": crud,
			"activity": crud, "relationship": crud, "pipeline": crud,
			"overlay_connection": crud, "import_run": crud, "audit": {Read: true},
		},
		RowScope: principal.RowScopeAll,
	}
}

func flipAdminCtx(ws, user ids.UUID) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(), UserID: user,
		SeatType:    principal.SeatFull,
		Permissions: flipAdminPerms(),
	})
}

// setupFlipEstate provisions the workspace, connects the (fake-backed)
// incumbent, seeds and backfills the estate, and records the sweep
// success a force-fresh check requires.
func setupFlipEstate(t *testing.T) flipEstate {
	t.Helper()
	vault := keyvault.NewMemory()
	e := setupWithOptions(t, compose.WithKeyvault(vault))
	e.bootstrapWorkspace(t)

	if status := e.call(t, "POST", "/v1/overlay/connection", anyMap{
		"incumbent": "hubspot", "region": "eu1", "privateAppToken": "fake-token-never-used",
	}, nil, nil); status != http.StatusCreated {
		t.Fatalf("connect overlay = %d", status)
	}

	var me anyMap
	if status := e.call(t, "GET", "/v1/me", nil, nil, &me); status != http.StatusOK {
		t.Fatalf("/me = %d", status)
	}
	user, ok := me["user"].(anyMap)
	if !ok {
		t.Fatalf("/me carried no user object: %v", me)
	}
	rawID, ok := user["id"].(string)
	if !ok {
		t.Fatalf("/me user carried no id string: %v", user)
	}
	adminID, err := ids.Parse(rawID)
	if err != nil {
		t.Fatalf("parsing admin id: %v", err)
	}
	var wsIDStr string
	if err := e.owner.QueryRow(context.Background(), `SELECT id FROM workspace WHERE slug = $1`, e.slug).Scan(&wsIDStr); err != nil {
		t.Fatalf("workspace lookup: %v", err)
	}
	wsID, err := ids.Parse(wsIDStr)
	if err != nil {
		t.Fatalf("parsing workspace id: %v", err)
	}

	pool := openAppPool(t)
	mirror := overlay.NewMirrorStore(pool, stubOwnerEmails{})
	adminCtx := flipAdminCtx(wsID, adminID)

	if err := mirror.UpsertUserMap(adminCtx, ids.From[ids.UserKind](adminID), "hubspot", "owner-1", "manual"); err != nil {
		t.Fatalf("mapping the admin: %v", err)
	}

	var connectedAt time.Time
	if err := database.WithWorkspaceTx(adminCtx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(adminCtx, `SELECT connected_at FROM incumbent_connection`).Scan(&connectedAt)
	}); err != nil {
		t.Fatalf("reading connected_at: %v", err)
	}

	fakeInc := fake.New()
	seed := func(incumbentClass, canonicalClass, ext string, fields map[string]any) {
		rec := fake.Rec(ext, fields)
		rec.ObjectClass = canonicalClass
		rec.OwnerExternalID = "owner-1"
		fakeInc.Seed(incumbentClass, rec)
	}
	// Child fields are seeded NESTED, the shape overlay.Apply actually
	// lands them in (mapping.go's TargetChild) — a flat "person_email.email"
	// key is a shape the mapper never produces, and seeding one would let
	// a writer that reads it flat pass while dropping every real email.
	seed(overlay.IncumbentClassCompanies, "organization", "org-1", map[string]any{
		"display_name": "BÄR Pharma", "organization_domain": map[string]any{"domain": "baer-pharma.test"},
	})
	seed(overlay.IncumbentClassCompanies, "organization", "org-2", map[string]any{
		"display_name": "Gitex", "organization_domain": map[string]any{"domain": "gitex.test"},
	})
	seed(overlay.IncumbentClassContacts, "person", "p-1", map[string]any{
		"full_name": "Mor Anders", "first_name": "Mor", "last_name": "Anders",
		"person_email": map[string]any{"email": "mor@baer-pharma.test"},
	})
	seed(overlay.IncumbentClassContacts, "person", "p-2", map[string]any{
		"full_name": "Riya Patel", "person_email": map[string]any{"email": "riya@gitex.test"},
	})
	// A contact the incumbent left unowned — the common case in a real
	// portal, and the branch that must inherit the flip operator rather
	// than landing ownerless (an ownerless native row is workspace-shared
	// at every tier, while the mirror row was hidden from every seat).
	unowned := fake.Rec("p-unowned", map[string]any{"full_name": "Unassigned Contact"})
	unowned.ObjectClass = "person"
	fakeInc.Seed(overlay.IncumbentClassContacts, unowned)
	seed(overlay.IncumbentClassDeals, "deal", "d-open", map[string]any{
		"name": "Packaging QA", "stage_id": "appointmentscheduled", "amount_minor": int64(21200000), "currency": "EUR",
	})
	seed(overlay.IncumbentClassDeals, "deal", "d-won", map[string]any{
		"name": "Filling Line", "stage_id": "closedwon", "amount_minor": int64(500000), "currency": "EUR",
	})
	seed(overlay.IncumbentClassLeads, "lead", "l-1", map[string]any{
		"full_name": "Lars Prospect", "email": "lars@prospect.test",
	})
	seed(overlay.IncumbentClassEmails, "activity", "emails:900", map[string]any{
		"kind": "email", "subject": "Intro call follow-up", "body": "Notes from the call.",
		"occurred_at": "2026-07-01T10:00:00Z", "direction": "outbound",
	})

	backfillClasses := append([]string{
		overlay.IncumbentClassCompanies, overlay.IncumbentClassContacts,
		overlay.IncumbentClassDeals, overlay.IncumbentClassLeads,
	}, overlay.IncumbentEngagementClasses()...)
	for _, class := range backfillClasses {
		if _, err := overlay.Backfill(adminCtx, fakeInc, mirror, class, connectedAt); err != nil {
			t.Fatalf("backfilling %s: %v", class, err)
		}
	}

	// The association edges (canonical vocabulary, the adapter's output
	// shape): deal→organization FK, person→organization employment,
	// activity→person link.
	for _, a := range []overlay.Assoc{
		{FromType: "deal", FromID: "d-open", ToType: "organization", ToID: "org-1", TypeID: 5, Category: "HUBSPOT_DEFINED", Direction: "forward"},
		{FromType: "person", FromID: "p-1", ToType: "organization", ToID: "org-1", TypeID: 1, Category: "HUBSPOT_DEFINED", Label: "primary", Direction: "forward"},
		{FromType: "activity", FromID: "emails:900", ToType: "person", ToID: "p-1", TypeID: 9, Category: "HUBSPOT_DEFINED", Direction: "forward"},
	} {
		if err := mirror.UpsertAssoc(adminCtx, a); err != nil {
			t.Fatalf("seeding association %+v: %v", a, err)
		}
	}

	if err := mirror.RecordSweepSuccess(adminCtx, time.Now()); err != nil {
		t.Fatalf("recording sweep success: %v", err)
	}

	return flipEstate{e: e, wsID: wsID, adminID: adminID, mirror: mirror, pool: pool, fakeInc: fakeInc, adminCtx: adminCtx}
}

// writePreflipExport produces the bundle (and its audit row — the
// preflight's export-recency evidence) through the real writer.
func (f flipEstate) writePreflipExport(t *testing.T) {
	t.Helper()
	if _, err := compose.NewExportWriter(f.pool).WriteBundle(f.adminCtx, io.Discard); err != nil {
		t.Fatalf("writing the pre-flip export bundle: %v", err)
	}
}

func (f flipEstate) setConnectionStatus(t *testing.T, status string) {
	t.Helper()
	f.inWorkspaceTx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(f.adminCtx, `UPDATE incumbent_connection SET status = $1`, status)
		return err
	})
}

func (f flipEstate) connectionStatus(t *testing.T) string {
	t.Helper()
	var status string
	f.inWorkspaceTx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(f.adminCtx, `SELECT status FROM incumbent_connection`).Scan(&status)
	})
	return status
}

// nativeEstateRows counts native rows carrying flip provenance — the
// "zero native rows written" and parity assertions both read it.
func (f flipEstate) nativeEstateRows(t *testing.T) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for object, query := range map[string]string{
		"person":       `SELECT count(*) FROM person WHERE source LIKE 'mirror:hubspot:%'`,
		"organization": `SELECT count(*) FROM organization WHERE source LIKE 'mirror:hubspot:%'`,
		"deal":         `SELECT count(*) FROM deal WHERE source LIKE 'mirror:hubspot:%'`,
		"lead":         `SELECT count(*) FROM lead WHERE source_system = 'mirror:hubspot'`,
		"activity":     `SELECT count(*) FROM activity WHERE source_system = 'mirror:hubspot'`,
	} {
		var n int
		f.inWorkspaceTx(t, func(tx pgx.Tx) error {
			return tx.QueryRow(f.adminCtx, query).Scan(&n)
		})
		counts[object] = n
	}
	return counts
}

func (f flipEstate) workspaceMode(t *testing.T) (mode string, incumbent *string) {
	t.Helper()
	if err := f.e.owner.QueryRow(context.Background(),
		`SELECT x_sor_mode, x_incumbent FROM workspace WHERE id = $1`, f.wsID).Scan(&mode, &incumbent); err != nil {
		t.Fatalf("reading workspace mode: %v", err)
	}
	return mode, incumbent
}

func TestOverlayFlipPreflightBlocksHonestly(t *testing.T) {
	f := setupFlipEstate(t)
	e := f.e

	// 1. Everything green EXCEPT the pre-flip export: not ready, the
	// export_missing reason named, nothing sealed (F1's no-op return).
	var verdict crmcontracts.OverlayFlipPreflight
	if code := e.call(t, "POST", "/v1/overlay/flip:preflight", anyMap{}, nil, &verdict); code != http.StatusOK {
		t.Fatalf("preflight = %d", code)
	}
	if verdict.Ready || !hasBlocking(verdict, "export_missing") {
		t.Fatalf("verdict = %+v, want not-ready with export_missing", verdict)
	}
	if verdict.Snapshot != nil {
		t.Fatal("a blocked preflight must not leave a sealed snapshot")
	}

	// 2. The OVA-AC-6(a) case: connection revoked → incumbent-unreachable
	// blocking reason + the emergency disclosure; the workspace stays in
	// overlay on its last mirror and nothing is partially migrated.
	f.setConnectionStatus(t, "revoked")
	if code := e.call(t, "POST", "/v1/overlay/flip:preflight", anyMap{}, nil, &verdict); code != http.StatusOK {
		t.Fatalf("preflight (revoked) = %d", code)
	}
	if verdict.Ready || !hasBlocking(verdict, "incumbent_unreachable") {
		t.Fatalf("verdict = %+v, want incumbent_unreachable", verdict)
	}
	if verdict.Emergency == nil || !verdict.Emergency.Available ||
		verdict.Emergency.LastSyncedAt == nil || verdict.Emergency.UnverifiableParityNotice == "" {
		t.Fatalf("emergency block = %+v, want available with staleness + unverifiable-parity notice", verdict.Emergency)
	}

	if mode, _ := f.workspaceMode(t); mode != "overlay" {
		t.Fatalf("x_sor_mode = %s, want overlay (unchanged)", mode)
	}
	for object, n := range f.nativeEstateRows(t) {
		if n != 0 {
			t.Errorf("native %s rows = %d, want 0 (nothing partially migrated)", object, n)
		}
	}
	// The mirror stays readable on its last state — the estate reads
	// still serve (fully readable, never partially migrated).
	if rows, err := f.mirror.FlipRows(f.adminCtx, "person", 0, 10); err != nil || len(rows) != 3 {
		t.Fatalf("mirror person rows after block = %d (%v), want 3 readable", len(rows), err)
	}

	// The execute op is blocked with the flip-blocked sentinel.
	var problem anyMap
	if code := e.call(t, "POST", "/v1/overlay/flip", anyMap{
		"confirmation_phrase": "FLIP TO SOR",
	}, nil, &problem); code != http.StatusConflict {
		t.Fatalf("execute while blocked = %d %v, want 409", code, problem)
	}
	if problem["code"] != "overlay_flip_blocked" {
		t.Fatalf("execute problem code = %v, want overlay_flip_blocked", problem["code"])
	}

	// The direct importer is blocked the same way, for the same reason,
	// via the SAME constant (OVA-AC-6a's importer clause): the engine's
	// guard over the connection's actual status.
	guardErr := migration.GuardIncumbentSource(f.connectionStatus(t))
	if guardErr == nil || !strings.Contains(guardErr.Error(), migration.ReasonIncumbentUnreachable) {
		t.Fatalf("importer guard err = %v, want the %s reason", guardErr, migration.ReasonIncumbentUnreachable)
	}
	if !errors.Is(guardErr, apperrors.ErrConflict) {
		t.Fatalf("importer guard err = %v, want ErrConflict identity", guardErr)
	}

	// 3. Restore the connection + write the export: preflight goes green,
	// seals the snapshot, and previews parity with ZERO rows written.
	f.setConnectionStatus(t, "active")
	f.writePreflipExport(t)
	if code := e.call(t, "POST", "/v1/overlay/flip:preflight", anyMap{}, nil, &verdict); code != http.StatusOK {
		t.Fatalf("preflight (green) = %d", code)
	}
	if !verdict.Ready || len(verdict.Blocking) != 0 {
		t.Fatalf("verdict = %+v, want ready", verdict)
	}
	if verdict.Snapshot == nil || verdict.Snapshot.Id == "" {
		t.Fatal("a green preflight must seal and report the frozen snapshot id")
	}
	if verdict.Parity == nil {
		t.Fatal("a green preflight must carry the parity preview")
	}
	parityByObject := map[string]int{}
	for _, p := range *verdict.Parity {
		parityByObject[p.Object] = p.WillCreate
	}
	want := map[string]int{"organization": 2, "person": 3, "deal": 2, "lead": 1, "activity": 1}
	for object, n := range want {
		if parityByObject[object] != n {
			t.Errorf("parity will_create[%s] = %d, want %d", object, parityByObject[object], n)
		}
	}
	for object, n := range f.nativeEstateRows(t) {
		if n != 0 {
			t.Errorf("native %s rows after dry-run = %d, want 0 (the dry-run writes nothing)", object, n)
		}
	}

	// 4. The typed-phrase gate: a wrong phrase never runs the flip.
	if code := e.call(t, "POST", "/v1/overlay/flip", anyMap{
		"confirmation_phrase": "flip to sor",
	}, nil, &problem); code != http.StatusUnprocessableEntity {
		t.Fatalf("wrong-phrase execute = %d, want 422", code)
	}
}

func hasBlocking(v crmcontracts.OverlayFlipPreflight, reason string) bool {
	for _, b := range v.Blocking {
		if string(b) == reason {
			return true
		}
	}
	return false
}

func TestOverlayFlipFreshSyncExecute(t *testing.T) {
	f := setupFlipEstate(t)
	e := f.e
	f.writePreflipExport(t)

	var verdict crmcontracts.OverlayFlipPreflight
	if code := e.call(t, "POST", "/v1/overlay/flip:preflight", anyMap{}, nil, &verdict); code != http.StatusOK || !verdict.Ready {
		t.Fatalf("green preflight = %d ready=%v", code, verdict.Ready)
	}

	var accepted crmcontracts.OverlayFlipAccepted
	if code := e.call(t, "POST", "/v1/overlay/flip", anyMap{
		"confirmation_phrase": "FLIP TO SOR",
	}, nil, &accepted); code != http.StatusAccepted {
		t.Fatalf("execute = %d %+v", code, accepted)
	}
	if accepted.Mode != crmcontracts.OverlayFlipAcceptedModeFreshSync || accepted.RecordsImported == nil {
		t.Fatalf("accepted = %+v, want fresh_sync with a count", accepted)
	}
	if accepted.EmergencyDisclosure != nil {
		t.Fatal("a fresh-sync flip must not carry the emergency disclosure — never silently substituted")
	}

	// Mode flipped: native, incumbent cleared (DS-AC-5), connection kept.
	mode, incumbent := f.workspaceMode(t)
	if mode != "native" || incumbent != nil {
		t.Fatalf("workspace = %s/%v, want native with x_incumbent cleared", mode, incumbent)
	}
	if connStatus := f.connectionStatus(t); connStatus != "active" {
		t.Fatalf("connection = %s, want active (retirement revokes it later)", connStatus)
	}

	// Counts preserved (AC-OV-10 parity vs the frozen estate).
	counts := f.nativeEstateRows(t)
	for object, n := range map[string]int{"person": 3, "organization": 2, "deal": 2, "lead": 1, "activity": 1} {
		if counts[object] != n {
			t.Errorf("native %s rows = %d, want %d", object, counts[object], n)
		}
	}
	if got := int64(3 + 2 + 2 + 1 + 1); *accepted.RecordsImported != got {
		t.Errorf("records_imported = %d, want %d", *accepted.RecordsImported, got)
	}

	// Relationships preserved: the deal→organization FK, the primary
	// employment row, the activity link (IEM-FORM-2's detangling), and
	// the won deal closed through the real advance path.
	assertOne := func(name, query string) {
		t.Helper()
		var n int
		f.inWorkspaceTx(t, func(tx pgx.Tx) error {
			return tx.QueryRow(f.adminCtx, query).Scan(&n)
		})
		if n != 1 {
			t.Errorf("%s = %d rows, want 1", name, n)
		}
	}
	assertOne("deal→organization FK", `
		SELECT count(*) FROM deal d JOIN organization o ON o.id = d.organization_id AND o.workspace_id = d.workspace_id
		WHERE d.source = 'mirror:hubspot:deal:d-open' AND o.source = 'mirror:hubspot:organization:org-1'`)
	assertOne("primary employment relationship", `
		SELECT count(*) FROM relationship r
		JOIN person p ON p.id = r.person_id AND p.workspace_id = r.workspace_id
		WHERE r.kind = 'employment' AND r.is_current_primary AND p.source = 'mirror:hubspot:person:p-1'`)
	assertOne("activity link", `
		SELECT count(*) FROM activity_link al
		JOIN activity a ON a.id = al.activity_id AND a.workspace_id = al.workspace_id
		JOIN person p ON p.id = al.person_id AND p.workspace_id = al.workspace_id
		WHERE a.source_system = 'mirror:hubspot' AND p.source = 'mirror:hubspot:person:p-1'`)
	assertOne("closed-won deal", `
		SELECT count(*) FROM deal WHERE source = 'mirror:hubspot:deal:d-won' AND status = 'won'`)
	// The child rows the mapper nests: a contact's email and a company's
	// domain. Both were silently dropped by a flat read once, so they
	// are pinned per-record rather than by count.
	assertOne("imported person's email", `
		SELECT count(*) FROM person_email pe
		JOIN person p ON p.id = pe.person_id AND p.workspace_id = pe.workspace_id
		WHERE p.source = 'mirror:hubspot:person:p-1' AND pe.email = 'mor@baer-pharma.test'`)
	assertOne("imported organization's domain", `
		SELECT count(*) FROM organization_domain od
		JOIN organization o ON o.id = od.organization_id AND o.workspace_id = od.workspace_id
		WHERE o.source = 'mirror:hubspot:organization:org-1' AND od.domain = 'baer-pharma.test'`)
	// Owners survive the flip: every estate row named incumbent owner
	// "owner-1", which mirror_user_map binds to the admin.
	var ownedByAdmin int
	f.inWorkspaceTx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(f.adminCtx,
			`SELECT count(*) FROM person WHERE source LIKE 'mirror:hubspot:%' AND owner_id = $1`, f.adminID).Scan(&ownedByAdmin)
	})
	if ownedByAdmin != 3 {
		t.Errorf("imported persons owned by the admin = %d, want 3 — two by mirror_user_map, and the unowned one inheriting the operator", ownedByAdmin)
	}
	// Specifically: the unowned record is NOT ownerless.
	var ownerless int
	f.inWorkspaceTx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(f.adminCtx,
			`SELECT count(*) FROM person WHERE source LIKE 'mirror:hubspot:%' AND owner_id IS NULL`).Scan(&ownerless)
	})
	if ownerless != 0 {
		t.Errorf("%d imported person(s) landed ownerless — an ownerless native row is visible to every seat, which the mirror row was not", ownerless)
	}

	// The lifecycle ops fall back to mode_not_overlay, /me reports
	// native, and the native read serves the imported person.
	if code := e.call(t, "GET", "/v1/overlay/sync-status", nil, nil, nil); code != http.StatusNotFound {
		t.Errorf("sync-status after flip = %d, want 404 mode_not_overlay", code)
	}
	var me anyMap
	if code := e.call(t, "GET", "/v1/me", nil, nil, &me); code != http.StatusOK {
		t.Fatalf("/me = %d", code)
	}
	if sor, ok := me["system_of_record"].(anyMap); !ok || sor["mode"] != "native" {
		t.Errorf("/me system_of_record = %v, want native", me["system_of_record"])
	}
	var people crmcontracts.PersonListResponse
	if code := e.call(t, "GET", "/v1/people", nil, nil, &people); code != http.StatusOK {
		t.Fatalf("GET /v1/people after flip = %d", code)
	}
	if len(people.Data) != 3 {
		t.Errorf("native people after flip = %d, want 3", len(people.Data))
	}

	// A second execute is refused: the flip is one-way (the lifecycle op
	// answers mode_not_overlay once native).
	if code := e.call(t, "POST", "/v1/overlay/flip", anyMap{
		"confirmation_phrase": "FLIP TO SOR",
	}, nil, nil); code != http.StatusNotFound {
		t.Errorf("second execute = %d, want 404 mode_not_overlay", code)
	}
}

func TestOverlayFlipEmergencyCutover(t *testing.T) {
	f := setupFlipEstate(t)
	e := f.e

	// Refused while the incumbent is reachable — the never-substituted
	// guarantee holds in BOTH directions.
	f.writePreflipExport(t)
	var problem anyMap
	if code := e.call(t, "POST", "/v1/overlay/flip", anyMap{
		"mode": "emergency", "confirmation_phrase": "FLIP TO SOR",
	}, nil, &problem); code != http.StatusConflict {
		t.Fatalf("emergency while reachable = %d, want 409", code)
	}
	if problem["code"] != "overlay_flip_blocked" {
		t.Fatalf("problem code = %v, want overlay_flip_blocked", problem["code"])
	}

	// Access lapses. The fresh-sync path refuses (asserted in the
	// preflight test); the emergency path runs — confirm-first (the
	// typed phrase) and disclosed-lossy.
	f.setConnectionStatus(t, "revoked")

	var accepted crmcontracts.OverlayFlipAccepted
	if code := e.call(t, "POST", "/v1/overlay/flip", anyMap{
		"mode": "emergency", "confirmation_phrase": "FLIP TO SOR",
	}, nil, &accepted); code != http.StatusAccepted {
		t.Fatalf("emergency execute = %d %+v", code, accepted)
	}
	if accepted.Mode != crmcontracts.OverlayFlipAcceptedModeEmergency {
		t.Fatalf("accepted.Mode = %s, want emergency", accepted.Mode)
	}
	if accepted.EmergencyDisclosure == nil ||
		accepted.EmergencyDisclosure.LastSyncedAt == nil ||
		accepted.EmergencyDisclosure.StalenessSeconds == nil ||
		accepted.EmergencyDisclosure.UnverifiableParityNotice == "" {
		t.Fatalf("emergency disclosure = %+v, want staleness + unverifiable-parity notice returned", accepted.EmergencyDisclosure)
	}

	if mode, _ := f.workspaceMode(t); mode != "native" {
		t.Fatalf("x_sor_mode after emergency cutover = %s, want native", mode)
	}
	counts := f.nativeEstateRows(t)
	if counts["person"] != 3 || counts["deal"] != 2 {
		t.Errorf("estate after emergency cutover = %+v, want the last-known mirror imported", counts)
	}
}

// The pre-flip export's own HTTP surface: the producer the flip's
// export_missing gate depends on. It is admin/ops + human-only, and its
// audit row — the thing the preflight actually reads — must be written
// only once a complete bundle has streamed.
func TestOverlayExportDownload(t *testing.T) {
	f := setupFlipEstate(t)
	e := f.e

	auditRows := func() int {
		t.Helper()
		var n int
		f.inWorkspaceTx(t, func(tx pgx.Tx) error {
			return tx.QueryRow(f.adminCtx,
				`SELECT count(*) FROM audit_log WHERE entity_type = 'workspace' AND action = 'export'`).Scan(&n)
		})
		return n
	}
	if auditRows() != 0 {
		t.Fatal("no export has happened yet")
	}

	req, err := http.NewRequest(http.MethodGet, e.ts.URL+"/v1/overlay/export", nil)
	if err != nil {
		t.Fatalf("building the export request: %v", err)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/overlay/export: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the bundle: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("closing the response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/overlay/export = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/zip" {
		t.Errorf("Content-Type = %q, want application/zip", ct)
	}
	if !strings.Contains(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Errorf("Content-Disposition = %q, want an attachment", resp.Header.Get("Content-Disposition"))
	}

	// A real archive carrying BOTH halves of AC-OV-9: the mirror
	// snapshot members, and the manifest line disclosing that canonical
	// data still resides in the incumbent.
	entries := bundleEntries(t, body)
	var manifest struct {
		CanonicalDataResidesIn string `json:"canonical_data_resides_in"`
	}
	if err := json.Unmarshal(entries["manifest.json"], &manifest); err != nil {
		t.Fatalf("decoding the manifest: %v", err)
	}
	if manifest.CanonicalDataResidesIn != "hubspot" {
		t.Errorf("manifest discloses %q, want the incumbent — P7 is partial until the flip", manifest.CanonicalDataResidesIn)
	}
	for _, member := range []string{"overlay_mirror.csv", "overlay_association.csv", "mirror_user_map.csv"} {
		if len(entries[member]) == 0 {
			t.Errorf("bundle has no %s — an overlay-mode export must carry the mirror snapshot", member)
		}
	}
	// The mirror member carries the whole seeded estate, not just a
	// header — counted through the CSV reader, since a newline inside a
	// quoted `fields` cell would inflate a line count and mask a
	// dropped row.
	if got := len(csvColumn(t, entries["overlay_mirror.csv"], "external_id")); got != 9 {
		t.Errorf("overlay_mirror.csv has %d rows, want the 9 seeded mirror records", got)
	}

	// Exactly one audit row, and it satisfies the preflight's gate.
	if got := auditRows(); got != 1 {
		t.Errorf("export audit rows = %d, want exactly 1", got)
	}
	var verdict crmcontracts.OverlayFlipPreflight
	if code := e.call(t, "POST", "/v1/overlay/flip:preflight", anyMap{}, nil, &verdict); code != http.StatusOK {
		t.Fatalf("preflight after export = %d", code)
	}
	if hasBlocking(verdict, "export_missing") {
		t.Error("a completed export must clear the export_missing gate")
	}
}

// The export audit row is what the flip's export_missing gate reads, so
// it must mean "a complete bundle exists" — not "an export was
// attempted". A stream that dies partway must leave the gate shut;
// asserting only the happy path would pass just as well with the audit
// written BEFORE the bundle, which is the ordering this pins.
func TestAbortedExportDoesNotSatisfyTheFlipGate(t *testing.T) {
	f := setupFlipEstate(t)

	auditRows := func() int {
		t.Helper()
		var n int
		f.inWorkspaceTx(t, func(tx pgx.Tx) error {
			return tx.QueryRow(f.adminCtx,
				`SELECT count(*) FROM audit_log WHERE entity_type = 'workspace' AND action = 'export'`).Scan(&n)
		})
		return n
	}

	// A writer that dies after the first bytes, the way a client that
	// disconnects mid-download does.
	failing := &failAfterWriter{limit: 64}
	if _, err := compose.NewExportWriter(f.pool).WriteBundle(f.adminCtx, failing); err == nil {
		t.Fatal("a bundle whose destination fails must surface the error, not report success")
	}
	if got := auditRows(); got != 0 {
		t.Fatalf("aborted export wrote %d audit row(s); the flip's export gate would clear with no bundle behind it", got)
	}

	// And the preflight still blocks on it.
	var verdict crmcontracts.OverlayFlipPreflight
	if code := f.e.call(t, "POST", "/v1/overlay/flip:preflight", anyMap{}, nil, &verdict); code != http.StatusOK {
		t.Fatalf("preflight = %d", code)
	}
	if !hasBlocking(verdict, "export_missing") {
		t.Error("an aborted export must leave export_missing blocking")
	}
}

// failAfterWriter accepts limit bytes and then fails, standing in for a
// client that disconnects mid-download.
type failAfterWriter struct {
	limit   int
	written int
}

func (w *failAfterWriter) Write(p []byte) (int, error) {
	w.written += len(p)
	if w.written > w.limit {
		return 0, errors.New("client went away mid-stream")
	}
	return len(p), nil
}
