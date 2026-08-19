// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package capture

// The Sink's read scope: a captured record resolves onto an INCUMBENT row —
// the lead a new address collides with, the activity a replayed natural key
// already landed as — and every one of those reads is a read. A connector
// runs under its granting human's read scope. Leads are readable by every
// seat of the workspace, so a collision with another team's lead stages a
// merge proposal; an activity whose only link is a colleague's
// capture-private contact is one that human cannot see, and a replay onto it
// must neither come back as a ref nor touch the row.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	capturemod "github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// scopeFake emits whatever batch the test hands it, so one connector serves
// both the collision case and the replay case.
type scopeFake struct{ records []connector.NormalizedRecord }

func (f *scopeFake) Descriptor() connector.Descriptor {
	return connector.Descriptor{
		Name: "graph", Version: "1.0.0",
		Scopes:   []principal.Scope{principal.ScopeRead},
		RiskTier: mcp.TierAutoExecute,
		Produces: []datasource.EntityType{datasource.EntityActivity, datasource.EntityLead},
	}
}

func (f *scopeFake) Authenticate(context.Context, connector.AuthRequest) (connector.Auth, error) {
	return connector.Auth("token"), nil
}

func (f *scopeFake) Sync(ctx context.Context, _ connector.Auth, cursor connector.Cursor, sink connector.Sink) (connector.Cursor, error) {
	for _, rec := range f.records {
		if _, err := sink.Upsert(ctx, rec); err != nil {
			return cursor, err
		}
	}
	return cursor, nil
}

func (f *scopeFake) Normalize(context.Context, connector.RawRecord) ([]connector.NormalizedRecord, error) {
	return nil, connector.ErrSkip
}

func (f *scopeFake) HealthCheck(context.Context, connector.Auth) error { return nil }

// recordingStager stands in for the approvals engine so a merge proposal the
// Sink must NOT raise is observable.
type recordingStager struct{ staged []capturemod.MergeProposal }

func (s *recordingStager) StageMerge(_ context.Context, in capturemod.MergeProposal) (ids.UUID, error) {
	s.staged = append(s.staged, in)
	return ids.NewV7(), nil
}

// newScopeCaptureRegistry wires the registry over a Sink with a recording
// merge stager — the default test registry has none, and "no proposal was
// staged" is only an assertion when staging is wired.
func newScopeCaptureRegistry(t *testing.T, e *integration.SearchEnv, fake *scopeFake) (*capturemod.Registry, *recordingStager) {
	t.Helper()
	stager := &recordingStager{}
	sink := capturemod.NewSink(e.DB()).WithStager(stager)
	registry := capturemod.NewRegistry(e.DB(), sink, fakeAuthority{}, newTestKeyvault(t, e))
	registry.Register(fake)
	return registry, stager
}

func TestCaptureStagesAMergeForALeadCollidingWithAnotherTeamsLead(t *testing.T) {
	e := integration.SetupSearch(t)
	// A lead owned by team2's rep. Leads carry no capture privacy, so the
	// team1 granting human reads it and the collision is theirs to resolve.
	incumbent := e.SeedID(t, `INSERT INTO lead (id, full_name, email, owner_id, source, captured_by)
		VALUES ($1, 'Other Team Prospect', 'collide@scope.test', $2, 'manual', 'human:x')`, e.Rep3)

	fake := &scopeFake{records: []connector.NormalizedRecord{{
		EntityType: datasource.EntityLead,
		NaturalKey: connector.NaturalKey{SourceSystem: "graph", SourceID: "sender-9"},
		Fields:     capturemod.LeadFields{FullName: "Same Address", Email: "collide@scope.test"},
		Source:     "graph", CapturedBy: "connector:graph",
	}}}
	registry, stager := newScopeCaptureRegistry(t, e, fake)

	grantCtx := humanWithScopes(e, e.Rep1, []principal.Scope{principal.ScopeRead})
	connID, err := registry.Connect(grantCtx, "graph", connector.Auth("token"), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.SyncOnce(grantCtx, connID); err != nil {
		t.Fatalf("collision with another team's lead → %v, want a staged merge and no error", err)
	}
	if len(stager.staged) != 1 {
		t.Fatalf("staged merges = %+v, want exactly one against the incumbent lead", stager.staged)
	}
	if got := stager.staged[0]; got.TargetType != "lead" || got.TargetID != incumbent {
		t.Errorf("merge target = %s/%v, want lead/%v", got.TargetType, got.TargetID, incumbent)
	}
	// The collision stages a proposal for a human; it never folds the address
	// into a second lead row.
	if n := countRows(t, e, `SELECT count(*) FROM lead WHERE source_id = 'sender-9'`); n != 0 {
		t.Errorf("the collision created %d duplicate lead rows, want 0", n)
	}
}

func TestCaptureSkipsAnActivityReplayWhoseIncumbentLeftTheGrantingHumansScope(t *testing.T) {
	e := integration.SetupSearch(t)
	// Capture-private to Rep3: the one state that hides a person from the
	// team1 granting human.
	foreign := e.SeedID(t, `INSERT INTO person (id, full_name, owner_id, visibility, source, captured_by)
		VALUES ($1, 'Foreign Private Counterparty', $2, 'owner', 'manual', 'human:x')`, e.Rep3)

	fake := &scopeFake{records: []connector.NormalizedRecord{{
		EntityType: datasource.EntityActivity,
		NaturalKey: connector.NaturalKey{SourceSystem: "graph", SourceID: "msg-9"},
		Fields:     capturemod.ActivityFields{Kind: "email", Subject: "Quote", OccurredAt: fixedCaptureTime, Direction: "inbound"},
		Source:     "graph", CapturedBy: "connector:graph",
	}}}
	registry, _ := newScopeCaptureRegistry(t, e, fake)

	grantCtx := humanWithScopes(e, e.Rep1, []principal.Scope{principal.ScopeRead})
	connID, err := registry.Connect(grantCtx, "graph", connector.Auth("token"), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.SyncOnce(grantCtx, connID); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	// The activity is later linked to a record the granting human cannot see,
	// which takes the activity itself out of their scope (the link walk).
	var activityID ids.UUID
	if err := e.Owner.QueryRow(context.Background(),
		`SELECT id FROM activity WHERE source_system = 'graph' AND source_id = 'msg-9'`).Scan(&activityID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Owner.Exec(context.Background(), `
		INSERT INTO activity_link (activity_id, entity_type, person_id)
		VALUES ($1, 'person', $2)`, activityID, foreign); err != nil {
		t.Fatal(err)
	}

	err = registry.SyncOnce(grantCtx, connID)
	if !errors.Is(err, connector.ErrSkip) {
		t.Fatalf("replay onto an invisible activity → %v, want connector.ErrSkip", err)
	}
	if strings.Contains(err.Error(), activityID.String()) {
		t.Errorf("the skip message discloses the invisible incumbent's id: %q", err)
	}
	if n := countRows(t, e, `SELECT count(*) FROM activity WHERE source_system = 'graph'`); n != 1 {
		t.Errorf("the refused replay left %d activity rows, want the single original", n)
	}
}
