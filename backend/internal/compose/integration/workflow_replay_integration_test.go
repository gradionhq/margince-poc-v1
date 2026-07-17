// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// AC-W6 (interfaces.md §5): a run's full persisted trace — the
// workflow_run row, its audit_log row, and its event_outbox event,
// linked by the correlation/causation ids the write shape stamps
// (storekit.Audit/Emit, events.md §2) — must let a reader RECONSTRUCT
// the firing: which trigger fired it, what it planned, what it applied,
// and its outcome. This suite fires ONE lead through the real domain
// write path (people.Store.CreateLead, which stages "lead.created" in
// event_outbox), reads that staged envelope back, and dispatches it via
// engine.HandleEvent — the exact call the cg:workflows redis subscriber
// makes once it decodes an envelope off the bus (cmd/worker's
// runSubscriber); no relay, no subscriber needed, since compose cannot
// import the redis client by architectural design (.go-arch-lint.yml's
// compose canUse list has no redis) and that transport hop is proven separately
// by platform/events' own bus integration test. The test then reads back
// NOTHING but the three persisted rows to rebuild the story, asserting
// it matches what the test itself orchestrated. A trace with a missing
// causation_id, a wrong audit_log_id, or a planned/applied encoding that
// does not name the real entity would leave this reconstruction with no
// row to find or a mismatched fact — it is not an assertion-free replay.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The two facts this suite's real firing produces, named once so the
// reconstruction asserts against the SAME strings the seeded starter
// (handlers_event.go's routeLeadCreateTask) and its create_task
// executor (activities.LogActivity) actually emit.
const (
	routeLeadHandler     = "route_lead"
	activityCapturedType = "activity.captured"
)

// replayedAction decodes one workflow_run planned/applied array entry.
// workflow.Action (workflow.go) carries no json tags, so its fields ride
// the wire under their Go names — PascalCase, not the snake_case every
// other wire shape in this codebase uses — and this type must match that
// exactly or every field silently zero-values instead of failing to parse.
type replayedAction struct {
	Kind   string
	Target struct {
		Type string
		ID   ids.UUID
	}
}

// workflowRunTrace is workflow_run's own persisted shape for one claimed
// (handler, key) row — read fresh from the database, never carried over
// from anything the test observed while the firing was in flight.
type workflowRunTrace struct {
	triggerEvent ids.UUID
	status       string
	detail       []byte
	planned      []replayedAction
	applied      []replayedAction
}

func TestWorkflowRunReplaysFromItsPersistedTrace(t *testing.T) {
	e := Setup(t)
	seedAllStarterAutomations(t, e)

	name := "Replay Trace Lead"
	lead, _, err := e.People.CreateLead(e.Admin(), people.CreateLeadInput{FullName: &name, Source: "manual"})
	if err != nil {
		t.Fatalf("seeding the trigger lead: %v", err)
	}
	leadID := ids.UUID(lead.Id)

	// Ground truth, read BEFORE dispatch runs: the real envelope
	// people.Store.CreateLead staged in event_outbox for this lead —
	// independent of anything the engine or this test later derives
	// from it. Dispatching THIS envelope (rather than a hand-built one)
	// is what makes the reconstruction below prove something: the run's
	// trigger_event must resolve back to this exact row.
	trigger := stagedTriggerEnvelope(t, e, leadCreatedEventType, leadID)

	// Dispatch: engine.HandleEvent is the synchronous cg:workflows
	// consumer call — see the file doc comment for why no bus rides
	// under this test.
	engine := compose.NewWorkflowEngine(e.Pool)
	if err := engine.HandleEvent(context.Background(), trigger); err != nil {
		t.Fatalf("dispatching the trigger lead's lead.created: %v", err)
	}

	// --- Everything from here on reads ONLY the persisted trace. ---

	run := readWorkflowRun(t, e, routeLeadHandler)
	if run.triggerEvent != trigger.EventID {
		t.Fatalf("workflow_run.trigger_event = %s, want the lead's own staged trigger %s", run.triggerEvent, trigger.EventID)
	}
	if run.status != "applied" || len(run.detail) != 0 {
		t.Fatalf("run outcome = status %q detail %q, want a clean applied run with no parked reason", run.status, run.detail)
	}

	// Which trigger fired it: reconstructed from the run row's OWN
	// trigger_event field, resolved back through event_outbox — not from
	// trigger above (that was this test's independent oracle).
	firedBy := envelopeByEventID(t, e, run.triggerEvent)
	if firedBy.Type != leadCreatedEventType || firedBy.Entity.ID != leadID {
		t.Fatalf("reconstructed trigger = %s on entity %s, want %s on lead %s", firedBy.Type, firedBy.Entity.ID, leadCreatedEventType, leadID)
	}

	// What it planned and applied: the run row's own action trace.
	for _, actions := range [][]replayedAction{run.planned, run.applied} {
		if len(actions) != 1 {
			t.Fatalf("run action trace has %d entries, want exactly the one create_task route_lead plans", len(actions))
		}
		got := actions[0]
		if got.Kind != "create_task" || got.Target.Type != "lead" || got.Target.ID != leadID {
			t.Fatalf("reconstructed action = %+v, want a create_task targeting lead %s", got, leadID)
		}
	}

	// The mutation this firing actually caused: the outbox event whose
	// trace.causation_id names the SAME trigger_event the run row
	// claims — the causal edge, not a coincidence of two events landing
	// near each other in time.
	mutation := envelopeCausedBy(t, e, run.triggerEvent, activityCapturedType)
	if mutation.Actor.Type != "system" {
		t.Fatalf("the applied mutation is attributed to actor type %q, want system", mutation.Actor.Type)
	}
	auditAction, auditEntityType, auditEntityID := readAuditRow(t, e, mutation.Trace.AuditLogID)
	if auditAction != "create" || auditEntityType != "activity" {
		t.Fatalf("linked audit row is %s/%s, want create/activity", auditAction, auditEntityType)
	}
	if auditEntityID != mutation.Entity.ID {
		t.Fatalf("event_outbox entity %s does not match its own trace.audit_log_id's audited entity %s", mutation.Entity.ID, auditEntityID)
	}
}

