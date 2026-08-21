// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The two claims the archive resolver makes about the world outside its own
// package, against a real database.
//
// The first is a PREDICTION across a DAG boundary: agents.servedByTheRecordSeam
// decides from datasource.EntityTypes() which archives the seam can be asked
// about, and the thing that actually answers is Provider.Read's switch, one
// layer up where the module lives. A type added to the vocabulary with no arm
// in that switch would make every archive of it fault at staging, with no unit
// test anywhere able to see it — the two halves are in packages that cannot
// import each other.
//
// The second is the refusal this seam added to the REST door. The unit tests
// prove the door asks the resolver; they cannot prove that a row the caller
// may not see comes back as not-found, because they supply the answer they are
// checking for. Only RLS and the store's row-scope clauses can say that.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// Provider.Read serves the seam's vocabulary and nothing outside it.
//
// Both directions matter and neither is decidable from one side. A declared
// entity type with no arm in the switch answers "not served here" for a record
// that exists, so the resolver would refuse an archive it is meant to guard. A
// type the switch grew quietly without joining the vocabulary would be guarded
// by nothing, because the resolver would never ask about it.
//
// The subject set is DERIVED: the vocabulary from datasource.EntityTypes(), the
// outside set from the record types the generated agent policy actually names.
// A contract that adds an archivable type is covered here without anybody
// extending a list.
func TestTheRecordProviderServesExactlyTheSeamVocabulary(t *testing.T) {
	e := integration.Setup(t)
	as := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms)
	native := NewProvider(e.Pool)

	if len(datasource.EntityTypes()) == 0 {
		t.Fatal("the seam declares no entity type at all — the half of this gate that covers the served " +
			"types is asserting nothing")
	}
	for _, entity := range datasource.EntityTypes() {
		// A random id: the answer under test is whether this provider ROUTES
		// the type, and a miss says routed-and-absent as clearly as a hit says
		// routed-and-present.
		_, err := native.Read(as, datasource.EntityRef{Type: entity, ID: ids.NewV7()})
		var unsupported *datasource.UnsupportedEntityError
		if errors.As(err, &unsupported) {
			t.Errorf("the provider does not serve %q, which the seam's own vocabulary declares — every "+
				"governed archive of one faults at staging, and agents.servedByTheRecordSeam cannot see it "+
				"from the other side of the DAG", entity)
		}
	}

	outside := 0
	for _, recordType := range archivableRecordTypes() {
		if isSeamEntity(recordType) {
			continue
		}
		outside++
		_, err := native.Read(as, datasource.EntityRef{Type: datasource.EntityType(recordType), ID: ids.NewV7()})
		var unsupported *datasource.UnsupportedEntityError
		if !errors.As(err, &unsupported) {
			t.Errorf("the provider serves %q, which is outside the seam's vocabulary — the resolver stands "+
				"its guards down for that type, so a record the seam CAN reach would be staged unguarded",
				recordType)
		}
	}
	if outside == 0 {
		t.Fatal("no archivable record type sits outside the seam vocabulary — the half of this gate that " +
			"covers the stood-down types is asserting nothing")
	}
}

// archivableRecordTypes is every record type an agent-reachable archive names,
// taken from the generated policy rather than from a list.
func archivableRecordTypes() []string {
	types := make([]string, 0, len(agentPolicies))
	for _, pol := range agentPolicies {
		if pol.Access == accessTool && pol.Tool == "archive_record" && pol.RecordType != "" {
			types = append(types, string(pol.RecordType))
		}
	}
	return types
}

// isSeamEntity mirrors the question agents.servedByTheRecordSeam answers, from
// the same source: the seam's own declared vocabulary.
func isSeamEntity(recordType string) bool {
	for _, entity := range datasource.EntityTypes() {
		if string(entity) == recordType {
			return true
		}
	}
	return false
}

