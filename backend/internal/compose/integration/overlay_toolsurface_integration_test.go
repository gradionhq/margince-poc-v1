// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Bounded equivalence on the AGENT surface (AC-OV-2 / ADR-0018): every tool
// the production registry advertises must, for an overlay-mode workspace,
// either behave as it does natively or answer the DECLARED
// unsupported-by-SoR sentinel. What it must never do is query the native
// tables — which hold none of that workspace's records — and present the
// resulting empty answer as a successful result. "No deals are slipping" and
// "this report returned zero rows" are the exact failure this criterion
// exists to forbid, because neither is distinguishable from a true answer.
//
// The unit specs for the guards themselves live in
// compose/nativeonlytools_test.go. This suite asserts the guards are
// actually WIRED, by driving compose.NewRegistry — the same constructor
// cmd/mcp and the api role use — against a real overlay-mode workspace. A
// future edit that unwires a guard keeps those unit specs green and turns
// this one red.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// nativeOnlyAgentTools are the tools whose only implementation reads the
// native domain tables: the compiled report engine, the two retrieval-grounded
// context intents, the pipeline-risk scan and its draft sibling, the two
// relationship-graph reads, the pipeline configuration read — and one WRITE,
// disqualify_lead, whose tool calls the people store directly and so misses the
// REST-only write guard that refuses the same verb for a mirrored type. None
// has a mirror projection to serve, so each owes an honest refusal in overlay
// mode.
// The args only have to be well-formed — the refusal must land before any
// record lookup, so a not-found in place of the sentinel means the guard runs
// late.
//
// Every `nativeOnly…` guard in compose/nativeonlytools.go must name its tool
// here, and every tool here must be named by exactly one guard —
// TestEveryNativeOnlyGuardNamesAToolThePinCovers holds the two in step. So a
// newly guarded tool is enrolled by the gate rather than by being remembered,
// which is what "which tools read native tables is not visible from a tool spec"
// otherwise costs.
func nativeOnlyAgentTools(anchor ids.UUID) map[string]string {
	return map[string]string{
		"run_report":               `{"report":"deals-by-stage"}`,
		"catch_me_up_on":           fmt.Sprintf(`{"record_type":"person","record_id":%q}`, anchor),
		"prep_for_meeting":         fmt.Sprintf(`{"record_type":"person","record_id":%q}`, anchor),
		"whats_slipping_this_week": `{}`,
		"draft_follow_ups_for":     `{"segment":"slipping"}`,
		"intro_path_to":            fmt.Sprintf(`{"organization_id":%q}`, anchor),
		"at_risk_relationships":    `{}`,
		"list_pipelines":           `{}`,
		"disqualify_lead":          fmt.Sprintf(`{"lead_id":%q}`, anchor),
	}
}

// nativeToolReaderPerms is overlayReaderPerms plus the `pipeline` object.
// list_pipelines rides the deals module's own config read, which is RBAC-gated
// on `pipeline` — so a reader without that grant is refused for a reason that
// has nothing to do with system-of-record mode, and the native half of this
// suite would be asserting the wrong thing.
func nativeToolReaderPerms() principal.Permissions {
	perms := overlayReaderPerms
	objects := make(map[string]principal.ObjectGrant, len(perms.Objects)+1)
	for object, grant := range perms.Objects {
		objects[object] = grant
	}
	objects["pipeline"] = principal.ObjectGrant{Read: true}
	perms.Objects = objects
	return perms
}

func TestOverlayAgentToolsRefuseRatherThanAnswerFromNativeTables(t *testing.T) {
	e := Setup(t)
	ws, user := seedOverlayModeWorkspace(t)
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})
	// Every object grant a guarded read could need, so an unwired guard fails
	// with the native-table answer this suite exists to forbid rather than with
	// "permission denied" — which would pass the assertion for a reason that has
	// nothing to do with system-of-record mode.
	ctx := overlayActorCtxWith(ws, user, nativeToolReaderPerms())

	for name, args := range nativeOnlyAgentTools(ids.NewV7()) {
		t.Run(name, func(t *testing.T) {
			if _, ok := registry.Spec(name); !ok {
				t.Fatalf("%s is not registered — this pin no longer covers it", name)
			}
			out, err := registry.Invoke(ctx, name, json.RawMessage(args))
			if err == nil {
				t.Fatalf("%s answered %s for an overlay workspace — a native-table result presented as an answer", name, out)
			}
			if !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
				t.Fatalf("%s err = %v, want ErrUnsupportedBySoR (a declared refusal, not an incidental failure)", name, err)
			}
		})
	}
}

