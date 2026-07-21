// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The full HubSpot-overlay read+sync path, end to end, over the
// real handler stack + a real migrated Postgres. This is the ONE place
// branch 1's whole read-side story is proven working TOGETHER rather
// than piecemeal: connect (a fake incumbent, so no real HubSpot network
// call ever happens) → backfill → the sync-status HTTP surface reports
// fresh → a native-API read of the mirrored record (through
// compose.Dispatcher, the REAL composed path a native GET/read_record
// call rides in production, not a direct MirrorStore call) carries
// TrustTier=external/Authoritative=false → an unmapped user sees zero
// rows (fail-closed visibility, end to end) → disconnect purges the
// mirror, tombstones it, and leaves the connection's own audit trail
// retained and PII-free.
//
// The fake incumbent is the SEAM the test drives Backfill with directly
// (package-level overlay.Backfill, the same call jobs.go's poller and
// backfill.go's own doc describe) — connect's own HTTP call still names
// "hubspot" (branch 1's Connect only ever validates that one incumbent
// name, connection.go's ConnectInput.validate), but never reaches a real
// HubSpot: nothing in this test calls hubspot.NewAdapter or hubspot.
// NewClient. This is the SAME "fake incumbent as the test incumbent"
// posture backfill_test.go and the fake package's own doc already
// establish.
//
// This test needs a *pgxpool.Pool of its own (the env/setup() harness
// exposes none) to drive compose.Dispatcher/overlay.Backfill directly —
// openAppPool opens a second, independent app-role connection to the
// SAME database the httptest server is backed by, so rows committed
// through one are immediately visible through the other.

