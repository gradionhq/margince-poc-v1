// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The Sink's row scope: a captured record resolves onto an INCUMBENT row —
// the lead a new address collides with, the activity a replayed natural key
// already landed as — and every one of those reads is a read. A connector
// runs under its granting human's row scope, so an incumbent that human
// cannot see must neither come back as a ref nor become a merge proposal.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
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
type recordingStager struct{ staged []capture.MergeProposal }

func (s *recordingStager) StageMerge(_ context.Context, in capture.MergeProposal) (ids.UUID, error) {
	s.staged = append(s.staged, in)
	return ids.NewV7(), nil
}

// newScopeCaptureRegistry wires the registry over a Sink with a recording
// merge stager — the default test registry has none, and "no proposal was
// staged" is only an assertion when staging is wired.
func newScopeCaptureRegistry(t *testing.T, e *SearchEnv, fake *scopeFake) (*capture.Registry, *recordingStager) {
	t.Helper()
	stager := &recordingStager{}
	sink := capture.NewSink(e.Pool).WithStager(stager)
	registry := capture.NewRegistry(e.Pool, sink, fakeAuthority{}, newTestKeyvault(t, e))
	registry.Register(fake)
	return registry, stager
}

func TestCaptureSkipsALeadCollidingWithAnInvisibleIncumbent(t *testing.T) {
	e := SetupSearch(t)
	// A lead owned by team2's rep — outside the team1 granting human's scope.
	e.Seed(t, `INSERT INTO lead (id, workspace_id, full_name, email, owner_id, source, captured_by)
		VALUES ($1, $2, 'Hidden Prospect', 'collide@scope.test', $3, 'manual', 'human:x')`, e.Rep3)

	fake := &scopeFake{records: []connector.NormalizedRecord{{
		EntityType: datasource.EntityLead,
		NaturalKey: connector.NaturalKey{SourceSystem: "graph", SourceID: "sender-9"},
		Fields:     capture.LeadFields{FullName: "Same Address", Email: "collide@scope.test"},
		Source:     "graph", CapturedBy: "connector:graph",
	}}}
	registry, stager := newScopeCaptureRegistry(t, e, fake)

	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})
	connID, err := registry.Connect(grantCtx, "graph", connector.Auth("token"))
	if err != nil {
		t.Fatal(err)
	}
	err = registry.SyncOnce(grantCtx, connID)
	if !errors.Is(err, connector.ErrSkip) {
		t.Fatalf("collision with an invisible lead → %v, want connector.ErrSkip", err)
	}
	// The skip names the natural key and never the address: the message must
	// not re-store the PII the capture was refused over.
	if strings.Contains(err.Error(), "collide@scope.test") {
		t.Errorf("the skip message carries the captured address: %q", err)
	}
	if !strings.Contains(err.Error(), "graph/sender-9") {
		t.Errorf("the skip message does not name the natural key: %q", err)
	}
	if len(stager.staged) != 0 {
		t.Errorf("staged a merge against an invisible lead: %+v", stager.staged)
	}
	if n := countRows(t, e, `SELECT count(*) FROM lead WHERE source_id = 'sender-9'`); n != 0 {
		t.Errorf("the refused capture created %d duplicate lead rows, want 0", n)
	}
}

func TestCaptureSkipsAnActivityReplayWhoseIncumbentLeftTheGrantingHumansScope(t *testing.T) {
	e := SetupSearch(t)
	foreign := e.Seed(t, `INSERT INTO person (id, workspace_id, full_name, owner_id, source, captured_by)
		VALUES ($1, $2, 'Foreign Counterparty', $3, 'manual', 'human:x')`, e.Rep3)

	fake := &scopeFake{records: []connector.NormalizedRecord{{
		EntityType: datasource.EntityActivity,
		NaturalKey: connector.NaturalKey{SourceSystem: "graph", SourceID: "msg-9"},
		Fields:     capture.ActivityFields{Kind: "email", Subject: "Quote", OccurredAt: fixedCaptureTime, Direction: "inbound"},
		Source:     "graph", CapturedBy: "connector:graph",
	}}}
	registry, _ := newScopeCaptureRegistry(t, e, fake)

	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})
	connID, err := registry.Connect(grantCtx, "graph", connector.Auth("token"))
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
		INSERT INTO activity_link (workspace_id, activity_id, entity_type, person_id)
		VALUES ($1, $2, 'person', $3)`, e.WS, activityID, foreign); err != nil {
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
