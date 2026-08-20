// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Limiting a message AFTER the derived read models were built must move the
// models too (#1877, #1885): the interaction-edge projection stops crediting
// the conversation to the global graph, the derived signals citing the message
// narrow to the capture owner, and the thread becomes due for a fresh
// extraction pass under the audience as it now stands.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/modules/signals"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestALimitedMessageLeavesTheInteractionGraph(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	ctx := context.Background()
	person := e.SeedPerson(t, "Grace Counterparty", &e.Rep1)

	activity := ids.NewV7()
	if _, err := owner.Exec(ctx, `
		INSERT INTO activity (id, kind, subject, occurred_at, direction, source, captured_by)
		VALUES ($1, 'email', 'quarterly numbers', now(), 'outbound', 'manual', 'human:x')`, activity); err != nil {
		t.Fatal(err)
	}
	LinkActivity(t, owner, activity, "person", person)
	for _, seed := range []struct {
		column string
		id     ids.UUID
		role   string
	}{{"user_id", e.Rep1, "from"}, {"person_id", person, "to"}} {
		if _, err := owner.Exec(ctx, `
			INSERT INTO activity_participant (activity_id, `+seed.column+`, role) VALUES ($1, $2, $3)`,
			activity, seed.id, seed.role); err != nil {
			t.Fatal(err)
		}
	}
	wsCtx := principal.WithWorkspaceID(ctx, e.WS)
	refold := func() {
		t.Helper()
		if err := database.WithWorkspaceTx(wsCtx, e.Pool, func(tx pgx.Tx) error {
			return search.RecomputeEdgesForActivities(wsCtx, tx, []ids.UUID{activity})
		}); err != nil {
			t.Fatal(err)
		}
	}
	edgeCount := func() int {
		t.Helper()
		var n int
		if err := owner.QueryRow(ctx, `SELECT count(*) FROM graph_interaction_edge
			WHERE user_id = $1 AND person_id = $2`, e.Rep1, person).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	refold()
	if edgeCount() != 1 {
		t.Fatal("the workspace-audience exchange folded no edge — the fixture proves nothing")
	}

	if _, err := owner.Exec(ctx, `UPDATE activity SET audience = 'participants' WHERE id = $1`, activity); err != nil {
		t.Fatal(err)
	}
	refold()
	if edgeCount() != 0 {
		t.Error("the limited conversation still credits the global graph with who talked to whom")
	}
}

func TestAnAudienceChangeNarrowsTheDerivedSignalAndMakesTheThreadDue(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	ctx := context.Background()
	captureOwner := e.Rep1

	org := e.SeedOrg(t, "Acme GmbH", &e.Rep1)
	activity := ids.NewV7()
	if _, err := owner.Exec(ctx, `
		INSERT INTO activity (id, kind, subject, occurred_at, direction, thread_key, source, captured_by)
		VALUES ($1, 'email', 'renewal wobble', now(), 'inbound', 'thr-1', 'gmail', 'connector:gmail:`+captureOwner.String()+`')`,
		activity); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO signal_thread_scan (thread_key, last_activity_at)
		VALUES ('thr-1', now())`); err != nil {
		t.Fatal(err)
	}

	// The signal arrives through the real writer, workspace-visible.
	extractor := e.As(e.Rep1, []ids.UUID{e.Team1}, AdminPerms)
	if err := database.WithWorkspaceTx(extractor, e.Pool, func(tx pgx.Tx) error {
		written, err := signals.RecordDerived(extractor, tx, signals.DerivedSignal{
			Kind: "risk", OrganizationID: org, Summary: "renewal at risk per the thread",
			Severity: "warn", Fingerprint: "thr-1:risk",
			Evidence: []signals.DerivedEvidence{{Snippet: "we may not renew", ActivityID: activity}},
		}, time.Now().UTC())
		if err == nil && !written {
			t.Fatal("the fixture's signal was swallowed by its fingerprint — the test would prove nothing")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}

	payload, err := json.Marshal(map[string]any{"changed_fields": map[string]any{"audience": "participants"}})
	if err != nil {
		t.Fatal(err)
	}
	gen := compose.NewAudienceRescopeGen(e.Pool)
	if err := gen.HandleEvent(ctx, events.Envelope{
		Type:    "activity.updated",
		Entity:  events.EntityRef{Type: "activity", ID: activity},
		Payload: payload,
	}); err != nil {
		t.Fatalf("the audience-rescope consumer refused the event: %v", err)
	}

	var visibility string
	var signalOwner *ids.UUID
	if err := owner.QueryRow(ctx, `SELECT visibility, owner_id FROM signal
		WHERE fingerprint = 'thr-1:risk'`).Scan(&visibility, &signalOwner); err != nil {
		t.Fatal(err)
	}
	if visibility != "owner" || signalOwner == nil || *signalOwner != captureOwner {
		t.Errorf("signal after the limit = %s/%v, want owner-private to the capture owner %s — "+
			"the workspace-readable summary of a limited email survived", visibility, signalOwner, captureOwner)
	}
	var scans int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM signal_thread_scan WHERE thread_key = 'thr-1'`).Scan(&scans); err != nil {
		t.Fatal(err)
	}
	if scans != 0 {
		t.Error("the thread's scan watermark survived — the next extraction pass will never re-read it")
	}
}

