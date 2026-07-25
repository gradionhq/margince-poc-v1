// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The write-shadow story (compose/overlaywriteshadow.go), proven over the
// real HTTP surface + a real migrated Postgres: an ordinary REST update or
// archive against a workspace in overlay mode routes THROUGH to the
// incumbent and answers with the re-mirrored row, instead of the Task-3
// guard letting a supported write fall through to the native handler's
// empty overlay-mode table (that window is exactly what this file exists
// to close — see Task 3's own report on the interim risk).
//
// Every test here connects the workspace through compose.WithKeyvault (so
// Connect can seal a credential and the guard sees an active connection),
// then overrides the live-incumbent resolver with compose.
// WithOverlayIncumbentResolver pointing at an overlay/fake.Adapter — no
// mocked provider, no real HubSpot account, no network call (T11): the
// fake stands in for the vaulted hubspot.Adapter WithKeyvault would
// otherwise build from the connection's own region+token.

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/fake"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// overlayWriteEnv is the ready fixture every test in this file starts from:
// a bootstrapped workspace connected to overlay mode over a fake incumbent,
// with the admin mapped to the fake's one seeded owner. That mapping is
// mandatory, not incidental — the mirror's fail-closed visibility deny-join
// hides any row whose owner does not resolve to the acting caller, so
// without it every read-back in this file would fail for a reason that has
// nothing to do with the write-shadow code under test (overlay_e2e_test.go
// establishes the same fixture shape for the read side).
type overlayWriteEnv struct {
	*env
	fake   *fake.Adapter
	mirror *overlay.MirrorStore
	ctx    context.Context // workspace+admin-bound, for direct mirror/fake seeding
}

