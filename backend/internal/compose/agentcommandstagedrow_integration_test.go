// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The two doors, staged for real: one operation, one record, two approval rows
// that a human could not tell apart on anything the decision depends on.
//
// The unit twin (agentcommandbothdoors_test.go) compares what each door
// RESOLVES. This compares what each door WROTE, and it has to run here for one
// reason: the version an approval pins is taken server-side inside the staging
// transaction (approvals.StageInTx), so no fake approvals engine can say
// anything about pin shape — a stub would answer whatever it was written to
// answer, for both doors, identically and meaninglessly.
//
// Two of the columns are DELIBERATELY different, and asserting the difference
// is as much the point as asserting the agreement: proposed_change and
// diff_hash are each door's own replay. A REST approval is redeemed by
// repeating the HTTP request, an MCP approval by repeating the tool call, and a
// row that carried the other door's payload would be a decision nobody could
// act on from where they made it.

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
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/authz"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// stagedApproval is the row a human decides from, as the approvals table holds
// it.
type stagedApproval struct {
	kind           string
	status         string
	targetType     string
	targetID       string
	pinned         bool
	proposedChange string
	diffHash       string
}

// archivePersonPolicy is the operation both doors are asked to perform.
var archivePersonPolicy = agentPolicy{
	Op: "archivePerson", Access: accessTool, Tool: "archive_record",
	RecordType: recordTypePerson, Tier: tierConfirmationRequired, Scope: scopeWrite,
}

func TestBothDoorsStageOneRowForOneOperation(t *testing.T) {
	e := integration.Setup(t)
	native := NewProvider(e.Pool)
	agent := scopedArchiveAgent(t, e)
	person := seedVisiblePerson(t, e, native, "Ada Lovelace")

	restID := stageArchiveOverREST(agent, t, e, native, person)
	toolID := stageArchiveOverTheToolDoor(agent, t, e, person)

	rest := readStagedApproval(agent, t, e, restID)
	tool := readStagedApproval(agent, t, e, toolID)

	// Both doors stage for the same operation against the same target inside one
	// test, and staging force-expires a stale pending approval that shares a
	// proposal identity (approvals/staging.go). Every comparison below is about
	// a row a human can decide from, so a superseded row must not pass as one.
	if rest.status != "pending" || tool.status != "pending" {
		t.Fatalf("the staged rows are %q (REST) and %q (tool), want both pending — an expired row is not "+
			"one a human can decide from", rest.status, tool.status)
	}
	if rest.kind != tool.kind {
		t.Errorf("the doors staged kinds %q and %q — an approval's kind is what the decision grants are "+
			"mapped by, so one of the two is decidable by a different set of people", rest.kind, tool.kind)
	}
	if rest.targetType != tool.targetType || rest.targetID != tool.targetID {
		t.Errorf("the doors bound one archive to two records: REST (%s,%s), tool (%s,%s) — the approvals "+
			"surface scopes and probes an inbox row by exactly this pair",
			rest.targetType, rest.targetID, tool.targetType, tool.targetID)
	}
	if rest.targetID != person.String() {
		t.Errorf("the staged target is %s, want the person %s both doors were asked about", rest.targetID, person)
	}
	if rest.pinned != tool.pinned {
		t.Errorf("one door pinned a version and the other did not (REST %v, tool %v) — the same approved "+
			"call would be re-checked against the record's version through one door and not the other",
			rest.pinned, tool.pinned)
	}
	if !rest.pinned {
		t.Error("neither door pinned a version for a person, which has one — an approval released against " +
			"an unpinned target is spent on whatever the record has become since")
	}

	// The deliberate difference. Each door's payload is the replay ITS OWN retry
	// must present: the REST row carries the request, the tool row the tool
	// call, and the diff hash each redemption checks is taken over its own.
	if rest.proposedChange == tool.proposedChange {
		t.Errorf("both doors staged the identical proposed_change %s — one of the two retries cannot "+
			"present the call it describes", rest.proposedChange)
	}
	if rest.diffHash == tool.diffHash {
		t.Error("both doors staged the identical diff_hash, so one door's approval would redeem the other " +
			"door's call — the hash binds an approval to the request that was actually made")
	}
}

// A record the agent's own row scope hides is refused by BOTH doors, and
// neither stages anything: the guards each door runs are the same guards,
// reached through the same resolver.
func TestBothDoorsRefuseOneRecordNeitherCallerCanSee(t *testing.T) {
	e := integration.Setup(t)
	native := NewProvider(e.Pool)
	agent := scopedArchiveAgent(t, e)

	// Capture-private to a rep in another team: a person is otherwise readable
	// by every seat whoever owns it, so visibility='owner' is the one state that
	// still hides the row from the human this agent acts for.
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

	rec := httptest.NewRecorder()
	stageRefusal(rec, archiveRequestFor(agent, hidden.ID), approvalsAdapter{svc: approvals.NewService(e.DB())},
		restCommandDeps{records: native}, archivePersonPolicy, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("the REST door answered %d for a row outside the agent's scope, want 404", rec.Code)
	}

	_, invokeErr := archiveRegistry(e).Invoke(agent, "archive_record",
		json.RawMessage(`{"record_type":"person","id":"`+hidden.ID.String()+`"}`))
	var staged *workflow.StagedApprovalError
	if errors.As(invokeErr, &staged) {
		t.Errorf("the tool door staged approval %s for a row outside the agent's scope, where the REST door "+
			"refused — one credential, two answers", staged.ApprovalID)
	}

	if n := pendingApprovals(agent, t, e); n != 0 {
		t.Errorf("%d approval(s) were staged against a record neither door's caller can see", n)
	}
}

