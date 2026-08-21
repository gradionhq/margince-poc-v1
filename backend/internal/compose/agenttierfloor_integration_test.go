// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The bypass itself, against real Postgres (#982).
//
// The contract declares `createProject` and `updateProject` confirm-first for an
// agent — a tier tightened for ONE record type, where creating a person or a
// deal is not tightened at all. The REST door honours it, because its tier comes
// from the operation. The MCP door resolves a tier from the VERB, and the verb
// that performs the same write is `create_record`, which is auto-execute for
// every type it admits.
//
// So the refusal is one tool call away from being irrelevant, and the only way
// to prove that is to let the call reach the write and then look at the
// database. A unit test asserting a resolved tier would prove what the gate
// decided; it would not prove that a project now exists which no human agreed
// to.
//
// The registry is built through registryWithGate — the same function the api role
// composes — so what is proved is that the floor is WIRED and applied before
// admission, not merely that the option works. The missing wiring WAS the defect,
// and a registry a test assembled itself would prove only the option.
//
// One seam is stubbed: the authority resolver (adminSeat, from
// dealmovepin_integration_test.go). The harness seeds users with no role
// assignment, so the live resolver denies every grant, and a refusal for a
// missing grant looks exactly like the refusal these tests want. Stubbing the
// question that is NOT under test is what lets the tier be the only thing that
// can answer.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

func TestATierTheContractTightensForOneRecordTypeBindsTheToolDoorToo(t *testing.T) {
	e := integration.Setup(t)
	human := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms)
	native := NewProvider(e.Pool)

	registry := composedRegistry(e)

	org := seedOrgForTierFloor(human, t, native)

	// The identical effect POST /v1/projects performs, asked for through the
	// verb whose tier the contract never tightened.
	_, err := registry.Invoke(tierFloorAgent(t, e), "create_record",
		json.RawMessage(`{"record_type":"project","fields":{"name":"Unapproved",`+
			`"organization_id":"`+org.String()+`"}}`))

	// Three assertions, answering three different questions, because any two of
	// them pass for the wrong reason.
	//
	// The sentinel alone proves least: workflow.StagedApprovalError unwraps to
	// ErrRequiresApproval, and so does the bare refusal a tool with no StageInfo
	// returns — so a fix that tightened the tier and left the call dead-ending
	// would satisfy it exactly. The staged ROW is what separates "put to a human"
	// from "refused with nowhere to land", which is the whole point of the two new
	// StageInfo implementations. The row count says no effect happened, which a
	// call refused for a missing grant would also satisfy — hence a full seat.
	if !errors.Is(err, apperrors.ErrRequiresApproval) {
		t.Errorf("create_record{project} answered %v, want the confirm-first refusal the "+
			"contract declares for createProject — a tier tightened for one record type is a "+
			"decision the tool door cannot see", err)
	}
	if n := liveProjectsNamed(human, t, e, "Unapproved"); n != 0 {
		t.Errorf("%d project(s) named Unapproved exist; the agent performed unattended the write "+
			"POST /v1/projects would have staged for a human", n)
	}
	assertStagedApprovalRow(human, t, e, stagedRow{kind: "create_record", targetType: "project", hasTargetID: false})
}

// The update half of the same bypass. It is a separate call rather than a table
// case because it stages a different SHAPE — a target id and a pin, where the
// create has neither — and because a fix that tightened creates and missed
// updates would otherwise leave every gate in this package green.
func TestATightenedUpdateAlsoStagesOnTheToolDoor(t *testing.T) {
	e := integration.Setup(t)
	human := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms)
	native := NewProvider(e.Pool)
	registry := composedRegistry(e)

	org := seedOrgForTierFloor(human, t, native)
	project := seedProjectForTierFloor(human, t, native, org)

	_, err := registry.Invoke(tierFloorAgent(t, e), "update_record",
		json.RawMessage(`{"record_type":"project","id":"`+project.String()+
			`","fields":{"name":"Renamed without asking"}}`))

	if !errors.Is(err, apperrors.ErrRequiresApproval) {
		t.Errorf("update_record{project} answered %v, want the confirm-first refusal "+
			"PATCH /v1/projects/{id} declares", err)
	}
	if n := liveProjectsNamed(human, t, e, "Renamed without asking"); n != 0 {
		t.Error("the project was renamed unattended; the REST twin stages that patch for a human")
	}
	assertStagedApprovalRow(human, t, e, stagedRow{kind: "update_record", targetType: "project", hasTargetID: true})
}

// An ordinary organization patch must NOT stage. `updateOrganization` is
// auto-execute, and the confirm-first routes that share (update_record,
// organization) — the fact and profile-field corrections — write a sidecar row
// this verb cannot reach. A floor that collapsed them tightened every org patch
// on the tool door while REST kept it automatic: #982 in reverse, which is what
// this test exists to catch and what the first draft of the fix did.
func TestAnOrdinaryOrganizationPatchStillRunsUnattended(t *testing.T) {
	e := integration.Setup(t)
	human := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms)
	native := NewProvider(e.Pool)
	registry := composedRegistry(e)

	org := seedOrgForTierFloor(human, t, native)

	if _, err := registry.Invoke(tierFloorAgent(t, e), "update_record",
		json.RawMessage(`{"record_type":"organization","id":"`+org.String()+
			`","fields":{"industry":"Logistics"}}`)); err != nil {
		t.Fatalf("an ordinary organization patch answered %v, want it to run — the contract "+
			"declares updateOrganization auto-execute, and tightening it here would make the tool "+
			"door stage what the REST door performs", err)
	}
}

