// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The write-back GOVERNANCE story, proven over the real HTTP surface and a
// real migrated Postgres: a human write against a workspace in overlay mode
// leaves the same attributable trail a native write leaves.
//
// This is the repo's non-negotiable write shape applied to a system of
// record we do not own. The canonical row lives at the incumbent, so the
// literal "domain row + audit_log + event_outbox in ONE transaction" is not
// available — but everything that IS local commits together, and a mutation
// never lands with no record of who made it. Without that, the overlay path
// would be the one mutation surface in this build where "who changed this
// record" has no answer, and the audit screen and exports could not replay
// what happened.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// auditRow is one audit_log row's governance-relevant columns.
type auditRow struct {
	action     string
	entityType string
	actorType  string
	actorID    string
	evidence   []byte
}

// auditRowsFor reads every audit_log row recorded for one entity, newest
// last. It reads through the owner pool deliberately: the assertion is that
// the row EXISTS and names the right principal, which must not depend on the
// reader's own row scope.
func auditRowsFor(t *testing.T, e *env, entityType, entityID string) []auditRow {
	t.Helper()
	rows, err := e.owner.Query(context.Background(), `
		SELECT action, entity_type, actor_type, actor_id, coalesce(evidence, '{}'::jsonb)
		  FROM audit_log
		 WHERE entity_type = $1 AND entity_id = $2
		 ORDER BY id`, entityType, entityID)
	if err != nil {
		t.Fatalf("reading audit_log for %s/%s: %v", entityType, entityID, err)
	}
	defer rows.Close()

	var out []auditRow
	for rows.Next() {
		var r auditRow
		if err := rows.Scan(&r.action, &r.entityType, &r.actorType, &r.actorID, &r.evidence); err != nil {
			t.Fatalf("scanning an audit_log row: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating audit_log rows: %v", err)
	}
	return out
}

// outboxTypesFor reads the event types staged for one entity.
func outboxTypesFor(t *testing.T, e *env, entityType, entityID string) []string {
	t.Helper()
	rows, err := e.owner.Query(context.Background(), `
		SELECT envelope->>'type'
		  FROM event_outbox
		 WHERE envelope->'entity'->>'type' = $1 AND envelope->'entity'->>'id' = $2
		 ORDER BY created_at, id`, entityType, entityID)
	if err != nil {
		t.Fatalf("reading event_outbox for %s/%s: %v", entityType, entityID, err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var eventType string
		if err := rows.Scan(&eventType); err != nil {
			t.Fatalf("scanning an event_outbox row: %v", err)
		}
		out = append(out, eventType)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating event_outbox rows: %v", err)
	}
	return out
}

// TestOverlayUpdateWritesTheAuditTrail proves an ordinary REST update in
// overlay mode records the mutation: an audit_log row attributed to the
// authenticated human, and the same public event a native update emits.
func TestOverlayUpdateWritesTheAuditTrail(t *testing.T) {
	e := setupOverlayWrite(t)
	e.seed(t, "deal", "9201", map[string]any{"name": "Acme Renewal", "currency": "USD"})
	id := firstListedID(t, e.env, "/v1/deals")

	var deal crmcontracts.Deal
	if status := e.call(t, "PATCH", "/v1/deals/"+id, anyMap{"name": "Acme Renewal — Q3"}, nil, &deal); status != http.StatusOK {
		t.Fatalf("PATCH /v1/deals/%s = %d", id, status)
	}

	// captured_by is required on every entity schema. An overlay-served body
	// that omits it is schema-invalid, and a validating client rejects it —
	// so the write response is as good a place to pin it as the read.
	if deal.CapturedBy == nil || *deal.CapturedBy == "" {
		t.Errorf("overlay deal body captured_by = %v, want the required provenance value", deal.CapturedBy)
	}

	audits := auditRowsFor(t, e.env, "deal", id)
	if len(audits) != 1 {
		t.Fatalf("audit_log rows for the updated deal = %d, want exactly 1 — an overlay write must leave the same trail a native write leaves", len(audits))
	}
	got := audits[0]
	if got.action != "update" {
		t.Errorf("audit action = %q, want %q", got.action, "update")
	}
	if got.actorType != "human" {
		t.Errorf("audit actor_type = %q, want %q — the write must be attributed to the authenticated principal, never to a connector or the mirror", got.actorType, "human")
	}
	if got.actorID == "" {
		t.Error("audit actor_id is empty — the row cannot answer who made the change")
	}
	// The incumbent's own record id rides evidence, not the field images:
	// it is context ABOUT the mutation, and a field-history projection
	// reading it out of before/after would report a change that never
	// happened on the record.
	if !containsJSONKey(got.evidence, "external_id") {
		t.Errorf("audit evidence = %s, want it to name the incumbent record the write landed on", got.evidence)
	}

	if types := outboxTypesFor(t, e.env, "deal", id); len(types) != 1 || types[0] != "deal.updated" {
		t.Errorf("staged events = %v, want exactly [deal.updated] — a subscriber must not need to know which system of record served the write", types)
	}
}

// TestOverlayArchiveWritesTheAuditTrail proves the archive verb records the
// mutation too. It matters more here than for update, not less: the record
// is destroyed in the customer's own CRM, and the mirror purge that follows
// writes only a derived-cache health event.
func TestOverlayArchiveWritesTheAuditTrail(t *testing.T) {
	e := setupOverlayWrite(t)
	e.seed(t, "person", "9202", map[string]any{"first_name": "Ada", "last_name": "Overlay"})
	id := firstListedID(t, e.env, "/v1/people")

	if status := e.call(t, "DELETE", "/v1/people/"+id, nil, nil, nil); status != http.StatusOK {
		t.Fatalf("DELETE /v1/people/%s = %d", id, status)
	}

	audits := auditRowsFor(t, e.env, "person", id)
	if len(audits) != 1 {
		t.Fatalf("audit_log rows for the archived person = %d, want exactly 1 — a record removed from the customer's CRM with no audit row has no answer to who removed it", len(audits))
	}
	if audits[0].action != "archive" {
		t.Errorf("audit action = %q, want %q", audits[0].action, "archive")
	}
	if audits[0].actorType != "human" {
		t.Errorf("audit actor_type = %q, want %q", audits[0].actorType, "human")
	}

	types := outboxTypesFor(t, e.env, "person", id)
	if !containsString(types, "person.archived") {
		t.Errorf("staged events = %v, want them to include person.archived", types)
	}
}

// TestOverlayRefusedWriteLeavesNoAuditTrail is the other half of the
// invariant: the trail records what HAPPENED. A write the provider refuses
// never reached the incumbent, so it must leave no audit row claiming it
// did — an audit log that records attempts as changes is worse than none.
func TestOverlayRefusedWriteLeavesNoAuditTrail(t *testing.T) {
	e := setupOverlayWrite(t)
	e.seed(t, "lead", "9203", map[string]any{"full_name": "Refused Lead"})
	id := firstListedID(t, e.env, "/v1/leads")

	// Archive is not a verb the mirror serves for a lead.
	if status := e.call(t, "DELETE", "/v1/leads/"+id, nil, nil, nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("DELETE /v1/leads/%s = %d, want 422 unsupported_by_sor", id, status)
	}
	if audits := auditRowsFor(t, e.env, "lead", id); len(audits) != 0 {
		t.Errorf("audit_log rows after a refused archive = %d, want 0 — a refusal is not a mutation", len(audits))
	}
}

// containsJSONKey reports whether a jsonb column names key at its top level.
func containsJSONKey(raw []byte, key string) bool {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return false
	}
	_, ok := obj[key]
	return ok
}

// containsString reports whether values contains want.
func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
