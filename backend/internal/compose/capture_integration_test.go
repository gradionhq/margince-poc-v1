// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose_test

// The capture substrate end to end (B-EP05.1/.2/.3/.9/.10/.11a): a fake
// connector syncs through the ONE Sink — raw original + domain row +
// audit (connector principal) + captured event in one transaction,
// idempotent replay, link targets visibility-probed, and the
// grant-time scope intersection refusing an over-scoped connector.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/authz"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// mailFake is the in-repo test connector: two records per sync — one
// email activity linked to a person, one lead. The raw payload varies
// per sync so replay tests can prove evidence immutability.
type mailFake struct {
	linkTo    ids.UUID
	scopes    []principal.Scope
	syncCount int
}

func (m *mailFake) Descriptor() connector.Descriptor {
	scopes := m.scopes
	if scopes == nil {
		scopes = []principal.Scope{principal.ScopeRead}
	}
	return connector.Descriptor{
		Name: "mailfake", Version: "1.0.0",
		Scopes:   scopes,
		RiskTier: mcp.TierGreen,
		Produces: []datasource.EntityType{datasource.EntityActivity, datasource.EntityLead},
	}
}

func (m *mailFake) Authenticate(context.Context, connector.AuthRequest) (connector.Auth, error) {
	return connector.Auth("token"), nil
}

func (m *mailFake) Sync(ctx context.Context, _ connector.Auth, cursor connector.Cursor, sink connector.Sink) (connector.Cursor, error) {
	m.syncCount++
	records := []connector.NormalizedRecord{
		{
			EntityType: datasource.EntityActivity,
			NaturalKey: connector.NaturalKey{SourceSystem: "mailfake", SourceID: "msg-1"},
			Fields:     capture.ActivityFields{Kind: "email", Subject: "Quote request", Body: "please send pricing", OccurredAt: time.Now().UTC(), Direction: "inbound"},
			Links:      []datasource.EntityRef{{Type: datasource.EntityPerson, ID: m.linkTo}},
			Source:     "mailfake", CapturedBy: "connector:mailfake",
			Raw: []byte(fmt.Sprintf(`{"provider":"mailfake","message_id":"msg-1","sync":%d}`, m.syncCount)),
		},
		{
			EntityType: datasource.EntityLead,
			NaturalKey: connector.NaturalKey{SourceSystem: "mailfake", SourceID: "sender-1"},
			Fields:     capture.LeadFields{FullName: "Lead Sender", Email: "sender@mailfake.test", CompanyName: "Mailfake GmbH"},
			Source:     "mailfake", CapturedBy: "connector:mailfake",
		},
	}
	for _, rec := range records {
		if _, err := sink.Upsert(ctx, rec); err != nil {
			return cursor, err
		}
	}
	return connector.Cursor(fmt.Sprintf("sync-%d", m.syncCount)), nil
}

func (m *mailFake) Normalize(context.Context, connector.RawRecord) ([]connector.NormalizedRecord, error) {
	return nil, connector.ErrSkip
}

func (m *mailFake) HealthCheck(context.Context, connector.Auth) error { return nil }

// captureCounts tallies what a connector sync left behind, read inside
// one workspace-bound transaction.
type captureCounts struct{ activities, leads, raws, audits int }

