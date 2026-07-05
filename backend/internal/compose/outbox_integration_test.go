// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose_test

// The write-shape invariant on the wire (B-EP04.5): an authenticated HTTP
// write stages exactly one outbox envelope, complete per events.md §2 —
// server-minted event_id/correlation_id, the actor from the session (never
// the request body), and trace.audit_log_id pointing at the audit row the
// same transaction committed.

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
)

// stagedEnvelope is one event_outbox row a write staged.
type stagedEnvelope struct {
	stream string
	env    events.Envelope
}

// stagedPersonCreated reads the person.created rows out of the outbox,
// asserting the single HTTP mutation staged exactly one. Bootstrap
// itself stages config events (pipeline.created from the C5 seed); the
// write-shape assertion is about the PERSON mutation alone.
func stagedPersonCreated(t *testing.T, owner *pgx.Conn) stagedEnvelope {
	t.Helper()
	rows, err := owner.Query(t.Context(),
		`SELECT stream, envelope FROM event_outbox WHERE envelope->>'type' = 'person.created' ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	all, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (stagedEnvelope, error) {
		var s stagedEnvelope
		var raw []byte
		if err := row.Scan(&s.stream, &raw); err != nil {
			return s, err
		}
		return s, json.Unmarshal(raw, &s.env)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("one mutation staged %d person.created rows, want exactly 1", len(all))
	}
	return all[0]
}

func TestWriteStagesOneCompleteEnvelope(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	var me anyMap
	if status := e.call(t, "GET", "/v1/me", nil, nil, &me); status != http.StatusOK {
		t.Fatalf("/me status = %d", status)
	}

	var person anyMap
	if status := e.call(t, "POST", "/v1/people", anyMap{
		"full_name": "Grace Hopper",
		"emails":    []anyMap{{"email": "grace@example.com"}},
	}, nil, &person); status != http.StatusCreated {
		t.Fatalf("create person status = %d, body %v", status, person)
	}

	owner, err := pgx.Connect(t.Context(), os.Getenv("MARGINCE_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})

	got := stagedPersonCreated(t, owner)
	if got.stream != "gw:events:crm:person" {
		t.Errorf("staged on %s, want gw:events:crm:person", got.stream)
	}
	if err := got.env.Validate(); err != nil {
		t.Errorf("staged envelope fails its own contract: %v", err)
	}
	if got.env.Type != "person.created" || got.env.Version != 1 {
		t.Errorf("type/version = %s/%d, want person.created/1", got.env.Type, got.env.Version)
	}
	personID, _ := person["id"].(string)
	if got.env.Entity.Type != "person" || got.env.Entity.ID.String() != personID {
		t.Errorf("entity ref %+v does not name the created person %v", got.env.Entity, personID)
	}
	adminID, _ := me["user"].(map[string]any)["id"].(string)
	if got.env.Actor.Type != "human" || got.env.Actor.ID != "human:"+adminID {
		t.Errorf("actor %+v is not the authenticated admin %q (provenance must come from the session)", got.env.Actor, adminID)
	}
	if got.env.Trace.CausationID != nil {
		t.Error("a direct API write starts a chain; causation_id must be null")
	}

	// The trace must point at the audit row the same transaction wrote.
	var auditAction, auditEntity string
	err = owner.QueryRow(t.Context(),
		`SELECT action, entity_type FROM audit_log WHERE id = $1`,
		got.env.Trace.AuditLogID).Scan(&auditAction, &auditEntity)
	if err != nil {
		t.Fatalf("trace.audit_log_id %s resolves to no audit row: %v", got.env.Trace.AuditLogID, err)
	}
	if auditAction != "create" || auditEntity != "person" {
		t.Errorf("linked audit row is %s/%s, want create/person", auditAction, auditEntity)
	}
}

func TestFailedLoginIsAuditedAndThrottled(t *testing.T) {
	e := setup(t)
	if status := e.call(t, "POST", "/v1/workspaces", anyMap{
		"workspace_name":     "Fable E2E",
		"admin_email":        "ada@example.com",
		"admin_display_name": "Ada Admin",
		"admin_password":     "correct-horse-battery",
	}, nil, nil); status != http.StatusCreated {
		t.Fatalf("bootstrap status = %d", status)
	}

	if status := e.call(t, "POST", "/v1/auth/login", anyMap{
		"email": "ada@example.com", "password": "wrong-password-entirely",
	}, nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("bad login status = %d, want 401", status)
	}

	owner, err := pgx.Connect(t.Context(), os.Getenv("MARGINCE_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})
	var failed int
	if err := owner.QueryRow(t.Context(),
		`SELECT count(*) FROM audit_log WHERE action = 'login' AND evidence->>'outcome' = 'failed'`).Scan(&failed); err != nil {
		t.Fatal(err)
	}
	if failed != 1 {
		t.Fatalf("failed-login audit rows = %d, want 1", failed)
	}

	// The per-email window admits 10/min; the 11th answers 429 before
	// any Argon2 work runs.
	last := 0
	for range 10 {
		last = e.call(t, "POST", "/v1/auth/login", anyMap{
			"email": "ada@example.com", "password": "wrong-password-entirely",
		}, nil, nil)
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("11th attempt inside the window = %d, want 429", last)
	}
}
