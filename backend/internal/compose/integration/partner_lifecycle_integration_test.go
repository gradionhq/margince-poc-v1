// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The partner lifecycle fields over HTTP (A41/ADR-0032 + the partner-desk
// working surface): PUT /organizations/{id}/partner carries the stage,
// next-step, segments, and gate metrics; the round-trip read returns
// exactly what was written; and a stage outside the closed lifecycle
// vocabulary is the seam's 422, never the DB CHECK's 500.

import (
	"net/http"
	"testing"
)

// partnerWire is the contract Partner shape as this suite reads it.
type partnerWire struct {
	OrganizationID    string         `json:"organization_id"`
	CertStatus        string         `json:"cert_status"`
	PartnerRole       string         `json:"partner_role"`
	RelationshipStage string         `json:"relationship_stage"`
	NextStep          *string        `json:"next_step"`
	NextStepDueAt     *string        `json:"next_step_due_at"`
	ServedSegments    []string       `json:"served_segments"`
	GateMetrics       map[string]int `json:"gate_metrics"`
	Version           int64          `json:"version"`
}

func TestPartnerLifecycleFieldsRoundTrip(t *testing.T) {
	e := setupRelationships(t)

	// Upsert with the full lifecycle block.
	var upserted partnerWire
	if status := e.call(t, "PUT", "/v1/organizations/"+e.orgID+"/partner", anyMap{
		"partner_role":       "consulting",
		"cert_status":        "applied",
		"relationship_stage": "in_conversation",
		"next_step":          "Send the partnership one-pager",
		"next_step_due_at":   "2026-08-01",
		"served_segments":    []string{"manufacturing", "fintech"},
		"gate_metrics":       anyMap{"certified_staff": 4, "retention_rate": 87},
	}, nil, &upserted); status != http.StatusOK {
		t.Fatalf("upsert partner with lifecycle fields → %d", status)
	}
	if upserted.RelationshipStage != "in_conversation" {
		t.Fatalf("upsert answered stage %q, want in_conversation", upserted.RelationshipStage)
	}

	// The read-back returns exactly what was written.
	var fetched partnerWire
	if status := e.call(t, "GET", "/v1/organizations/"+e.orgID+"/partner", nil, nil, &fetched); status != http.StatusOK {
		t.Fatalf("get partner → %d", status)
	}
	if fetched.OrganizationID != e.orgID || fetched.PartnerRole != "consulting" || fetched.CertStatus != "applied" {
		t.Fatalf("round-trip identity drifted: %+v", fetched)
	}
	if fetched.RelationshipStage != "in_conversation" {
		t.Fatalf("relationship_stage read back as %q, want in_conversation", fetched.RelationshipStage)
	}
	if fetched.NextStep == nil || *fetched.NextStep != "Send the partnership one-pager" {
		t.Fatalf("next_step read back as %v", fetched.NextStep)
	}
	if fetched.NextStepDueAt == nil || *fetched.NextStepDueAt != "2026-08-01" {
		t.Fatalf("next_step_due_at read back as %v, want the 2026-08-01 date", fetched.NextStepDueAt)
	}
	if len(fetched.ServedSegments) != 2 || fetched.ServedSegments[0] != "manufacturing" || fetched.ServedSegments[1] != "fintech" {
		t.Fatalf("served_segments read back as %v", fetched.ServedSegments)
	}
	if fetched.GateMetrics["certified_staff"] != 4 || fetched.GateMetrics["retention_rate"] != 87 {
		t.Fatalf("gate_metrics read back as %v, want certified_staff 4 / retention_rate 87", fetched.GateMetrics)
	}

	// A stage outside the closed lifecycle vocabulary is refused at the
	// seam — 422, and the stored stage stands.
	if status := e.call(t, "PUT", "/v1/organizations/"+e.orgID+"/partner", anyMap{
		"partner_role":       "consulting",
		"relationship_stage": "best_friends",
	}, map[string]string{"If-Match": "1"}, nil); status != 422 {
		t.Fatalf("unknown relationship_stage → %d, want 422", status)
	}
	var after partnerWire
	if status := e.call(t, "GET", "/v1/organizations/"+e.orgID+"/partner", nil, nil, &after); status != http.StatusOK {
		t.Fatalf("get partner after refusal → %d", status)
	}
	if after.RelationshipStage != "in_conversation" {
		t.Fatalf("a refused stage write landed anyway: %q", after.RelationshipStage)
	}
}
