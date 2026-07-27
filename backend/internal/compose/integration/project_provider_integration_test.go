// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The project through the datasource seam — the surface an MCP agent reaches
// records by. It is not a thin alias for the REST handlers: it decodes
// STRICTLY (an unknown field is a refusal, not a silent drop), stamps its own
// provenance, and its archive verb carries no version because the agent
// surface has none to offer.
//
// What matters here is that the seam is held to the same rules as the human
// path — the same validation, the same RBAC, the same typed refusals — since
// an agent reaching a weaker copy of a surface is the whole risk the seam
// exists to remove.

import (
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

func projectProvider(e *Env) *deals.Provider { return deals.NewProvider(e.Pool) }

// The seam's create-read-update-archive round trip, with the provenance the
// agent path stamps.
func TestProjectThroughTheAgentSeam(t *testing.T) {
	e := Setup(t)
	p := projectProvider(e)
	ctx := e.Admin()
	org := e.SeedOrg(t, "Seam GmbH", nil)

	created, err := p.Create(ctx, datasource.CreateInput{
		EntityType: datasource.EntityProject,
		Fields: map[string]any{
			"name": "Agent-opened work", "organization_id": org.String(),
		},
		Source: "agent",
	})
	if err != nil {
		t.Fatalf("create through the seam: %v", err)
	}
	if created.Type != datasource.EntityProject {
		t.Fatalf("created ref type = %s, want project", created.Type)
	}

	record, err := p.Read(ctx, created)
	if err != nil {
		t.Fatalf("read through the seam: %v", err)
	}
	if record.Ref.ID != created.ID {
		t.Fatalf("read back %s, want the project just created", record.Ref.ID)
	}

	if _, err := p.Update(ctx, datasource.UpdateInput{
		Ref:   created,
		Patch: map[string]any{"description": "written by an agent"},
	}); err != nil {
		t.Fatalf("update through the seam: %v", err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM project WHERE id = $1 AND description = $2`,
		created.ID, "written by an agent"); n != 1 {
		t.Fatal("the seam's update did not reach the row")
	}

	if _, err := p.Archive(ctx, created); err != nil {
		t.Fatalf("archive through the seam: %v", err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM project WHERE id = $1 AND archived_at IS NOT NULL`,
		created.ID); n != 1 {
		t.Fatal("the seam's archive did not retire the project")
	}
}

// The create contract carries additionalProperties, so an unrecognised key is
// a CUSTOM-FIELD candidate rather than a refusal — it lands only if the
// workspace catalog has a matching column, and is dropped otherwise.
//
// That makes one thing worth pinning hard: a key naming a real project column
// the contract does not accept must not reach that column. `phase` moves only
// through advanceProjectPhase, which is what keeps a move, its history row and
// its event in one transaction — so the agent seam must not be a side door
// onto it.
func TestTheAgentSeamCannotSetPhaseThroughAnUnrecognisedField(t *testing.T) {
	e := Setup(t)
	p := projectProvider(e)
	org := e.SeedOrg(t, "Strict GmbH", nil)

	created, err := p.Create(e.Admin(), datasource.CreateInput{
		EntityType: datasource.EntityProject,
		Fields: map[string]any{
			"name": "Typo carrier", "organization_id": org.String(),
			"phase": "delivering",
		},
		Source: "agent",
	})
	if err != nil {
		t.Fatalf("create through the seam: %v", err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM project WHERE id = $1 AND phase = 'initiative'`,
		created.ID); n != 1 {
		t.Fatal("an unrecognised `phase` field reached the column — the seam is a side door around advanceProjectPhase")
	}
	// And the history says the same: one birth row, no transition.
	if n := e.WsCount(t, `SELECT count(*) FROM project_phase_history WHERE project_id = $1`,
		created.ID); n != 1 {
		t.Fatalf("%d phase-history rows for a project that never advanced", n)
	}
}

// The seam is held to the same validation as the human path. It would be easy
// for an agent surface to grow its own laxer mapping; this pins that it has
// not.
func TestTheAgentSeamAppliesTheSameProjectRulesAsREST(t *testing.T) {
	e := Setup(t)
	p := projectProvider(e)
	org := e.SeedOrg(t, "Rules GmbH", nil)

	for name, fields := range map[string]map[string]any{
		"no name":    {"organization_id": org.String()},
		"blank name": {"name": "   ", "organization_id": org.String()},
		"bad key":    {"name": "Keyed", "key": "1nvalid", "organization_id": org.String()},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := p.Create(e.Admin(), datasource.CreateInput{
				EntityType: datasource.EntityProject, Fields: fields, Source: "agent",
			}); err == nil {
				t.Fatalf("the seam accepted %s", name)
			}
		})
	}
}

// An entity the seam does not serve must say so by name rather than fail
// obscurely — the gate above it decides what to do with that answer.
func TestTheAgentSeamNamesAnEntityItDoesNotServe(t *testing.T) {
	e := Setup(t)
	p := projectProvider(e)

	_, err := p.Create(e.Admin(), datasource.CreateInput{
		EntityType: datasource.EntityPerson, Fields: map[string]any{}, Source: "agent",
	})
	var unsupported *datasource.UnsupportedEntityError
	if !errors.As(err, &unsupported) {
		t.Fatalf("create of an unserved entity produced %v, want UnsupportedEntityError", err)
	}
	if _, err := p.Archive(e.Admin(), datasource.EntityRef{
		Type: datasource.EntityPerson, ID: ids.NewV7(),
	}); !errors.As(err, &unsupported) {
		t.Fatalf("archive of an unserved entity produced %v, want UnsupportedEntityError", err)
	}
}

// RBAC does not weaken because the caller is an agent: the seam runs the
// store's own gates, so a principal without the project grant is refused
// here exactly as on the human path.
func TestTheAgentSeamStillEnforcesTheProjectGrant(t *testing.T) {
	e := Setup(t)
	p := projectProvider(e)
	org := e.SeedOrg(t, "Gated GmbH", nil)

	readOnly := e.As(e.Rep1, []ids.UUID{e.Team1}, principalReadOnlyProject())
	_, err := p.Create(readOnly, datasource.CreateInput{
		EntityType: datasource.EntityProject,
		Fields:     map[string]any{"name": "Not allowed", "organization_id": org.String()},
		Source:     "agent",
	})
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("a principal without project.create was answered %v, want a permission denial", err)
	}
}

// principalReadOnlyProject is a rep who may look at projects and not open
// one — the posture that proves the seam runs the store's gate rather than
// its own.
func principalReadOnlyProject() principal.Permissions {
	return principal.Permissions{
		RoleKeys: []string{"rep"},
		Objects: map[string]principal.ObjectGrant{
			"project":      {Read: true},
			"organization": {Read: true},
		},
		RowScope: principal.RowScopeOwn,
	}
}