// A write into the incumbent is outbound egress (AC-OV-5), so an agent must
// not be able to place content in the customer's CRM on its own authority.
// Per-field human-edit precedence cannot govern it — a mirrored record has
// no human audit history, so nothing ever reads as human-owned and every
// patch would pass through. The assertion that matters most is the negative
// one: the mirror row is untouched, so nothing was pushed.
func TestOverlayUpdateRecordRefusesAnAgentRatherThanWritingBack(t *testing.T) {
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
		Fields:          map[string]any{"firstname": "Ada", "lastname": "Overlay", "jobtitle": "Analyst"},
		ModifiedAt:      time.Now().UTC(),
		OwnerExternalID: "owner-1",
	}); err != nil {
		t.Fatalf("ingesting the overlay fixture record: %v", err)
	}

	// The tool addresses the record by the id its own reads hand out, so
	// resolve it the way an agent would rather than minting one.
	d := compose.NewDispatcher(compose.NewProvider(e.Pool), compose.NewOverlayProvider(e.Pool, overlaybudget.New(nil, nil), nil), e.Pool)
	found, err := d.Search(ctx, datasource.SearchQuery{
		EntityTypes: []datasource.EntityType{datasource.EntityPerson},
		Limit:       10,
	})
	if err != nil || len(found.Records) != 1 {
		t.Fatalf("resolving the mirrored person: err=%v records=%d", err, len(found.Records))
	}
	target := found.Records[0].Ref.ID

	registry := compose.NewRegistry(e.Pool, compose.SendPath{})
	args := fmt.Sprintf(`{"record_type":"person","id":%q,"fields":{"title":"Principal Analyst"}}`, target)

	_, err = registry.Invoke(agentActorCtx(overlayWS, actorID), "update_record", json.RawMessage(args))

	if !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Fatalf("update_record err = %v, want ErrUnsupportedBySoR — an ungoverned agent write reached the incumbent", err)
	}
	// No approval row is minted: staging one would name a release path that
	// dead-ends, since the decidability probe and the redemption version pin
	// both read tables a mirrored record has no row in.
	var staged int
	if err := OwnerConn(t).QueryRow(ctx,
		`SELECT count(*) FROM approval WHERE workspace_id = $1`, overlayWS).Scan(&staged); err != nil {
		t.Fatalf("counting staged approvals: %v", err)
	}
	if staged != 0 {
		t.Errorf("staged approvals = %d, want 0 — an unreleasable authority object was created", staged)
	}

	// And the mirror still holds the incumbent's own value: nothing was
	// pushed outward.
	unchanged, err := d.Read(ctx, found.Records[0].Ref)
	if err != nil {
		t.Fatalf("re-reading the mirrored person: %v", err)
	}
	if !bytes.Contains(unchanged.Fields, []byte("Analyst")) || bytes.Contains(unchanged.Fields, []byte("Principal Analyst")) {
		t.Errorf("mirror fields = %s — the unapproved patch was applied", unchanged.Fields)
	}
}

