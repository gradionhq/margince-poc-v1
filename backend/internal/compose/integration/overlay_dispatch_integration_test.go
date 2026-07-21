// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Per-workspace SoR dispatch: compose.Dispatcher is the ONE
// datasource.SystemOfRecordProvider every seam-injection point (the MCP
// registry, the workflow engine, the ADR-0055 admission layer) now binds
// to — it must route a native-mode workspace's calls to the native
// composite Provider (Authoritative:true) and an overlay-mode
// workspace's calls to the overlay.Provider (Authoritative:false),
// chosen per call from the context's workspace, never fixed at
// construction time. This needs a real, migrated Postgres (RLS +
// workspace.x_sor_mode + the mirror_visibility deny-join), so it is
// gated behind //go:build integration like the rest of this package.

import (
	"context"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// TestDispatcherRoutesNativeWorkspaceReadsToTheNativeProvider is the
// native-mode half of the AC: a workspace that never flipped x_sor_mode
// (the harness's default fixture) dispatches Read to the native
// composite Provider, whose Freshness is trivially authoritative.
func TestDispatcherRoutesNativeWorkspaceReadsToTheNativeProvider(t *testing.T) {
	e := Setup(t)
	personID := e.SeedPerson(t, "Ada Native", nil)

	d := compose.NewDispatcher(compose.NewProvider(e.Pool), compose.NewOverlayProvider(e.Pool, compose.NewOverlayMeter(), nil), e.Pool)

	rec, err := d.Read(e.Admin(), datasource.EntityRef{Type: datasource.EntityPerson, ID: personID})
	if err != nil {
		t.Fatalf("dispatched Read for a native-mode workspace: %v", err)
	}
	if !rec.Freshness.Authoritative {
		t.Fatal("a native-mode workspace's dispatched Read must be Authoritative:true")
	}
}

// TestDispatcherRoutesOverlayWorkspaceReadsToTheOverlayProvider is the
// overlay-mode half: a workspace with x_sor_mode='overlay' dispatches
// Read/Search to overlay.Provider, which serves the mirror
// (Authoritative:false, DS-AC-7) — and the contract-assembly helper
// tags that Search result with the T2 external trust tier (design.md
// §4.6).
func TestDispatcherRoutesOverlayWorkspaceReadsToTheOverlayProvider(t *testing.T) {
	e := Setup(t)
	overlayWS, actorID := seedOverlayModeWorkspace(t)
	ctx := overlayActorCtx(overlayWS, actorID)

	mirror := overlay.NewMirrorStore(e.Pool, stubOwnerEmails{})
	if err := mirror.UpsertUserMap(ctx, ids.From[ids.UserKind](actorID), "hubspot", "owner-1", "manual"); err != nil {
		t.Fatalf("mapping the acting user to owner-1: %v", err)
	}
	if err := mirror.Ingest(ctx, overlay.Record{
		ObjectClass:     "person",
		ExternalID:      "100214862042",
		Fields:          map[string]any{"firstname": "Ada Overlay"},
		ModifiedAt:      time.Now().UTC(),
		OwnerExternalID: "owner-1",
	}); err != nil {
		t.Fatalf("ingesting the overlay fixture record: %v", err)
	}

	d := compose.NewDispatcher(compose.NewProvider(e.Pool), compose.NewOverlayProvider(e.Pool, compose.NewOverlayMeter(), nil), e.Pool)

	searchRes, err := d.Search(ctx, datasource.SearchQuery{
		EntityTypes: []datasource.EntityType{datasource.EntityPerson},
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("dispatched Search for an overlay-mode workspace: %v", err)
	}
	if len(searchRes.Records) != 1 {
		t.Fatalf("expected exactly one mirrored record, got %d", len(searchRes.Records))
	}
	if searchRes.Records[0].Freshness.Authoritative {
		t.Fatal("an overlay-mode workspace's dispatched Search result must never claim Authoritative:true")
	}

	readRec, err := d.Read(ctx, searchRes.Records[0].Ref)
	if err != nil {
		t.Fatalf("dispatched Read for an overlay-mode workspace: %v", err)
	}
	if readRec.Freshness.Authoritative {
		t.Fatal("an overlay-mode workspace's dispatched Read must never claim Authoritative:true")
	}

	contractResults := compose.ContractSearchResults(searchRes)
	if len(contractResults) != 1 {
		t.Fatalf("expected exactly one contract search result, got %d", len(contractResults))
	}
	tier := contractResults[0].TrustTier
	if tier == nil || *tier != crmcontracts.SearchResultTrustTierExternal {
		t.Fatalf("overlay-served contract SearchResult must carry TrustTier=external, got %v", tier)
	}
}

// seedOverlayModeWorkspace mints a fresh workspace whose x_sor_mode is
// 'overlay' from creation (the x_overlay_iff_incumbent CHECK requires
// x_incumbent set in the same statement) plus one human app_user, via
// the owner connection — the same "direct SQL, owner role bypasses RLS"
// pattern SeedRow uses elsewhere in this harness. It opens its own
// owner connection (via OwnerConn) rather than reusing the caller's
// Env, since this workspace is intentionally a SECOND, independent
// tenant from the harness's own default fixture.
func seedOverlayModeWorkspace(t *testing.T) (ws, user ids.UUID) {
	t.Helper()
	owner := OwnerConn(t)
	ws = ids.NewV7()
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO workspace (id, name, slug, base_currency, x_sor_mode, x_incumbent)
		 VALUES ($1, 'Overlay', $2, 'EUR', 'overlay', 'hubspot')`,
		ws, "overlay-"+ws.String()); err != nil {
		t.Fatalf("seeding the overlay-mode workspace: %v", err)
	}
	user = ids.NewV7()
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'Overlay User')`,
		user, ws, "overlay-"+user.String()+"@overlay.test"); err != nil {
		t.Fatalf("seeding the overlay-mode workspace's user: %v", err)
	}
	return ws, user
}

// overlayActorCtx binds a workspace+actor context for user in ws — the
// mirror read path (MirrorStore.Get/List) gates on principal.Actor's
// UserID via mirror_user_map, not on object-RBAC permissions, so no
// Permissions grant is needed here.
// overlayReaderPerms is the least-privilege grant an overlay reader needs:
// Read on every mirrored entity type and nothing else (no CRUD). The
// overlay Provider object-gates its reads like the native stores, so the
// object gate must pass for the row-scope (visibility) assertions to be the
// ones that actually run. RowScope is All because overlay ROW visibility is
// the store's mirror_visibility deny-join (HubSpot-owner mapping), not the
// RBAC owner predicate — an unmapped actor still sees zero rows despite
// RowScopeAll. (ReadOnlyPerms would under-grant here: it omits
// organization/lead/activity, which these overlay tests also read.)
var overlayReaderPerms = principal.Permissions{
	RoleKeys: []string{"read_only"},
	Objects: map[string]principal.ObjectGrant{
		"person":       {Read: true},
		"organization": {Read: true},
		"deal":         {Read: true},
		"lead":         {Read: true},
		"activity":     {Read: true},
	},
	RowScope: principal.RowScopeAll,
}

func overlayActorCtx(ws, user ids.UUID) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(), UserID: user,
		Permissions: overlayReaderPerms,
	})
}

// stubOwnerEmails is a no-op overlay.OwnerEmailResolver: this suite's
// mirror_user_map row uses match_source="manual" (the human-override
// path that never calls OwnerEmail) and Ingest's own revalidation
// treats a resolution failure as "no email" and fails closed rather
// than erroring — so a fixed empty answer is honest enough for this
// suite without needing a real HubSpot connection.
type stubOwnerEmails struct{}

func (stubOwnerEmails) OwnerEmail(_ context.Context, _ string) (string, error) {
	return "", nil
}