func readCaptureCounts(t *testing.T, e *searchEnv) captureCounts {
	t.Helper()
	var got captureCounts
	err := database.WithWorkspaceTx(e.admin(), e.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(context.Background(), `SELECT count(*) FROM activity WHERE source_system = 'mailfake'`).Scan(&got.activities); err != nil {
			return err
		}
		if err := tx.QueryRow(context.Background(), `SELECT count(*) FROM lead WHERE source_system = 'mailfake'`).Scan(&got.leads); err != nil {
			return err
		}
		if err := tx.QueryRow(context.Background(), `SELECT count(*) FROM raw_capture`).Scan(&got.raws); err != nil {
			return err
		}
		return tx.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE actor_type = 'connector'`).Scan(&got.audits)
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestCaptureSyncIsIdempotentAndProvenanced(t *testing.T) {
	e := setupSearch(t)
	personID := e.seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Inbox Sender', 'manual', 'human:x')`)

	registry := newTestCaptureRegistry(e)
	fake := &mailFake{linkTo: personID}
	registry.Register(fake)

	grantCtx := e.humanWithScopes(e.rep1, []principal.Scope{principal.ScopeRead})
	connID, err := registry.Connect(grantCtx, "mailfake", connector.Auth("token"))
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.SyncOnce(grantCtx, connID); err != nil {
		t.Fatal(err)
	}
	// Replay: the connector re-emits the same natural keys.
	if err := registry.SyncOnce(grantCtx, connID); err != nil {
		t.Fatal(err)
	}

	got := readCaptureCounts(t, e)
	if got.activities != 1 || got.leads != 1 || got.raws != 1 {
		t.Fatalf("replay duplicated rows: %+v", got)
	}
	// Raw capture is evidence: the replay carried DIFFERENT bytes, and
	// the stored original must not have moved.
	var payload string
	if err := e.owner.QueryRow(context.Background(),
		`SELECT payload->>'sync' FROM raw_capture WHERE source_id = 'msg-1'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if payload != "1" {
		t.Fatalf("replay rewrote the raw evidence: sync=%s, want the first capture's 1", payload)
	}
	if got.audits != 2 {
		t.Fatalf("connector audit rows = %d, want 2 (one per NEW record, none for replays)", got.audits)
	}
	// The captured event went through the outbox exactly once.
	var captured int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM event_outbox WHERE envelope->>'type' = 'activity.captured'`).Scan(&captured); err != nil {
		t.Fatal(err)
	}
	if captured != 1 {
		t.Fatalf("activity.captured emitted %d times, want 1", captured)
	}
	// Provenance is the connector, and the link landed.
	var capturedBy string
	var links int
	err = database.WithWorkspaceTx(e.admin(), e.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(context.Background(), `SELECT captured_by FROM activity WHERE source_system = 'mailfake'`).Scan(&capturedBy); err != nil {
			return err
		}
		return tx.QueryRow(context.Background(), `SELECT count(*) FROM activity_link WHERE person_id = $1`, personID).Scan(&links)
	})
	if err != nil {
		t.Fatal(err)
	}
	if capturedBy != "connector:mailfake" || links != 1 {
		t.Fatalf("provenance/link wrong: captured_by=%q links=%d", capturedBy, links)
	}
	// The cursor advanced.
	var cursor []byte
	err = database.WithWorkspaceTx(grantCtx, e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT cursor FROM connector_connection WHERE id = $1`, connID).Scan(&cursor)
	})
	if err != nil || string(cursor) != "sync-2" {
		t.Fatalf("cursor = %q err=%v, want sync-2", cursor, err)
	}
}

func TestCaptureScopeIntersectionRefusesOverScopedConnector(t *testing.T) {
	e := setupSearch(t)
	registry := newTestCaptureRegistry(e)
	registry.Register(&mailFake{scopes: []principal.Scope{principal.ScopeRead, principal.ScopeSend}})

	grantCtx := e.humanWithScopes(e.rep1, []principal.Scope{principal.ScopeRead})
	_, err := registry.Connect(grantCtx, "mailfake", nil)
	if !errors.Is(err, apperrors.ErrScopeExceeded) {
		t.Fatalf("over-scoped connector grant → %v, want ErrScopeExceeded", err)
	}
	var connections int
	err = database.WithWorkspaceTx(grantCtx, e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT count(*) FROM connector_connection`).Scan(&connections)
	})
	if err != nil || connections != 0 {
		t.Fatalf("refused grant persisted a connection: %d %v", connections, err)
	}
}

func TestCaptureLinkTargetOutsideScopeRefused(t *testing.T) {
	e := setupSearch(t)
	// A person owned by team2 — invisible to the team1 granting human.
	foreignPerson := e.seed(t, `INSERT INTO person (id, workspace_id, full_name, owner_id, source, captured_by) VALUES ($1, $2, 'Foreign Target', $3, 'manual', 'human:x')`, e.rep3)

	registry := newTestCaptureRegistry(e)
	fake := &mailFake{linkTo: foreignPerson}
	registry.Register(fake)

	grantCtx := e.humanWithScopes(e.rep1, []principal.Scope{principal.ScopeRead})
	connID, err := registry.Connect(grantCtx, "mailfake", nil)
	if err != nil {
		t.Fatal(err)
	}
	err = registry.SyncOnce(grantCtx, connID)
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("cross-scope link target → %v, want existence-hiding ErrNotFound", err)
	}
	// The refused record left no activity behind (one tx per record).
	var activities int
	dbErr := database.WithWorkspaceTx(e.admin(), e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT count(*) FROM activity WHERE source_system = 'mailfake'`).Scan(&activities)
	})
	if dbErr != nil || activities != 0 {
		t.Fatalf("refused capture left rows: %d %v", activities, dbErr)
	}
}

// humanWithScopes builds a human principal in the searchEnv workspace
// carrying rep-grade RBAC (team scope) plus explicit verb scopes for
// the connector grant check.
func (e *searchEnv) humanWithScopes(user ids.UUID, scopes []principal.Scope) context.Context {
	scopeSet := principal.NewScopeSet()
	for _, s := range scopes {
		scopeSet[s] = struct{}{}
	}
	ctx := principal.WithWorkspaceID(context.Background(), e.ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(), UserID: user,
		TeamIDs:  []ids.UUID{e.team1},
		SeatType: principal.SeatFull,
		Scopes:   scopeSet,
		Permissions: principal.Permissions{
			Objects: map[string]principal.ObjectGrant{
				"activity": {Create: true, Read: true},
				"lead":     {Create: true, Read: true},
				"person":   {Read: true},
			},
			RowScope: principal.RowScopeTeam,
		},
	})
}

// fakeAuthority stands in for identity's live resolver: rep-grade RBAC
// for every human (the resolver-integration line is compose's).
type fakeAuthority struct{}

func (fakeAuthority) EffectiveRBAC(context.Context, ids.UUID, ids.UUID) (authz.RBAC, error) {
	return authz.RBAC{Permissions: principal.Permissions{
		Objects: map[string]principal.ObjectGrant{
			"activity": {Create: true, Read: true},
			"lead":     {Create: true, Read: true},
			"person":   {Read: true},
		},
		RowScope: principal.RowScopeTeam,
	}}, nil
}

func (fakeAuthority) SeatType(context.Context, ids.UUID, ids.UUID) (principal.SeatType, error) {
	return principal.SeatFull, nil
}

func newTestCaptureRegistry(e *searchEnv) *capture.Registry {
	return capture.NewRegistry(e.pool, capture.NewSink(e.pool), fakeAuthority{})
}