// The two edge cases the narrow path must not mishandle: an agent-captured
// message names a passport, not a reader, so its signals ARCHIVE rather than
// failing the app_user FK; and a signal already private to a DIFFERENT owner
// that cites a newly limited message archives too — its summary now mixes
// correspondence two different people limited, and no one reader admits both.
func TestOwnerlessAndCrossOwnerCitationsArchiveTheSignal(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	ctx := context.Background()
	org := e.SeedOrg(t, "Acme GmbH", &e.Rep1)

	agentActivity := ids.NewV7()
	if _, err := owner.Exec(ctx, `
		INSERT INTO activity (id, kind, subject, occurred_at, direction, thread_key, source, captured_by)
		VALUES ($1, 'email', 'agent thread', now(), 'inbound', 'thr-agent', 'gmail', 'agent:`+ids.NewV7().String()+`')`,
		agentActivity); err != nil {
		t.Fatal(err)
	}
	rep1Activity := ids.NewV7()
	if _, err := owner.Exec(ctx, `
		INSERT INTO activity (id, kind, subject, occurred_at, direction, thread_key, source, captured_by)
		VALUES ($1, 'email', 'rep1 thread', now(), 'inbound', 'thr-cross', 'gmail', 'connector:gmail:`+e.Rep1.String()+`')`,
		rep1Activity); err != nil {
		t.Fatal(err)
	}

	extractor := e.As(e.Rep1, []ids.UUID{e.Team1}, AdminPerms)
	seed := func(fingerprint string, activity ids.UUID, privateTo ids.UUID) {
		t.Helper()
		if err := database.WithWorkspaceTx(extractor, e.Pool, func(tx pgx.Tx) error {
			_, err := signals.RecordDerived(extractor, tx, signals.DerivedSignal{
				Kind: "risk", OrganizationID: org, Summary: "s", Severity: "warn",
				Fingerprint: fingerprint, PrivateTo: privateTo,
				Evidence: []signals.DerivedEvidence{{Snippet: "x", ActivityID: activity}},
			}, time.Now().UTC())
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	seed("thr-agent:risk", agentActivity, ids.UUID{})
	seed("thr-cross:risk", rep1Activity, e.Rep2) // already private to a DIFFERENT reader

	gen := compose.NewAudienceRescopeGen(e.Pool)
	limit := func(activity ids.UUID) {
		t.Helper()
		payload, err := json.Marshal(map[string]any{"changed_fields": map[string]any{"audience": "participants"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := gen.HandleEvent(ctx, events.Envelope{
			Type: "activity.updated", Entity: events.EntityRef{Type: "activity", ID: activity}, Payload: payload,
		}); err != nil {
			t.Fatalf("rescope: %v", err)
		}
	}
	limit(agentActivity)
	limit(rep1Activity)

	for _, tc := range []struct {
		fingerprint string
		why         string
	}{
		{"thr-agent:risk", "an agent passport is not a reader a signal can answer to"},
		{"thr-cross:risk", "the summary mixes evidence limited to a different owner"},
	} {
		var archived *time.Time
		if err := owner.QueryRow(ctx, `SELECT archived_at FROM signal WHERE fingerprint = $1`, tc.fingerprint).Scan(&archived); err != nil {
			t.Fatal(err)
		}
		if archived == nil {
			t.Errorf("signal %s survived unarchived — %s", tc.fingerprint, tc.why)
		}
	}
}
