// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The undo half of the migrate-in surface, end to end over real HTTP and a
// real Postgres: upload, approve, undo — proving IEM-WIRE-9 and A93 against
// the actual wiring (csvWriters.Reverse through people.Store), not a fake.

import (
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
)

type importUndoKeptDTO struct {
	Object string `json:"object"`
	ID     string `json:"id"`
}

type importUndoErroredDTO struct {
	Object string `json:"object"`
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

type importUndoReportDTO struct {
	RunID         string                 `json:"run_id"`
	Status        string                 `json:"status"`
	ReversedCount int                    `json:"reversed_count"`
	Kept          []importUndoKeptDTO    `json:"kept"`
	Errored       []importUndoErroredDTO `json:"errored"`
}

type importReportWithUndoDTO struct {
	importReportDTO
	Undo *importUndoReportDTO `json:"undo"`
}

type leadRowDTO struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

func leadRows(t *testing.T, e *apptest.AppEnv) []leadRowDTO {
	t.Helper()
	var leads struct {
		Data []leadRowDTO `json:"data"`
	}
	if status := e.Call(t, http.MethodGet, "/v1/leads?limit=100", nil, nil, &leads); status != http.StatusOK {
		t.Fatalf("GET /v1/leads → %d, want 200", status)
	}
	return leads.Data
}

// A93 in one test: undo reverses the rows nobody touched and leaves the one a
// human edited exactly as they left it, named in the report rather than
// silently skipped or silently overwritten back.
func TestCSVImportUndoReversesUntouchedAndKeepsEdited(t *testing.T) {
	e := setupImportApp(t)

	profile, _ := uploadCSV(t, e, "lead", prospectCSV)
	run, _ := createRunWithMapping(t, e, "lead", profile.SourceRef, profile.SuggestedMapping)
	if status := e.Call(t, http.MethodPost, "/v1/imports/"+run.ID+"/approve", nil, nil, nil); status != http.StatusAccepted {
		t.Fatalf("approve → %d, want 202", status)
	}
	if got := leadCount(t, e); got != 3 {
		t.Fatalf("leads after approval = %d, want 3", got)
	}

	// Grace's row is edited by a human after the import lands — the one row
	// undo must leave alone.
	var edited leadRowDTO
	for _, l := range leadRows(t, e) {
		if l.Email == "grace@hopper.example" {
			edited = l
		}
	}
	if edited.ID == "" {
		t.Fatal("could not find the imported Grace Hopper lead to edit")
	}
	var patched struct {
		FullName string `json:"full_name"`
	}
	if status := e.Call(t, http.MethodPatch, "/v1/leads/"+edited.ID,
		map[string]any{"full_name": "Grace M. Hopper"}, nil, &patched); status != http.StatusOK {
		t.Fatalf("editing the lead as a human → %d, want 200", status)
	}

	var undone importRunDTO
	if status := e.Call(t, http.MethodPost, "/v1/imports/"+run.ID+"/undo", nil, nil, &undone); status != http.StatusAccepted {
		t.Fatalf("undo → %d, want 202", status)
	}
	if undone.Status != "undone" {
		t.Fatalf("run status after undo = %q, want undone", undone.Status)
	}

	var report importReportWithUndoDTO
	if status := e.Call(t, http.MethodGet, "/v1/imports/"+run.ID+"/report", nil, nil, &report); status != http.StatusOK {
		t.Fatalf("report after undo → %d, want 200", status)
	}
	if report.Undo == nil {
		t.Fatal("the report carries no undo outcome after the run was undone")
	}
	if report.Undo.ReversedCount != 2 {
		t.Fatalf("reversed_count = %d, want 2 (Ada and Katherine, not Grace)", report.Undo.ReversedCount)
	}
	if len(report.Undo.Kept) != 1 || report.Undo.Kept[0].ID != edited.ID || report.Undo.Kept[0].Object != "lead" {
		t.Fatalf("kept = %+v, want exactly Grace's lead named", report.Undo.Kept)
	}

	// The estate itself, not just the report: two leads gone from the live
	// list, Grace's still there with the human's edit intact.
	remaining := leadRows(t, e)
	if len(remaining) != 1 || remaining[0].ID != edited.ID {
		t.Fatalf("live leads after undo = %+v, want only Grace's", remaining)
	}
	var current struct {
		FullName string `json:"full_name"`
	}
	if status := e.Call(t, http.MethodGet, "/v1/leads/"+edited.ID, nil, nil, &current); status != http.StatusOK {
		t.Fatalf("reading the kept lead → %d, want 200", status)
	}
	if current.FullName != "Grace M. Hopper" {
		t.Fatalf("kept lead full_name = %q, want the human's edit left in place", current.FullName)
	}

	// Undoing an already-undone run is a conflict, not a no-op.
	if status := e.Call(t, http.MethodPost, "/v1/imports/"+run.ID+"/undo", nil, nil, nil); status != http.StatusConflict {
		t.Fatalf("second undo → %d, want 409", status)
	}
}

// Undo is reachable only once a run has actually committed — an
// awaiting_approval run has created nothing yet, so there is nothing to undo.
func TestCSVImportUndoRefusesBeforeTheRunCommits(t *testing.T) {
	e := setupImportApp(t)

	profile, _ := uploadCSV(t, e, "lead", prospectCSV)
	run, _ := createRunWithMapping(t, e, "lead", profile.SourceRef, profile.SuggestedMapping)

	if status := e.Call(t, http.MethodPost, "/v1/imports/"+run.ID+"/undo", nil, nil, nil); status != http.StatusConflict {
		t.Fatalf("undo of an awaiting_approval run → %d, want 409", status)
	}
}

const orgProspectCSV = "Company,Legal Name,Industry\n" +
	"Initech,Initech GmbH,software\n" +
	"Umbrella,Umbrella AG,biotech\n"

func organizationRows(t *testing.T, e *apptest.AppEnv) []leadRowDTO {
	t.Helper()
	var orgs struct {
		Data []leadRowDTO `json:"data"`
	}
	if status := e.Call(t, http.MethodGet, "/v1/organizations?limit=100", nil, nil, &orgs); status != http.StatusOK {
		t.Fatalf("GET /v1/organizations → %d, want 200", status)
	}
	return orgs.Data
}

// Reverse's other object branch: an import of organizations undoes through
// ArchiveOrganization exactly as a lead import undoes through DisqualifyLead.
func TestCSVImportUndoReversesOrganizations(t *testing.T) {
	e := setupImportApp(t)
	before := len(organizationRows(t, e))

	profile, _ := uploadCSV(t, e, "organization", orgProspectCSV)
	mapping := map[string]string{"Company": "display_name", "Legal Name": "legal_name", "Industry": "industry"}
	run, status := createRunWithMapping(t, e, "organization", profile.SourceRef, mapping)
	if status != http.StatusAccepted {
		t.Fatalf("create run → %d, want 202", status)
	}
	if status := e.Call(t, http.MethodPost, "/v1/imports/"+run.ID+"/approve", nil, nil, nil); status != http.StatusAccepted {
		t.Fatalf("approve → %d, want 202", status)
	}
	if got := len(organizationRows(t, e)); got != before+2 {
		t.Fatalf("organizations after approval = %d, want %d", got, before+2)
	}

	var undone importRunDTO
	if status := e.Call(t, http.MethodPost, "/v1/imports/"+run.ID+"/undo", nil, nil, &undone); status != http.StatusAccepted {
		t.Fatalf("undo → %d, want 202", status)
	}
	var report importReportWithUndoDTO
	if status := e.Call(t, http.MethodGet, "/v1/imports/"+run.ID+"/report", nil, nil, &report); status != http.StatusOK {
		t.Fatalf("report after undo → %d, want 200", status)
	}
	if report.Undo == nil || report.Undo.ReversedCount != 2 || len(report.Undo.Kept) != 0 {
		t.Fatalf("undo report = %+v, want both organizations reversed and none kept", report.Undo)
	}
	if got := len(organizationRows(t, e)); got != before {
		t.Fatalf("organizations after undo = %d, want %d (both reversed)", got, before)
	}
}

// A row a human has touched by ANY action — not narrowly an 'update' —
// must be kept, not reversed: here, a human disqualified the lead directly,
// outside the import. DisqualifyLead audits action='archive', and the
// human-touch check catches any human actor's audit row on the entity, so
// this lands in `kept` rather than being reversed out from under the human
// who touched it.
func TestCSVImportUndoKeepsARowAHumanDisqualifiedOutsideTheImport(t *testing.T) {
	e := setupImportApp(t)

	profile, _ := uploadCSV(t, e, "lead", "Email,Full Name\nada@lovelace.example,Ada Lovelace\n")
	run, status := createRunWithMapping(t, e, "lead", profile.SourceRef, profile.SuggestedMapping)
	if status != http.StatusAccepted {
		t.Fatalf("create run → %d, want 202", status)
	}
	if status := e.Call(t, http.MethodPost, "/v1/imports/"+run.ID+"/approve", nil, nil, nil); status != http.StatusAccepted {
		t.Fatalf("approve → %d, want 202", status)
	}
	rows := leadRows(t, e)
	if len(rows) != 1 {
		t.Fatalf("leads after approval = %d, want 1", len(rows))
	}

	// Disqualify (archive) the lead directly — a human action, but not an
	// 'update'.
	if status := e.Call(t, http.MethodDelete, "/v1/leads/"+rows[0].ID, nil, nil, nil); status != http.StatusOK {
		t.Fatalf("disqualify → %d, want 200", status)
	}

	var undone importRunDTO
	if status := e.Call(t, http.MethodPost, "/v1/imports/"+run.ID+"/undo", nil, nil, &undone); status != http.StatusAccepted {
		t.Fatalf("undo → %d, want 202", status)
	}
	var report importReportWithUndoDTO
	if status := e.Call(t, http.MethodGet, "/v1/imports/"+run.ID+"/report", nil, nil, &report); status != http.StatusOK {
		t.Fatalf("report after undo → %d, want 200", status)
	}
	if report.Undo == nil || report.Undo.ReversedCount != 0 || len(report.Undo.Kept) != 1 || report.Undo.Kept[0].ID != rows[0].ID {
		t.Fatalf("undo report = %+v, want the disqualified row kept, not reversed", report.Undo)
	}
}
