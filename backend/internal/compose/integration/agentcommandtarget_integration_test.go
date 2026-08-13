// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// A REST archive of a record type the archive_record TOOL cannot even express
// still stages against the row it names.
//
// `tag` is outside that tool's declared enum, and outside the record seam's
// vocabulary too — nothing the tool door can reach describes it. The REST door
// archives it all the same, so it is the operation, not the tool's schema, that
// decides what a governed call is about. The proof has to be the approval ROW:
// ErrRequiresApproval comes back from a refusal with nowhere to land as
// readily as from a staged one, and a target_entity_id that is merely a
// well-formed uuid is not the record anybody will decide about.

import (
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
)

func TestARestArchiveOutsideTheToolSchemaStagesTheRowItNames(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)
	bearer := agentBearer(t, e, "outside-the-tool-schema agent")

	tagID := createdID(t, e, "/v1/tags", apptest.AnyMap{"name": "Champion"})

	var problem struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	if status := e.Call(t, "DELETE", "/v1/tags/"+tagID, nil, bearer, &problem); status != http.StatusForbidden ||
		problem.Code != "approval_required" {
		t.Fatalf("agent tag archive → %d %q, want 403 approval_required", status, problem.Code)
	}
	approvalID := ExtractStagedApprovalID(t, problem.Detail)

	var targetType string
	var targetID *string
	if err := e.Owner.QueryRow(t.Context(),
		`SELECT coalesce(target_entity_type, ''), target_entity_id FROM approval WHERE id = $1`,
		approvalID).Scan(&targetType, &targetID); err != nil {
		t.Fatalf("reading the staged approval: %v", err)
	}
	if targetType != "tag" {
		t.Errorf("staged target_entity_type = %q, want \"tag\" — the approvals surface scopes an inbox row "+
			"by probing its target's visibility, and it cannot probe a type it was not told", targetType)
	}
	if targetID == nil {
		t.Fatal("the staged approval names no target id — a decision about which tag was never captured")
	}

	// The id is the tag the agent asked to archive, not merely a uuid: the
	// staged row is what a human decides from, and a target that resolves to
	// nothing is a decision about nothing.
	var name string
	if err := e.Owner.QueryRow(t.Context(),
		`SELECT name FROM tag WHERE id = $1 AND archived_at IS NULL`, *targetID).Scan(&name); err != nil {
		t.Fatalf("the staged target id %s resolves to no live tag (%v)", *targetID, err)
	}
	if name != "Champion" {
		t.Errorf("the staged target is the tag named %q, want \"Champion\"", name)
	}
	if *targetID != tagID {
		t.Errorf("staged target_entity_id = %s, want %s", *targetID, tagID)
	}
}
