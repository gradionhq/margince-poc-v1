// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The RC-2 personal-mail exclusion gate end to end (capture.md CAP-DDL-3,
// AC1.3/AC1.8): a message matching one of the capturing user's rules
// produces ZERO rows anywhere — no activity, no raw original — and exactly
// one capture.skipped{personal_exclusion} event; a non-matching message in
// the same sync lands normally. The gate lives in the ONE Sink, so it holds
// for every connector without any of them knowing.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// exclusionFake emits two email activities per sync: one from a personal
// domain (which a seeded rule excludes) and one from a work domain (which no
// rule touches). It mirrors the real connectors' ErrSkip handling — a Sink
// skip is counted, never fatal.
type exclusionFake struct{ skipped, captured int }

func (m *exclusionFake) Descriptor() connector.Descriptor {
	return connector.Descriptor{
		// Persisted as capture_connection.provider → must be in the CAP-DDL-2 set;
		// the emitted rows keep their own 'graph' provenance (unconstrained).
		Name: "graph", Version: "1.0.0",
		Scopes:   []principal.Scope{principal.ScopeRead},
		RiskTier: mcp.TierAutoExecute,
		Produces: []datasource.EntityType{datasource.EntityActivity},
	}
}

func (m *exclusionFake) Authenticate(context.Context, connector.AuthRequest) (connector.Auth, error) {
	return connector.Auth("token"), nil
}

func (m *exclusionFake) Sync(ctx context.Context, _ connector.Auth, cursor connector.Cursor, sink connector.Sink) (connector.Cursor, error) {
	records := []connector.NormalizedRecord{
		{
			EntityType: datasource.EntityActivity,
			NaturalKey: connector.NaturalKey{SourceSystem: "graph", SourceID: "msg-personal"},
			Fields:     capture.ActivityFields{Kind: "email", Subject: "Dinner Sunday?", OccurredAt: fixedCaptureTime, Direction: "inbound"},
			Source:     "graph", CapturedBy: "connector:graph",
			Raw:   []byte(`{"provider":"graph","message_id":"msg-personal"}`),
			Match: connector.ExclusionAttrs{SenderDomain: "personal-family.example", RecipientDomains: []string{"myco.test"}},
		},
		{
			EntityType: datasource.EntityActivity,
			NaturalKey: connector.NaturalKey{SourceSystem: "graph", SourceID: "msg-work"},
			Fields:     capture.ActivityFields{Kind: "email", Subject: "Quote request", OccurredAt: fixedCaptureTime, Direction: "inbound"},
			Source:     "graph", CapturedBy: "connector:graph",
			Raw:   []byte(`{"provider":"graph","message_id":"msg-work"}`),
			Match: connector.ExclusionAttrs{SenderDomain: "acme.test", RecipientDomains: []string{"myco.test"}},
		},
	}
	for _, rec := range records {
		if _, err := sink.Upsert(ctx, rec); err != nil {
			if errors.Is(err, connector.ErrSkip) {
				m.skipped++
				continue
			}
			return cursor, err
		}
		m.captured++
	}
	return connector.Cursor(`{"done":true}`), nil // sync_cursor is jsonb
}

func (m *exclusionFake) Normalize(context.Context, connector.RawRecord) ([]connector.NormalizedRecord, error) {
	return nil, connector.ErrSkip
}

func (m *exclusionFake) HealthCheck(context.Context, connector.Auth) error { return nil }

