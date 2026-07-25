// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The write-shadow story (compose/overlaywriteshadow.go), proven over the
// real HTTP surface + a real migrated Postgres: an ordinary REST update or
// archive against a workspace in overlay mode routes THROUGH to the
// incumbent and answers with the re-mirrored row. The overlay-mode write
// guard (compose/overlaywrite.go) admits a mirrored-type write the whole
// way to a handler on the promise that a shadow serves it
// (overlay.SupportsWrite); a supported write with no shadow falls through
// to the native module handler's promoted method instead and commits to
// the empty overlay-mode table — this file is what proves that promise
// holds for every write the guard admits.
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
//
// The body must describe the record as it is AFTER the call: the contract
// defines the archive response as one that "now carries a non-null
// archived_at", and a body reporting the record as live is a write reported
// as not having happened.
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
	if person.ArchivedAt == nil {
		t.Fatal("archived person body carries archived_at: null — the response claims a record the incumbent just archived is still live")
	}

	if _, err := e.fake.Get(context.Background(), "person", "9101"); err == nil {
		t.Fatal("the fake incumbent still holds the archived person — the archive never reached the seam")
	}
	// The contract also promises an archived row stays fetchable by id. It is
	// not, on this path: an incumbent archive stops serving the object, so the
	// mirror purges rather than flags it. Pinned as the KNOWN divergence it is
	// — the upstream question is what archive means when the system of record
	// is an incumbent, and this assertion is what will fail loudly the day
	// that answer lands.
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
	// DELETE /v1/leads/{id} is disqualify_lead (a lifecycle verb, not a
	// seam write overlayWriteVerbs carries), refused on that basis alone —
	// pinned over the real HTTP surface, not just at the guard-unit level
	// (overlaywrite_test.go's "lead disqualify refused in overlay"), so a
	// policy regen or a new overlayWriteVerbs entry sending it to
	// DisqualifyLead's native handler fails a test here too.
	if status := e.call(t, "DELETE", "/v1/leads/"+placeholder, nil, nil, nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("disqualify lead in overlay mode = %d, want 422 unsupported_by_sor", status)
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

// overlayUpdateCase is one type's slice of
// TestOverlayWriteShadowsRoundTripEveryMirroredType's update table: seed a
// record, PATCH one field, and assert that EXACT field changed on the
// response. Asserting the specific field (not just "PATCH answered 200")
// is what a transposed EntityType or a swapped overlayWire* mapper cannot
// survive — a wrong entity type answers 404 (no such row natively, or a
// mismatched mirror lookup), and a wrong mapper answers a response shaped
// for a different entity, missing the field entirely or holding its
// unchanged previous value.
type overlayUpdateCase struct {
	entityType string
	path       string
	externalID string
	seedFields map[string]any
	patch      anyMap
	field      string
	want       string
}

// overlayArchiveCase is the archive-table sibling of overlayUpdateCase:
// seed a record, archive it, and assert the pre-archive snapshot's field
// value on the response, plus that the fake incumbent no longer holds it.
type overlayArchiveCase struct {
	entityType string
	path       string
	externalID string
	seedFields map[string]any
	field      string
	want       string
}

// TestOverlayWriteShadowsRoundTripEveryMirroredType is the behavioral
// counterpart to the fitness test's existence check
// (TestOverlayWriteShadowsCoverEverySupportedWrite, compose/overlaywrite_test.go):
// that test only proves a method with the right NAME exists on Server: it
// cannot catch a shadow that dispatches the WRONG EntityType or wires the
// WRONG overlayWire* mapper (e.g. UpdateLead accidentally built on
// datasource.EntityActivity, or wired to overlayWireActivity) — a
// transposition that would still satisfy the existence check while
// silently reading or writing the wrong mirror row. One update and one
// archive per mirrored type here, each asserting the specific patched
// field actually changed on the response, closes that gap. Deal-update and
// person-archive are exercised in more depth by their own dedicated tests
// above; they are still included here so the table is one complete,
// uniform proof per type rather than five ad hoc ones.
func TestOverlayWriteShadowsRoundTripEveryMirroredType(t *testing.T) {
	updates := []overlayUpdateCase{
		{"person", "/v1/people", "9301", map[string]any{"first_name": "Marie"}, anyMap{"first_name": "Marie2"}, "first_name", "Marie2"},
		{"organization", "/v1/organizations", "9302", map[string]any{"display_name": "Acme Org"}, anyMap{"display_name": "Acme Org 2"}, "display_name", "Acme Org 2"},
		{"deal", "/v1/deals", "9303", map[string]any{"name": "Widget Deal"}, anyMap{"name": "Widget Deal 2"}, "name", "Widget Deal 2"},
		{"lead", "/v1/leads", "9304", map[string]any{"full_name": "Grace Lead"}, anyMap{"full_name": "Grace Lead 2"}, "full_name", "Grace Lead 2"},
		{"activity", "/v1/activities", "9305", map[string]any{"kind": "call", "subject": "Intro Call"}, anyMap{"subject": "Intro Call 2"}, "subject", "Intro Call 2"},
	}
	for _, tc := range updates {
		t.Run("update/"+tc.entityType, func(t *testing.T) {
			e := setupOverlayWrite(t)
			e.seed(t, tc.entityType, tc.externalID, tc.seedFields)
			id := firstListedID(t, e.env, tc.path)

			var body anyMap
			if status := e.call(t, "PATCH", tc.path+"/"+id, tc.patch, nil, &body); status != http.StatusOK {
				t.Fatalf("PATCH %s/%s = %d", tc.path, id, status)
			}
			if got, _ := body[tc.field].(string); got != tc.want {
				t.Fatalf("%s.%s = %q, want %q — wrong EntityType or overlayWire* mapper wired for this shadow", tc.entityType, tc.field, got, tc.want)
			}
		})
	}

	archives := []overlayArchiveCase{
		{"person", "/v1/people", "9311", map[string]any{"first_name": "Isaac"}, "first_name", "Isaac"},
		{"organization", "/v1/organizations", "9312", map[string]any{"display_name": "Beta Org"}, "display_name", "Beta Org"},
		{"deal", "/v1/deals", "9313", map[string]any{"name": "Small Deal"}, "name", "Small Deal"},
	}
	for _, tc := range archives {
		t.Run("archive/"+tc.entityType, func(t *testing.T) {
			e := setupOverlayWrite(t)
			e.seed(t, tc.entityType, tc.externalID, tc.seedFields)
			id := firstListedID(t, e.env, tc.path)

			var body anyMap
			if status := e.call(t, "DELETE", tc.path+"/"+id, nil, nil, &body); status != http.StatusOK {
				t.Fatalf("DELETE %s/%s = %d, want 200", tc.path, id, status)
			}
			if got, _ := body[tc.field].(string); got != tc.want {
				t.Fatalf("%s.%s = %q, want %q — wrong EntityType or overlayWire* mapper wired for this shadow", tc.entityType, tc.field, got, tc.want)
			}
			if _, err := e.fake.Get(context.Background(), tc.entityType, tc.externalID); err == nil {
				t.Fatalf("the fake incumbent still holds the archived %s — the archive never reached the seam", tc.entityType)
			}
		})
	}
}