// stagedTriggerEnvelope resolves the real envelope a domain write staged
// for entityID — read before any dispatch runs, so it is independent
// ground truth rather than something this test invented in memory, and
// it is the exact payload engine.HandleEvent is asked to process (the
// same bytes a cg:workflows subscriber would have decoded off the bus).
func stagedTriggerEnvelope(t *testing.T, e *Env, eventType string, entityID ids.UUID) kevents.Envelope {
	t.Helper()
	return queryOneEnvelope(t, e,
		`SELECT envelope FROM event_outbox WHERE envelope->>'type' = $1 AND envelope->'entity'->>'id' = $2::text`,
		eventType, entityID.String())
}

// envelopeByEventID resolves an event id back to the outbox row it
// names — the read a reconstruction uses to learn what a bare
// workflow_run.trigger_event value actually was.
func envelopeByEventID(t *testing.T, e *Env, eventID ids.UUID) kevents.Envelope {
	t.Helper()
	return queryOneEnvelope(t, e,
		`SELECT envelope FROM event_outbox WHERE envelope->>'event_id' = $1`, eventID.String())
}

// envelopeCausedBy finds the one outbox event of eventType whose
// trace.causation_id names causation — the Apply step's own write,
// linked back to the trigger that caused it via the write shape's
// causation chain (storekit.Emit, events.md §2).
func envelopeCausedBy(t *testing.T, e *Env, causation ids.UUID, eventType string) kevents.Envelope {
	t.Helper()
	return queryOneEnvelope(t, e,
		`SELECT envelope FROM event_outbox WHERE envelope->>'type' = $1 AND envelope->'trace'->>'causation_id' = $2`,
		eventType, causation.String())
}

// queryOneEnvelope runs a single-row event_outbox query and decodes its
// envelope column. event_outbox carries no RLS (tenancy rides inside the
// envelope, per its own migration comment); WithWorkspaceTx is still
// used here so every read in this suite goes through the one sanctioned
// query path, matching the harness's own WsCount/WsExec convention.
func queryOneEnvelope(t *testing.T, e *Env, query string, args ...any) kevents.Envelope {
	t.Helper()
	var raw []byte
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), query, args...).Scan(&raw)
	})
	if err != nil {
		t.Fatalf("reading event_outbox: %v", err)
	}
	var env kevents.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decoding the outbox envelope: %v", err)
	}
	return env
}

// readWorkflowRun reads the ONE claimed run row for handler — a fresh
// per-test database (harness.Setup truncates) guarantees exactly one
// after a single lead fires route_lead's unconditional match.
func readWorkflowRun(t *testing.T, e *Env, handler string) workflowRunTrace {
	t.Helper()
	var trace workflowRunTrace
	var plannedRaw, appliedRaw []byte
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT trigger_event, status, detail, planned, applied FROM workflow_run WHERE handler = $1`,
			handler).Scan(&trace.triggerEvent, &trace.status, &trace.detail, &plannedRaw, &appliedRaw)
	})
	if err != nil {
		t.Fatalf("reading the %s run row: %v", handler, err)
	}
	trace.planned = decodeActions(t, plannedRaw)
	trace.applied = decodeActions(t, appliedRaw)
	return trace
}

func decodeActions(t *testing.T, raw []byte) []replayedAction {
	t.Helper()
	var actions []replayedAction
	if err := json.Unmarshal(raw, &actions); err != nil {
		t.Fatalf("decoding the run's action trace %s: %v", raw, err)
	}
	return actions
}

// readAuditRow resolves an audit_log_id (trace.audit_log_id, the write
// shape's own link) to the row's own action/entity_type/entity_id.
func readAuditRow(t *testing.T, e *Env, id ids.UUID) (action, entityType string, entityID ids.UUID) {
	t.Helper()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT action, entity_type, entity_id FROM audit_log WHERE id = $1`, id).Scan(&action, &entityType, &entityID)
	})
	if err != nil {
		t.Fatalf("resolving trace.audit_log_id %s to its audit row: %v", id, err)
	}
	return action, entityType, entityID
}