// The seam backstop, proven where it actually sits. update_record is not the
// only way an agent reaches a write: the REST twin (PATCH /v1/people/{id}
// under an agent passport) runs the same per-field split, which finds nothing
// human-owned on a mirrored record, and qualify_lead writes through the
// provider with no gate of its own. Both — and any write tool added later —
// funnel through Dispatcher's update/archive dispatch, so that is where the
// refusal has to live. Testing it here covers every one of those routes at
// once rather than one route and a hope.
func TestOverlayWritesRefuseAnUnreleasedAgentAtTheSeam(t *testing.T) {
	e := Setup(t)
	ws, actorID := seedOverlayModeWorkspace(t)
	ctx := overlayActorCtx(ws, actorID)

	mirror := overlay.NewMirrorStore(e.Pool, stubOwnerEmails{})
	if err := mirror.UpsertUserMap(ctx, ids.From[ids.UserKind](actorID), "hubspot", "owner-1", "manual"); err != nil {
		t.Fatalf("mapping the acting user to owner-1: %v", err)
	}
	if err := mirror.Ingest(ctx, overlay.Record{
		ObjectClass: "person", ExternalID: "100214862044",
		Fields:     map[string]any{"firstname": "Seam", "lastname": "Backstop"},
		ModifiedAt: time.Now().UTC(), OwnerExternalID: "owner-1",
	}); err != nil {
		t.Fatalf("ingesting the overlay fixture record: %v", err)
	}

	d := compose.NewDispatcher(compose.NewProvider(e.Pool), compose.NewOverlayProvider(e.Pool, overlaybudget.New(nil, nil), nil), e.Pool)
	found, err := d.Search(ctx, datasource.SearchQuery{
		EntityTypes: []datasource.EntityType{datasource.EntityPerson}, Limit: 10,
	})
	if err != nil || len(found.Records) != 1 {
		t.Fatalf("resolving the mirrored person: err=%v records=%d", err, len(found.Records))
	}
	ref := found.Records[0].Ref
	patch := datasource.UpdateInput{Ref: ref, Patch: json.RawMessage(`{"title":"Principal"}`), Source: "tool"}

	// An agent with no released approval is refused before the incumbent is
	// touched, whatever route brought it here.
	if _, err := d.Update(agentActorCtx(ws, actorID), patch); !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Errorf("agent Update err = %v, want ErrUnsupportedBySoR", err)
	}
	if _, err := d.Archive(agentActorCtx(ws, actorID), ref); !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Errorf("agent Archive err = %v, want ErrUnsupportedBySoR", err)
	}

	// A human in their own seat is object RBAC's question, not this gate's:
	// the write proceeds past the backstop (and then fails for its own
	// reasons in this fixture, which has no incumbent write resolver).
	if _, err := d.Update(ctx, patch); errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Errorf("human Update was gated by the agent egress backstop: %v", err)
	}
}

// agentActorCtx is overlayActorCtx as a PASSPORT principal — the type every
// agent surface authenticates as, and the only one the egress backstop gates.
func agentActorCtx(ws, user ids.UUID) context.Context {
	perms := overlayReaderPerms
	perms.Objects = map[string]principal.ObjectGrant{
		"person": {Read: true, Update: true, Delete: true},
	}
	ctx := principal.WithWorkspaceID(context.Background(), ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:" + user.String(),
		OnBehalfOf: user, UserID: user, PassportID: ids.NewV7(),
		Scopes:      principal.NewScopeSet(principal.ScopeWrite),
		Permissions: perms,
	})
}