func TestCaptureExclusionGateProducesZeroRowsAndOneSkipEvent(t *testing.T) {
	e := setupSearch(t)

	// Rep1 excludes their personal-family domain (RC-2).
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO capture_exclusion_rule (id, workspace_id, user_id, kind, value)
		 VALUES ($1, $2, $3, 'sender_domain', 'personal-family.example')`,
		ids.NewV7(), e.WS, e.Rep1); err != nil {
		t.Fatal(err)
	}

	// A Sink wired with the exclusion gate (as compose wires it in prod).
	sink := capture.NewSink(e.Pool).WithExclusions(capture.NewExclusions(e.Pool))
	registry := capture.NewRegistry(e.Pool, sink, fakeAuthority{}, newTestKeyvault(t, e))
	fake := &exclusionFake{}
	registry.Register(fake)

	grantCtx := e.humanWithScopes(e.Rep1, []principal.Scope{principal.ScopeRead})
	connID, err := registry.Connect(grantCtx, "graph", connector.Auth("token"))
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.SyncOnce(grantCtx, connID); err != nil {
		t.Fatal(err)
	}

	if fake.captured != 1 || fake.skipped != 1 {
		t.Fatalf("connector saw captured=%d skipped=%d, want 1 and 1", fake.captured, fake.skipped)
	}

	// The excluded message left ZERO rows; only the work message landed,
	// and only its raw original was stored.
	var activities, personalActs, raws int
	err = database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(context.Background(), `SELECT count(*) FROM activity WHERE source_system = 'graph'`).Scan(&activities); err != nil {
			return err
		}
		if err := tx.QueryRow(context.Background(), `SELECT count(*) FROM activity WHERE source_id = 'msg-personal'`).Scan(&personalActs); err != nil {
			return err
		}
		return tx.QueryRow(context.Background(), `SELECT count(*) FROM raw_capture WHERE source_system = 'graph'`).Scan(&raws)
	})
	if err != nil {
		t.Fatal(err)
	}
	if activities != 1 || personalActs != 0 || raws != 1 {
		t.Fatalf("exclusion leaked rows: activities=%d personal=%d raws=%d, want 1/0/1", activities, personalActs, raws)
	}

	// The skip trace: one entity-less capture.skipped{personal_exclusion}
	// event paired with one system_log 'capture_skip' row and NO audit_log
	// row — the machine-checkable AC1.3 proof.
	assertOneEntitylessSkip(t, e)

	// Replay: the guarantee that matters is that excluded mail stays
	// zero-rows and never sneaks in on a re-sync. (The skip event/audit are
	// at-least-once by design — excluded mail persists NO domain row to
	// dedupe on, so a bounded backfill re-emitting the skip is acceptable.)
	if err := registry.SyncOnce(grantCtx, connID); err != nil {
		t.Fatal(err)
	}
	var replayActs, replayPersonal int
	err = database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(context.Background(), `SELECT count(*) FROM activity WHERE source_system = 'graph'`).Scan(&replayActs); err != nil {
			return err
		}
		return tx.QueryRow(context.Background(), `SELECT count(*) FROM activity WHERE source_id = 'msg-personal'`).Scan(&replayPersonal)
	})
	if err != nil {
		t.Fatal(err)
	}
	if replayActs != 1 || replayPersonal != 0 {
		t.Fatalf("replay changed rows: activities=%d personal=%d, want 1/0 unchanged", replayActs, replayPersonal)
	}
}

// assertOneEntitylessSkip proves the reworked skip trace: exactly one
// capture.skipped{personal_exclusion} event, paired with one system_log
// 'capture_skip' row and NO audit_log row, where the event is entity-less
// and its ledger trace link is that system_log row (not a fabricated v5 id).
func assertOneEntitylessSkip(t *testing.T, e *searchEnv) {
	t.Helper()
	var skips, personalReason, systemSkips, auditSkips int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM event_outbox WHERE envelope->>'type' = 'capture.skipped'`).Scan(&skips); err != nil {
		t.Fatal(err)
	}
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM event_outbox WHERE envelope->>'type' = 'capture.skipped' AND envelope->'payload'->>'reason' = 'personal_exclusion'`).Scan(&personalReason); err != nil {
		t.Fatal(err)
	}
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM system_log WHERE action = 'capture_skip'`).Scan(&systemSkips); err != nil {
		t.Fatal(err)
	}
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE action = 'skip'`).Scan(&auditSkips); err != nil {
		t.Fatal(err)
	}
	if skips != 1 || personalReason != 1 || systemSkips != 1 || auditSkips != 0 {
		t.Fatalf("skip trace wrong: skipped=%d personal_reason=%d system_skips=%d audit_skips=%d, want 1/1/1/0",
			skips, personalReason, systemSkips, auditSkips)
	}

	var entityType, entityID, traceLedger, systemLogID string
	if err := e.owner.QueryRow(context.Background(),
		`SELECT envelope->'entity'->>'type', envelope->'entity'->>'id', envelope->'trace'->>'audit_log_id'
		 FROM event_outbox WHERE envelope->>'type' = 'capture.skipped'`).Scan(&entityType, &entityID, &traceLedger); err != nil {
		t.Fatal(err)
	}
	if err := e.owner.QueryRow(context.Background(),
		`SELECT id::text FROM system_log WHERE action = 'capture_skip'`).Scan(&systemLogID); err != nil {
		t.Fatal(err)
	}
	if entityType != "" || entityID != ids.Nil.String() {
		t.Fatalf("capture.skipped is not entity-less: entity type=%q id=%q", entityType, entityID)
	}
	if traceLedger != systemLogID {
		t.Fatalf("capture.skipped ledger link = %q, want the system_log row id %q", traceLedger, systemLogID)
	}
}