// An archive of a row the agent's own row scope hides is refused before
// anything is staged, and nothing is staged.
//
// Through the REAL provider and the REAL approvals engine: the refusal under
// test is the store's row-scope clause answering not-found, and a stub that
// returns ErrNotFound proves only that the door forwards whatever it is handed.
// The person is seeded by the real writer under a rep whose team the agent's
// granting human is not in, so nothing about the fixture is hand-made.
func TestAnArchiveOfARowOutsideTheAgentsScopeStagesNothing(t *testing.T) {
	e := integration.Setup(t)
	native := NewProvider(e.Pool)
	staging := approvalsAdapter{svc: approvals.NewService(e.DB())}

	// Rep3 sits in Team2; the agent below acts for Rep1, in Team1. A person is
	// readable by every seat of the workspace whoever owns it, so ownership alone
	// hides nothing — the row is made CAPTURE-PRIVATE to Rep3 (visibility='owner'),
	// the one state that still puts a person out of another seat's read scope.
	elsewhere := e.As(e.Rep3, []ids.UUID{e.Team2}, integration.AdminPerms)
	hidden, err := native.Create(elsewhere, datasource.CreateInput{
		EntityType: datasource.EntityPerson,
		Fields:     json.RawMessage(`{"full_name":"Out Of Scope","owner_id":"` + e.Rep3.String() + `"}`),
		Source:     "test",
	})
	if err != nil {
		t.Fatalf("seeding the out-of-scope person: %v", err)
	}
	e.MakeCapturePrivate(t, "person", hidden.ID, e.Rep3)
	// The fixture is only evidence if the agent's own seat really cannot reach
	// it, and the row scope is what must do the hiding — not a missing grant.
	if _, err := native.Read(e.As(e.Rep3, []ids.UUID{e.Team2}, integration.RepPerms), datasource.EntityRef{
		Type: datasource.EntityPerson, ID: hidden.ID,
	}); err != nil {
		t.Fatalf("the owning rep cannot read the seeded person either (%v) — the refusal below would then "+
			"say nothing about the AGENT's scope", err)
	}

	// Through the door, with a staging engine that WOULD have written: "zero
	// approvals" then means the gate declined to stage, not that there was
	// nothing present to stage with.
	agent := scopedArchiveAgent(t, e)
	req := httptest.NewRequest(http.MethodDelete, "/v1/people/"+hidden.ID.String(), nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", hidden.ID.String())
	req = req.WithContext(context.WithValue(agent, chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()

	stageRefusal(rec, req, staging, restCommandDeps{records: native},
		agentPolicy{Op: "archivePerson", Access: accessTool, Tool: "archive_record", RecordType: recordTypePerson}, nil)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("archiving a row outside the agent's scope answered %d, want 404 — the archive itself "+
			"answers that once released, and a human's yes must not be spent on it", rec.Code)
	}
	if n := pendingApprovals(agent, t, e); n != 0 {
		t.Errorf("%d approval(s) were staged against a record the agent cannot see", n)
	}
}

// archiveRepPerms is RepPerms plus the one grant an archive needs.
//
// RepPerms carries person create/read/update and NO delete, which made every
// archive under it refusable for a reason none of these tests is about. That
// was invisible while staging asked only whether the target could be READ:
// the row was staged, a human could answer it, and the store refused the
// released retry on the missing grant. It is visible now that staging asks the
// executor, and the fixture has to say which refusal it means — the row-scope
// test above states the requirement in its own words: the hiding must be done
// by the row scope, "not a missing grant".
var archiveRepPerms = func() principal.Permissions {
	perms := integration.RepPerms
	objects := make(map[string]principal.ObjectGrant, len(perms.Objects))
	for object, grant := range perms.Objects {
		objects[object] = grant
	}
	person := objects["person"]
	person.Delete = true
	objects["person"] = person
	perms.Objects = objects
	return perms
}()

// scopedArchiveAgent is an agent acting for Rep1 with Rep1's team scope — the
// authority a real passport carries, which is the granting human's.
//
// The passport is SEEDED because a staged approval records the passport it was
// minted by under a real foreign key: an invented id would fail in the database
// and report a schema complaint where this test means to report a refusal.
func scopedArchiveAgent(t *testing.T, e *integration.Env) context.Context {
	t.Helper()
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:scoped-archive", SeatType: principal.SeatFull,
		OnBehalfOf: e.Rep1, UserID: e.Rep1, TeamIDs: []ids.UUID{e.Team1},
		PassportID:  e.SeedPassport(t, integration.OwnerConn(t), "scoped archive probe"),
		Scopes:      principal.NewScopeSet(principal.ScopeRead, principal.ScopeWrite),
		Permissions: archiveRepPerms,
	})
}

// pendingApprovals counts the staged rows.
//
// Bound through WithWorkspaceTx: Env.Pool is the workspace-bound app role, so an
// unbound count resolves the policy against a NULL workspace and answers zero
// for every row that exists — an absence-assertion that passes whether or not
// anything was staged, which is the one thing this test may not do.
func pendingApprovals(as context.Context, t *testing.T, e *integration.Env) int {
	t.Helper()
	var n int
	if err := database.WithWorkspaceTx(as, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(as, `SELECT count(*) FROM approval WHERE status = 'pending'`).Scan(&n)
	}); err != nil {
		t.Fatalf("counting the staged approvals: %v", err)
	}
	return n
}