// The egress gate is a mutation boundary, so it must resolve the mode
// UNCACHED. A workspace that connects an overlay invalidates only the
// process that committed the flip, so another api replica — or the same one
// inside the cache TTL — can still hold 'native'. If the gate trusted that,
// it would auto-execute while Dispatcher.Update (which does read fresh)
// routed the very same patch to the incumbent: the write reaches a third
// party's CRM with the confirm-first gate believing it never left our
// boundary. This drives that exact skew: warm the cache as native, flip
// x_sor_mode underneath with no invalidation, and assert the gate still
// holds.
func TestOverlayUpdateRecordEgressGateIgnoresAStaleNativeModeCache(t *testing.T) {
	e := Setup(t)
	ws, actorID := seedNativeModeWorkspaceForFlip(t)
	ctx := overlayActorCtx(ws, actorID)
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})

	// Warm this process's mode cache while the workspace is still native, by
	// making the same read an ordinary request would.
	if _, err := registry.Invoke(ctx, "search_records",
		json.RawMessage(`{"q":"anything","record_type":"person"}`)); err != nil {
		t.Fatalf("warming the mode cache with a native-mode read: %v", err)
	}

	// Flip to overlay the way a connect does, but WITHOUT Invalidate — the
	// state a second replica is in for the rest of the TTL.
	if _, err := OwnerConn(t).Exec(ctx,
		`UPDATE workspace SET x_sor_mode = 'overlay', x_incumbent = 'hubspot' WHERE id = $1`, ws); err != nil {
		t.Fatalf("flipping the workspace to overlay mode: %v", err)
	}

	mirror := overlay.NewMirrorStore(e.Pool, stubOwnerEmails{})
	if err := mirror.UpsertUserMap(ctx, ids.From[ids.UserKind](actorID), "hubspot", "owner-1", "manual"); err != nil {
		t.Fatalf("mapping the acting user to owner-1: %v", err)
	}
	if err := mirror.Ingest(ctx, overlay.Record{
		ObjectClass:     "person",
		ExternalID:      "100214862043",
		Fields:          map[string]any{"firstname": "Grace", "lastname": "Stale", "jobtitle": "Analyst"},
		ModifiedAt:      time.Now().UTC(),
		OwnerExternalID: "owner-1",
	}); err != nil {
		t.Fatalf("ingesting the overlay fixture record: %v", err)
	}

	d := compose.NewDispatcher(compose.NewProvider(e.Pool), compose.NewOverlayProvider(e.Pool, overlaybudget.New(nil, nil), nil), e.Pool)
	found, err := d.Search(ctx, datasource.SearchQuery{
		EntityTypes: []datasource.EntityType{datasource.EntityPerson},
		Limit:       10,
	})
	if err != nil || len(found.Records) != 1 {
		t.Fatalf("resolving the mirrored person: err=%v records=%d", err, len(found.Records))
	}

	args := fmt.Sprintf(`{"record_type":"person","id":%q,"fields":{"title":"Principal Analyst"}}`, found.Records[0].Ref.ID)
	_, err = registry.Invoke(agentActorCtx(ws, actorID), "update_record", json.RawMessage(args))

	if !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Fatalf("update_record err = %v, want ErrUnsupportedBySoR — the egress gate trusted a stale native mode cache", err)
	}
}

// seedNativeModeWorkspaceForFlip mints a workspace that starts NATIVE (so a
// read can warm the mode cache with that answer) plus one human user. Its
// overlay sibling, seedOverlayModeWorkspace, cannot serve here: the
// x_overlay_iff_incumbent CHECK makes it overlay from creation, leaving no
// native window to cache.
func seedNativeModeWorkspaceForFlip(t *testing.T) (ws, user ids.UUID) {
	t.Helper()
	owner := OwnerConn(t)
	ws = ids.NewV7()
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Pre-flip', $2, 'EUR')`,
		ws, "preflip-"+ws.String()); err != nil {
		t.Fatalf("seeding the native-mode workspace: %v", err)
	}
	user = ids.NewV7()
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'Pre-flip User')`,
		user, ws, "preflip-"+user.String()+"@overlay.test"); err != nil {
		t.Fatalf("seeding the native-mode workspace's user: %v", err)
	}
	return ws, user
}

// The guard is a mode gate, not a kill switch: the same registry must still
// serve a native workspace. Such a call may fail on its own terms (bad
// arguments, a missing anchor record) but never with the unsupported-by-SoR
// sentinel, which means "this system of record cannot do this at all".
func TestNativeAgentToolsAreNotRefusedByTheSoRModeGuard(t *testing.T) {
	e := Setup(t)
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})
	ctx := e.As(e.Rep1, nil, nativeToolReaderPerms())

	// The anchored intents may honestly answer not-found (their anchor id is
	// minted, not seeded); the ones that take no record must SUCCEED, so a
	// native report path that broke outright cannot hide behind "well, it
	// didn't say unsupported_by_sor".
	anchored := map[string]bool{
		"catch_me_up_on": true, "prep_for_meeting": true, "intro_path_to": true,
	}

	for name, args := range nativeOnlyAgentTools(ids.NewV7()) {
		t.Run(name, func(t *testing.T) {
			_, err := registry.Invoke(ctx, name, json.RawMessage(args))
			if errors.Is(err, apperrors.ErrUnsupportedBySoR) {
				t.Fatalf("%s refused a NATIVE workspace with unsupported_by_sor — the mode guard is inverted", name)
			}
			switch {
			case anchored[name] && err != nil && !errors.Is(err, apperrors.ErrNotFound):
				t.Fatalf("%s err = %v, want nil or ErrNotFound", name, err)
			case !anchored[name] && err != nil:
				t.Fatalf("%s err = %v, want nil — a native workspace must actually be served", name, err)
			}
		})
	}
}