// seedVisiblePerson writes one person through the real provider, owned by the
// human the agent acts for, so both doors can read it.
func seedVisiblePerson(t *testing.T, e *integration.Env, native *Provider, name string) ids.UUID {
	t.Helper()
	ref, err := native.Create(e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms), datasource.CreateInput{
		EntityType: datasource.EntityPerson,
		Fields:     json.RawMessage(`{"full_name":"` + name + `","owner_id":"` + e.Rep1.String() + `"}`),
		Source:     "test",
	})
	if err != nil {
		t.Fatalf("seeding the person both doors archive: %v", err)
	}
	return ref.ID
}

// archiveRequestFor is the request the router would hand the gate for
// DELETE /v1/people/{id}, carrying the agent's own context.
func archiveRequestFor(as context.Context, person ids.UUID) *http.Request {
	req := httptest.NewRequest(http.MethodDelete, "/v1/people/"+person.String(), nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", person.String())
	return req.WithContext(context.WithValue(as, chi.RouteCtxKey, rctx))
}

// repSeat re-derives, for the tool door's admission gate, exactly the authority
// the REST door's own request context carries: a full seat with a rep's
// permissions and the rep's team. Both doors must be governed by ONE authority
// or the comparison below is about seats rather than about doors — and a gate
// handed admin permissions would read rows the REST side's row scope hides,
// which is a difference in the fixture, not in the surface.
//
// The harness stamps a rep's authority onto its contexts rather than seeding
// role grants, so the live identity resolver has nothing to answer with here;
// this stands in for it, with the same two answers.
type repSeat struct{ e *integration.Env }

func (s repSeat) EffectiveRBAC(context.Context, ids.UUID, ids.UUID) (authz.RBAC, error) {
	return authz.RBAC{Permissions: archiveRepPerms, TeamIDs: []ids.UUID{s.e.Team1}}, nil
}

func (repSeat) SeatType(context.Context, ids.UUID, ids.UUID) (principal.SeatType, error) {
	return principal.SeatFull, nil
}

// archiveRegistry is the tool door over the real provider and the real
// approvals engine — the same adapter the composed api server injects, so the
// row this door writes is written by production's own stager.
func archiveRegistry(e *integration.Env) *agents.Registry {
	native := NewProvider(e.Pool)
	reg := agents.NewRegistry(approvalsAdapter{svc: approvals.NewService(e.DB())}, auth.NewGate(repSeat{e}))
	agents.RegisterCoreTools(reg, native, native, nil, fieldOwnership{pool: e.Pool})
	return reg
}

// stageArchiveOverREST stages through the REST door and answers the id of the
// row it wrote — read back rather than parsed out of the refusal text, since
// the row is what this test is about.
func stageArchiveOverREST(agent context.Context, t *testing.T, e *integration.Env, native *Provider,
	person ids.UUID,
) ids.ApprovalID {
	t.Helper()
	rec := httptest.NewRecorder()
	stageRefusal(rec, archiveRequestFor(agent, person), approvalsAdapter{svc: approvals.NewService(e.DB())},
		restCommandDeps{records: native}, archivePersonPolicy, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("the REST archive answered %d, want 403 with the redemption instructions", rec.Code)
	}
	ids := pendingApprovalIDs(agent, t, e)
	if len(ids) != 1 {
		t.Fatalf("%d approvals are pending after one REST archive, want exactly 1", len(ids))
	}
	return ids[0]
}

// stageArchiveOverTheToolDoor invokes the same archive as a tool call and
// answers the id the staging refusal carries.
func stageArchiveOverTheToolDoor(agent context.Context, t *testing.T, e *integration.Env,
	person ids.UUID,
) ids.ApprovalID {
	t.Helper()
	_, err := archiveRegistry(e).Invoke(agent, "archive_record",
		json.RawMessage(`{"record_type":"person","id":"`+person.String()+`"}`))
	var staged *workflow.StagedApprovalError
	if !errors.As(err, &staged) {
		t.Fatalf("the tool door answered %v rather than staging the archive", err)
	}
	return staged.ApprovalID
}

// pendingApprovalIDs lists what is waiting on a human, bound through
// WithWorkspaceTx: Env.Pool is the workspace-bound app role, and an unbound read
// resolves the policy against a NULL workspace and answers nothing for every
// row that exists.
func pendingApprovalIDs(as context.Context, t *testing.T, e *integration.Env) []ids.ApprovalID {
	t.Helper()
	var found []ids.ApprovalID
	if err := database.WithWorkspaceTx(as, e.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(as, `SELECT id FROM approval WHERE status = 'pending' ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id ids.ApprovalID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			found = append(found, id)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("listing the staged approvals: %v", err)
	}
	return found
}

func readStagedApproval(as context.Context, t *testing.T, e *integration.Env, id ids.ApprovalID) stagedApproval {
	t.Helper()
	var row stagedApproval
	if err := database.WithWorkspaceTx(as, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(as, `
			SELECT kind, status, coalesce(target_entity_type, ''), coalesce(target_entity_id::text, ''),
			       target_version IS NOT NULL, proposed_change::text, diff_hash
			FROM approval WHERE id = $1`, id).Scan(
			&row.kind, &row.status, &row.targetType, &row.targetID, &row.pinned, &row.proposedChange,
			&row.diffHash)
	}); err != nil {
		t.Fatalf("reading staged approval %s: %v", id, err)
	}
	return row
}