import (
	"context"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/fake"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// syncStatusWire is the wire slice GET /v1/overlay/sync-status answers.
type syncStatusWire struct {
	Objects []struct {
		Object           string     `json:"object"`
		State            string     `json:"state"`
		BackfillComplete bool       `json:"backfillComplete"` //nolint:tagliatelle // must match the wire response verbatim (crm.yaml's own camelCase)
		LastSyncedAt     *time.Time `json:"lastSyncedAt"`     //nolint:tagliatelle // see above
	} `json:"objects"`
}

// budgetWire is the wire shape GET /v1/overlay/budget answers.
type budgetWire struct {
	Window   string  `json:"window"`
	Consumed float64 `json:"consumed"`
	Limit    float64 `json:"limit"`
	Band     string  `json:"band"`
}

// openAppPool opens a second, independent app-role pool over the SAME
// real database the httptest server (env.ts) is backed by.
func openAppPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("MARGINCE_TEST_APP_DSN")
	if dsn == "" {
		t.Fatal("MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	pool, err := database.NewPool(context.Background(), dsn)
	if err != nil {
		t.Fatalf("opening the app pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// seedSecondAppUser inserts one more human app_user into ws through the
// owner connection — an "unmapped" second actor sharing the workspace,
// deliberately never given a mirror_user_map row.
func seedSecondAppUser(t *testing.T, e *env, wsID ids.UUID, email string) ids.UUID {
	t.Helper()
	userID := ids.NewV7()
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'Unmapped User')`,
		userID, wsID, email); err != nil {
		t.Fatalf("seeding the unmapped app_user: %v", err)
	}
	return userID
}

// TestOverlayReadAndSyncEndToEnd is the full branch-1 read+sync story,
// proven over the real composed HTTP + package path together.
func TestOverlayReadAndSyncEndToEnd(t *testing.T) {
	vault := keyvault.NewMemory()
	e := setupWithOptions(t, compose.WithKeyvault(vault))
	e.bootstrapWorkspace(t)

	// Sync-status/budget/reconcile are all mode-gated (ErrModeNotOverlay
	// -> 404): asserted here, BEFORE connect, while the workspace is
	// still in native mode — the one place in this test any of the three
	// can be exercised without a live incumbent adapter reaching for a
	// real HubSpot over the network (reconcile's on-demand sweep, once
	// connected, always builds a REAL hubspot.Adapter from the
	// connection's own vaulted region+token — see compose/jobs.go's
	// reconcileConnection — so this suite never calls it against a
	// connected workspace; T11 bars real-network reliance in a test).
	for _, path := range []string{"/v1/overlay/sync-status", "/v1/overlay/budget"} {
		if code := e.call(t, "GET", path, nil, nil, nil); code != http.StatusNotFound {
			t.Fatalf("GET %s in native mode = %d, want 404 mode_not_overlay", path, code)
		}
	}
	if code := e.call(t, "POST", "/v1/overlay/reconcile", nil, nil, nil); code != http.StatusNotFound {
		t.Fatalf("reconcile in native mode = %d, want 404 mode_not_overlay", code)
	}

	// While still native, the shadowed read ops (compose/overlayread.go)
	// delegate to the native module handlers — the ordinary native page
	// answers, proving the mode fallthrough side of the dispatch.
	if code := e.call(t, "GET", "/v1/people", nil, nil, nil); code != http.StatusOK {
		t.Fatalf("native-mode GET /v1/people through the shadowed op = %d, want 200", code)
	}

	// --- connect (branch 1's Connect only ever validates "hubspot" —
	// the private-app token is never actually used to reach a real
	// HubSpot in this test: every mirror row below lands through the
	// fake incumbent's own Backfill call, never through hubspot.Adapter) ---
	var conn map[string]any
	if status := e.call(t, "POST", "/v1/overlay/connection", anyMap{
		"incumbent":       "hubspot",
		"region":          "eu1",
		"privateAppToken": "fake-token-never-used",
	}, nil, &conn); status != http.StatusCreated {
		t.Fatalf("connect overlay = %d %v", status, conn)
	}

	var me anyMap
	if status := e.call(t, "GET", "/v1/me", nil, nil, &me); status != http.StatusOK {
		t.Fatalf("/me status = %d", status)
	}
	adminID, err := ids.Parse(me["user"].(anyMap)["id"].(string))
	if err != nil {
		t.Fatalf("parsing admin user id: %v", err)
	}
	var wsIDStr string
	if err := e.owner.QueryRow(context.Background(), `SELECT id FROM workspace WHERE slug = $1`, e.slug).Scan(&wsIDStr); err != nil {
		t.Fatalf("looking up the workspace id: %v", err)
	}
	wsID, err := ids.Parse(wsIDStr)
	if err != nil {
		t.Fatalf("parsing workspace id: %v", err)
	}

	pool := openAppPool(t)

	// --- map the admin to the fake incumbent's owner-1, then backfill
	// ONE contacts record it owns, directly through the fake incumbent
	// seam (Backfill, mirrorstore.go's Ingest — the same primitives the
	// real poller/backfill worker drive) ---
	mirror := overlay.NewMirrorStore(pool, stubOwnerEmails{})
	adminCtx := overlayActorCtx(wsID, adminID)
	if err := mirror.UpsertUserMap(adminCtx, ids.From[ids.UserKind](adminID), "hubspot", "owner-1", "manual"); err != nil {
		t.Fatalf("mapping the admin to the fake incumbent owner: %v", err)
	}

	fakeInc := fake.New()
	// Canonical object class AND canonical field names — the fake stands
	// in for the mapping adapter's OUTPUT (fake.Adapter's own doc), and
	// production's hubspot mapping lands first_name/last_name, which is
	// what the human wire assembly (compose/overlaywire.go) reads.
	rec := fake.Rec("555000111", map[string]any{"first_name": "Ada", "last_name": "Overlay"})
	rec.ObjectClass = "person"
	rec.OwnerExternalID = "owner-1"
	fakeInc.Seed(overlay.IncumbentClassContacts, rec)
	if err := overlay.Backfill(adminCtx, fakeInc, mirror, overlay.IncumbentClassContacts); err != nil {
		t.Fatalf("backfilling the fake incumbent's contacts: %v", err)
	}

	// --- bullet 1: GET /v1/overlay/sync-status shows fresh + backfill complete ---
	var status syncStatusWire
	if code := e.call(t, "GET", "/v1/overlay/sync-status", nil, nil, &status); code != http.StatusOK {
		t.Fatalf("sync-status = %d", code)
	}
	found := false
	for _, o := range status.Objects {
		if o.Object != "person" {
			continue
		}
		found = true
		if o.State != "fresh" {
			t.Errorf("person sync state = %q, want fresh", o.State)
		}
		if !o.BackfillComplete {
			t.Error("person sync-status reports backfillComplete=false after a converged single-page backfill")
		}
		if o.LastSyncedAt == nil {
			t.Error("person sync-status carries no lastSyncedAt")
		}
	}
	if !found {
		t.Fatalf("sync-status has no person entry: %+v", status)
	}

	// GetOverlayBudget also answers now that the workspace is connected.
	var budget budgetWire
	if code := e.call(t, "GET", "/v1/overlay/budget", nil, nil, &budget); code != http.StatusOK {
		t.Fatalf("budget = %d", code)
	}
	if budget.Band == "" || budget.Window == "" {
		t.Errorf("budget response incomplete: %+v", budget)
	}

	// --- bullet 2: a native-API read of the mirrored person, through
	// the REAL composed dispatcher path (compose.Dispatcher — the exact
	// seam every native GET/read_record call rides in production),
	// carries TrustTier=external + Authoritative=false ---
	dispatcher := compose.NewDispatcher(compose.NewProvider(pool), compose.NewOverlayProvider(pool, compose.NewOverlayMeter()), pool)
	searchRes, err := dispatcher.Search(adminCtx, datasource.SearchQuery{
		EntityTypes: []datasource.EntityType{datasource.EntityPerson}, Limit: 10,
	})
	if err != nil {
		t.Fatalf("dispatched Search for the mapped admin: %v", err)
	}
	if len(searchRes.Records) != 1 {
		t.Fatalf("expected exactly one mirrored person visible to the mapped admin, got %d", len(searchRes.Records))
	}
	contractResults := compose.ContractSearchResults(searchRes)
	tier := contractResults[0].TrustTier
	if tier == nil || *tier != crmcontracts.SearchResultTrustTierExternal {
		t.Fatalf("overlay-served search result TrustTier = %v, want external", tier)
	}
	readRec, err := dispatcher.Read(adminCtx, searchRes.Records[0].Ref)
	if err != nil {
		t.Fatalf("dispatched Read for the mapped admin: %v", err)
	}
	if readRec.Freshness.Authoritative {
		t.Fatal("an overlay-mode workspace's dispatched Read must never claim Authoritative:true")
	}

	// --- bullet 2b: the HUMAN REST surface serves the SAME mirror rows
	// (design.md §4.1 "Overlay does not fork the data API"): list, get,
	// and search answer through the real composed HTTP stack, typed,
	// stamped source=overlay, and search-tagged trust_tier=external ---
	var peoplePage crmcontracts.PersonListResponse
	if code := e.call(t, "GET", "/v1/people", nil, nil, &peoplePage); code != http.StatusOK {
		t.Fatalf("overlay-mode GET /v1/people = %d", code)
	}
	if len(peoplePage.Data) != 1 {
		t.Fatalf("overlay-mode people list = %d rows, want exactly the one mirrored person", len(peoplePage.Data))
	}
	wirePerson := peoplePage.Data[0]
	if wirePerson.FullName != "Ada Overlay" || wirePerson.Source != "overlay" {
		t.Fatalf("mirrored person on the wire = %q/source=%q, want Ada Overlay/source=overlay", wirePerson.FullName, wirePerson.Source)
	}
	var gotPerson crmcontracts.Person
	if code := e.call(t, "GET", "/v1/people/"+wirePerson.Id.String(), nil, nil, &gotPerson); code != http.StatusOK {
		t.Fatalf("overlay-mode GET /v1/people/{id} = %d", code)
	}
	if gotPerson.FullName != "Ada Overlay" {
		t.Fatalf("GET-by-id FullName = %q, want Ada Overlay", gotPerson.FullName)
	}
	var searchPage crmcontracts.SearchResponse
	if code := e.call(t, "GET", "/v1/search?q=Ada", nil, nil, &searchPage); code != http.StatusOK {
		t.Fatalf("overlay-mode GET /v1/search = %d", code)
	}
	if len(searchPage.Data) != 1 {
		t.Fatalf("overlay-mode search = %d hits, want 1", len(searchPage.Data))
	}
	if searchPage.Data[0].TrustTier == nil || *searchPage.Data[0].TrustTier != crmcontracts.SearchResultTrustTierExternal {
		t.Fatalf("overlay search hit TrustTier = %v, want external", searchPage.Data[0].TrustTier)
	}
	if searchPage.Data[0].Title == nil || *searchPage.Data[0].Title != "Ada Overlay" {
		t.Fatalf("overlay search hit Title = %v, want Ada Overlay", searchPage.Data[0].Title)
	}
	// A list dial the mirror cannot answer is refused with 422 naming the
	// parameter — never silently ignored (overlayread.go's own contract).
	if code := e.call(t, "GET", "/v1/people?sort=-created_at", nil, nil, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("overlay-mode sorted people list = %d, want 422 unsupported_in_overlay_mode", code)
	}

	// --- bullet 3: an UNMAPPED user sees ZERO rows (fail-closed
	// visibility — MirrorStore.List answers apperrors.ErrNotFound for a
	// ctx principal with no mirror_user_map row at all, existence-hiding
	// rather than an empty-but-successful page; see visibility.go's own
	// doc on resolveActingMirrorUserID) ---
	unmappedID := seedSecondAppUser(t, e, wsID, "unmapped@overlay.test")
	unmappedCtx := overlayActorCtx(wsID, unmappedID)
	if _, err := dispatcher.Search(unmappedCtx, datasource.SearchQuery{
		EntityTypes: []datasource.EntityType{datasource.EntityPerson}, Limit: 10,
	}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("unmapped user's Search = %v, want apperrors.ErrNotFound (existence-hiding, zero rows)", err)
	}

	// --- bullet 4: DELETE /v1/overlay/connection → mirror empty,
	// tombstone present, connection audit RETAINED + PII-scrubbed ---
	if code := e.call(t, "DELETE", "/v1/overlay/connection", nil, nil, nil); code != http.StatusAccepted {
		t.Fatalf("disconnect overlay = %d, want 202", code)
	}

	var mirrorCount, tombstoneCount int
	if err := e.owner.QueryRow(context.Background(), `SELECT count(*) FROM overlay_mirror WHERE workspace_id = $1`, wsIDStr).Scan(&mirrorCount); err != nil {
		t.Fatalf("counting overlay_mirror rows: %v", err)
	}
	if mirrorCount != 0 {
		t.Errorf("overlay_mirror still has %d rows after disconnect, want 0", mirrorCount)
	}
	if err := e.owner.QueryRow(context.Background(), `SELECT count(*) FROM overlay_tombstone WHERE workspace_id = $1`, wsIDStr).Scan(&tombstoneCount); err != nil {
		t.Fatalf("counting overlay_tombstone rows: %v", err)
	}
	if tombstoneCount == 0 {
		t.Error("overlay_tombstone has no rows after disconnect, want at least one (the purged person)")
	}

	var audit struct {
		Data []struct {
			EntityType string         `json:"entity_type"`
			Action     string         `json:"action"`
			Before     map[string]any `json:"before"`
			After      map[string]any `json:"after"`
		} `json:"data"`
	}
	if code := e.call(t, "GET", "/v1/audit-log?entity_type=incumbent_connection&action=archive", nil, nil, &audit); code != http.StatusOK {
		t.Fatalf("audit log = %d", code)
	}
	if len(audit.Data) != 1 {
		t.Fatalf("expected exactly one retained incumbent_connection archive audit row, got %d", len(audit.Data))
	}
	for _, snapshot := range []map[string]any{audit.Data[0].Before, audit.Data[0].After} {
		for key := range snapshot {
			if key != "incumbent" && key != "region" && key != "status" {
				t.Errorf("connection audit snapshot carries an unexpected field %q — PII/credential leak: %v", key, snapshot)
			}
		}
	}
}