func setupOverlayWrite(t *testing.T) overlayWriteEnv {
	t.Helper()
	fakeInc := fake.New()
	fakeInc.SeedOwner("owner-1", "owner@overlay.test")
	vault := keyvault.NewMemory()
	e := setupWithOptions(t, compose.WithKeyvault(vault),
		// Applied AFTER WithKeyvault so it wins: WithKeyvault's own
		// SetOverlayIncumbentResolver call would otherwise install the
		// real vaulted resolver last.
		compose.WithOverlayIncumbentResolver(func(context.Context) (overlay.Incumbent, error) { return fakeInc, nil }))
	e.bootstrapWorkspace(t)

	var conn map[string]any
	if status := e.call(t, "POST", "/v1/overlay/connection", anyMap{
		"incumbent": "hubspot", "region": "eu1", "privateAppToken": "fake-token-never-used",
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

	mirror := overlay.NewMirrorStore(e.pool, stubOwnerEmails{})
	actorCtx := overlayActorCtx(wsID, adminID)
	if err := mirror.UpsertUserMap(actorCtx, ids.From[ids.UserKind](adminID), "hubspot", "owner-1", "manual"); err != nil {
		t.Fatalf("mapping the admin to the fake incumbent owner: %v", err)
	}
	return overlayWriteEnv{env: e, fake: fakeInc, mirror: mirror, ctx: actorCtx}
}

// seedModifiedAt is deliberately a fixed instant well before fake.Adapter's
// own write epoch (2026-01-01, adapter.go's writeEpoch): fake.Update always
// tries to land a write at writeEpoch+N seconds first, falling back to
// "stored.ModifiedAt plus one nanosecond" only when that instant is not
// already later — a fallback that a real-clock seed (fake.Rec's own
// time.Now()) hits constantly once wall-clock time passes 2026-01-01, and
// that single nanosecond does not survive the mirror DB's timestamp
// column precision on the round-trip Ingest does, so the write silently
// fails its OWN staleness guard and the re-mirrored row keeps the old
// fields. Seeding safely in fake.Adapter's past avoids the fallback
// branch entirely — this is a fixture-precision concern, not a real-clock
// assertion in the test itself (T11).
var seedModifiedAt = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

// seed lands one record both in the fake incumbent's own write-path store
// (fake.Adapter.Update/Archive look records up by CANONICAL class, exactly
// the string datasource.EntityType carries — "person", "deal", …, per
// fake/adapter.go's own doc on why its write methods are canonical-keyed)
// and in the real mirror DB via Ingest with the IDENTICAL ModifiedAt, so
// the provider's incumbent-first drift check (the mirror row's
// UpdatedAtBaseline vs the fake's own stored clock) never spuriously
// refuses the very first write this test issues.
func (e overlayWriteEnv) seed(t *testing.T, class, externalID string, fields map[string]any) {
	t.Helper()
	rec := overlay.Record{ExternalID: externalID, Fields: fields, ModifiedAt: seedModifiedAt}
	rec.ObjectClass = class
	rec.OwnerExternalID = "owner-1"
	e.fake.Seed(class, rec)
	if err := e.mirror.Ingest(e.ctx, rec); err != nil {
		t.Fatalf("seeding the mirror's %s/%s record: %v", class, externalID, err)
	}
}

// firstListedID resolves the wire UUID a seeded external id landed on —
// the mirror's own numeric<->UUID bridge (overlay/provider.go's
// externalIDToUUID) is unexported, so the honest way to learn it from this
// black-box package is the same one a real client would use: list and read
// the id back off the wire.
func firstListedID(t *testing.T, e *env, path string) string {
	t.Helper()
	var page struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if status := e.call(t, "GET", path, nil, nil, &page); status != http.StatusOK {
		t.Fatalf("GET %s = %d", path, status)
	}
	if len(page.Data) != 1 {
		t.Fatalf("GET %s returned %d rows, want exactly 1: %+v", path, len(page.Data), page.Data)
	}
	return page.Data[0].ID
}

// An ordinary REST update on a mirrored deal writes THROUGH to the
// incumbent and answers with the re-mirrored row, instead of being refused
// (or, worse, silently committing to the empty native deal table).
func TestOverlayUpdateDealWritesBackAndReturnsTheMirroredRow(t *testing.T) {
	e := setupOverlayWrite(t)
	e.seed(t, "deal", "9001", map[string]any{"name": "Acme Renewal", "currency": "USD"})
	id := firstListedID(t, e.env, "/v1/deals")

	var deal crmcontracts.Deal
	if status := e.call(t, "PATCH", "/v1/deals/"+id, anyMap{"name": "Acme Renewal — Q3"}, nil, &deal); status != http.StatusOK {
		t.Fatalf("PATCH /v1/deals/%s = %d", id, status)
	}
	if deal.Name != "Acme Renewal — Q3" {
		t.Fatalf("updated deal Name = %q, want %q", deal.Name, "Acme Renewal — Q3")
	}

	fakeRec, err := e.fake.Get(context.Background(), "deal", "9001")
	if err != nil {
		t.Fatalf("reading the fake incumbent's deal record: %v", err)
	}
	if fakeRec.Fields["name"] != "Acme Renewal — Q3" {
		t.Fatalf("the fake incumbent's own record.name = %v, want %q — the write never reached the seam", fakeRec.Fields["name"], "Acme Renewal — Q3")
	}
}

// Archive reaches the incumbent for the types it supports: the response is
// the contract's own 200-with-body (never a bare 204 for a domain row,
// matching every native ArchivePerson/ArchiveOrganization/ArchiveDeal), and
// the incumbent — not just the mirror — loses the record.
func TestOverlayArchivePersonWritesBack(t *testing.T) {
	e := setupOverlayWrite(t)
	e.seed(t, "person", "9101", map[string]any{"first_name": "Ada", "last_name": "Overlay"})
	id := firstListedID(t, e.env, "/v1/people")

	var person crmcontracts.Person
	if status := e.call(t, "DELETE", "/v1/people/"+id, nil, nil, &person); status != http.StatusOK {
		t.Fatalf("DELETE /v1/people/%s = %d, want 200 (architecture/11 §8: never a bare 204 for a domain row)", id, status)
	}
	if person.FullName != "Ada Overlay" {
		t.Fatalf("archived person body FullName = %q, want the pre-archive %q", person.FullName, "Ada Overlay")
	}

	if _, err := e.fake.Get(context.Background(), "person", "9101"); err == nil {
		t.Fatal("the fake incumbent still holds the archived person — the archive never reached the seam")
	}
	if status := e.call(t, "GET", "/v1/people/"+id, nil, nil, nil); status != http.StatusNotFound {
		t.Fatalf("GET the archived person = %d, want 404 (the mirror row is purged by the archive itself)", status)
	}
}

// The verbs the provider declares unsupported still answer 422, and never
// reach the native handler — proven by the native table holding nothing
// for the workspace afterward, not just by the status code.
func TestOverlayUnsupportedWritesStillRefused(t *testing.T) {
	e := setupOverlayWrite(t)
	placeholder := ids.NewV7().String()

	if status := e.call(t, "POST", "/v1/deals/"+placeholder+"/advance",
		anyMap{"to_stage_id": placeholder}, nil, nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("advance_deal in overlay mode = %d, want 422 unsupported_by_sor", status)
	}
	if status := e.call(t, "POST", "/v1/people",
		anyMap{"full_name": "Should Never Land"}, nil, nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("create person in overlay mode = %d, want 422 unsupported_by_sor", status)
	}
	if status := e.call(t, "DELETE", "/v1/activities/"+placeholder, nil, nil, nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("archive activity in overlay mode = %d, want 422 unsupported_by_sor", status)
	}

	var personCount int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM person WHERE workspace_id = (SELECT id FROM workspace WHERE slug = $1)`, e.slug,
	).Scan(&personCount); err != nil {
		t.Fatalf("counting native person rows: %v", err)
	}
	if personCount != 0 {
		t.Fatalf("native person table holds %d rows after a refused create — the guard let it through", personCount)
	}
}

// A native-only entity (never mirrored — overlaywrite.go's own
// overlayMirroredTypes gate) writes normally while the workspace is in
// overlay mode: tags live in their own table, live in overlay mode too, and
// carry no seam verb at all.
func TestOverlayTagWriteStillWorks(t *testing.T) {
	e := setupOverlayWrite(t)

	var tag crmcontracts.Tag
	if status := e.call(t, "POST", "/v1/tags", anyMap{"name": "hot-lead"}, nil, &tag); status != http.StatusCreated {
		t.Fatalf("POST /v1/tags in overlay mode = %d, want 201", status)
	}
	if tag.Name != "hot-lead" {
		t.Fatalf("created tag Name = %q, want hot-lead", tag.Name)
	}
}

// An If-Match header on an overlay update is not a spurious version-skew
// refusal: a mirror row carries no version, and the incumbent-first
// stored-baseline drift check inside the seam is the only concurrency guard
// on this path.
func TestOverlayUpdateIgnoresIfMatch(t *testing.T) {
	e := setupOverlayWrite(t)
	e.seed(t, "person", "9102", map[string]any{"first_name": "Grace", "last_name": "Overlay"})
	id := firstListedID(t, e.env, "/v1/people")

	var person crmcontracts.Person
	status := e.call(t, "PATCH", "/v1/people/"+id, anyMap{"first_name": "Grace2"},
		map[string]string{"If-Match": "999"}, &person)
	if status != http.StatusOK {
		t.Fatalf("PATCH with a stale If-Match = %d, want 200 (If-Match is not evaluated on the overlay path)", status)
	}
	if person.FirstName == nil || *person.FirstName != "Grace2" {
		t.Fatalf("FirstName = %v, want Grace2 — the update itself must still have applied", person.FirstName)
	}
}

// The write mapping carries only the fields it declares writable — here,
// observed at the overlay REST surface's OWN honest limit: owner_id is a
// valid, contract-writable UpdatePersonRequest field, but overlayWirePerson
// (compose/overlaywire.go) never wires owner_id onto the Person response in
// overlay mode AT ALL, write or no write. A patch touching it therefore
// always answers the SAME (absent) owner on the wire, while a
// simultaneously-patched, wire-mapped field (first_name) visibly changes —
// pinned deliberately: this is the honest limit of overlay write-back
// exactly where the response is observed, not a claim about what the fake
// incumbent itself retained (the fake has no field-level write mapping;
// production's real hubspot.mapWrite is what actually drops owner_id
// before it ever reaches HubSpot, and that is unit-tested at the hubspot
// package level, not here).
func TestOverlayUpdateDropsUnmappedFields(t *testing.T) {
	e := setupOverlayWrite(t)
	e.seed(t, "person", "9103", map[string]any{"first_name": "Rosalind", "last_name": "Overlay"})
	id := firstListedID(t, e.env, "/v1/people")

	var person crmcontracts.Person
	status := e.call(t, "PATCH", "/v1/people/"+id,
		anyMap{"first_name": "Rosalind2", "owner_id": ids.NewV7().String()}, nil, &person)
	if status != http.StatusOK {
		t.Fatalf("PATCH with an owner_id field = %d, want 200", status)
	}
	if person.FirstName == nil || *person.FirstName != "Rosalind2" {
		t.Fatalf("FirstName = %v, want Rosalind2 — the mapped field must still change", person.FirstName)
	}
	if person.OwnerId != nil {
		t.Fatalf("OwnerId = %v, want nil — overlay mode never wires owner_id onto the Person response", *person.OwnerId)
	}
}