// composedRegistry is the api role's own composition, with the authority seam
// stubbed for the reason stated at the top of this file. It goes through
// registryWithGate rather than NewRegistry so the tier floor arrives the way
// production supplies it.
func composedRegistry(e *integration.Env) *agents.Registry {
	return registryWithGate(e.DB(), auth.NewGate(adminSeat{}), nil, nil,
		SendPath{}, companyEnricher{}, nil, nil, nil, nil)
}

// stagedRow is what a staged approval must look like for the operation that
// produced it — the shape, not just the fact that something was written.
type stagedRow struct {
	kind, targetType string
	hasTargetID      bool
}

// assertStagedApprovalRow reads the approval the refusal claims to have minted. Without it
// this suite could not tell a staged call from one refused with nowhere to land:
// both answer ErrRequiresApproval, and only one of them puts a question in front
// of a human.
func assertStagedApprovalRow(as context.Context, t *testing.T, e *integration.Env, want stagedRow) {
	t.Helper()
	var kind, targetType string
	var targetID *ids.UUID
	if err := database.WithWorkspaceTx(as, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(as, `
			SELECT kind, coalesce(target_entity_type, ''), target_entity_id
			  FROM approval
			 WHERE kind = $1 AND status = 'pending'`, want.kind).Scan(&kind, &targetType, &targetID)
	}); err != nil {
		t.Fatalf("no pending %s approval was staged (%v) — the call was refused with nowhere to "+
			"land, which is not the confirm-first promise the contract makes", want.kind, err)
	}
	if targetType != want.targetType {
		t.Errorf("staged target type %q, want %q — the approvals surface scopes an inbox row by "+
			"probing its target's own visibility, and it cannot probe a type it was not told",
			targetType, want.targetType)
	}
	if got := targetID != nil; got != want.hasTargetID {
		t.Errorf("staged with a target id = %v, want %v", got, want.hasTargetID)
	}
}

// seedProjectForTierFloor creates one project through the real writer, so the
// update case patches a row the store actually wrote.
func seedProjectForTierFloor(as context.Context, t *testing.T, p *Provider, org ids.UUID) ids.UUID {
	t.Helper()
	ref, err := p.Create(as, datasource.CreateInput{
		EntityType: datasource.EntityProject,
		Fields:     json.RawMessage(`{"name":"Tier floor project","organization_id":"` + org.String() + `"}`),
		Source:     "test",
	})
	if err != nil {
		t.Fatalf("seeding the project: %v", err)
	}
	return ref.ID
}

// seedOrgForTierFloor creates the anchor company a project requires, through the
// real writer.
func seedOrgForTierFloor(as context.Context, t *testing.T, p *Provider) ids.UUID {
	t.Helper()
	ref, err := p.Create(as, datasource.CreateInput{
		EntityType: datasource.EntityOrganization,
		Fields:     json.RawMessage(`{"display_name":"Tier floor anchor"}`),
		Source:     "test",
	})
	if err != nil {
		t.Fatalf("seeding the anchor company: %v", err)
	}
	return ref.ID
}

// liveProjectsNamed counts the projects the call did or did not create.
//
// Bound through WithWorkspaceTx, not read off the pool directly. Env.Pool is the
// workspace-bound app role, so an unbound `SELECT count(*) FROM project` resolves the
// policy expression against a NULL workspace and answers zero for every row that
// exists — an absence-assertion that passes whether or not the write happened,
// which is the one thing this test may not do.
func liveProjectsNamed(as context.Context, t *testing.T, e *integration.Env, name string) int {
	t.Helper()
	var n int
	if err := database.WithWorkspaceTx(as, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(as, `SELECT count(*) FROM project WHERE name = $1`, name).Scan(&n)
	}); err != nil {
		t.Fatalf("counting the projects: %v", err)
	}
	return n
}

// tierFloorAgent is a passport holding every scope and grant a project create
// needs, so the only thing left that can refuse it is the tier.
//
// The passport is SEEDED rather than invented: a staged approval records the
// passport it was minted by under a real foreign key, so a synthetic id fails in
// the database and the test would report a schema complaint where it means to
// report a governance decision.
func tierFloorAgent(t *testing.T, e *integration.Env) context.Context {
	t.Helper()
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:tier-floor", SeatType: principal.SeatFull,
		OnBehalfOf: e.Rep1, UserID: e.Rep1,
		PassportID:  e.SeedPassport(t, integration.OwnerConn(t), "tier floor probe"),
		Scopes:      principal.NewScopeSet(principal.ScopeRead, principal.ScopeWrite),
		Permissions: integration.AdminPerms,
	})
}
