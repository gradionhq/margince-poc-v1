// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The migrate-in surface end to end: upload a file, map it, dry-run it,
// approve it — and the promises that make it safe to hand a customer's estate
// to. Every assertion here is one the chapter makes by number:
//
//	IEM-AC-7  — the dry run writes NOTHING, and bulk rows land as leads, not people
//	IEM-AC-9  — a re-run creates no duplicates
//	IEM-WIRE-5 — approval is valid only from awaiting_approval
//
// The counts are read back over HTTP rather than out of the pool, because what
// the customer sees is the list, not the table.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
)

type importColumnDTO struct {
	Header   string   `json:"header"`
	FillRate float64  `json:"fill_rate"`
	Samples  []string `json:"samples"`
}

type importProfileDTO struct {
	SourceRef        string            `json:"source_ref"`
	Object           string            `json:"object"`
	Columns          []importColumnDTO `json:"columns"`
	RowsProfiled     int               `json:"rows_profiled"`
	SuggestedMapping map[string]string `json:"suggested_mapping"`
	Targets          []string          `json:"targets"`
}

type importRunDTO struct {
	Id         string `json:"id"`
	Status     string `json:"status"`
	Connector  string `json:"connector"`
	Object     string `json:"object"`
	Checkpoint int    `json:"checkpoint"`
}

type importDispositionDTO struct {
	Created   int `json:"created"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`
	Skipped   int `json:"skipped"`
}

type importIssueDTO struct {
	Line   int    `json:"line"`
	Reason string `json:"reason"`
}

type importReportDTO struct {
	RunId         string               `json:"run_id"`
	Status        string               `json:"status"`
	RowsRead      int                  `json:"rows_read"`
	Disposition   importDispositionDTO `json:"disposition"`
	Issues        []importIssueDTO     `json:"issues"`
	SourceKeyUsed string               `json:"source_key_used"`
}

type leadListDTO struct {
	Data []struct {
		Email    string `json:"email"`
		FullName string `json:"full_name"`
		Title    string `json:"title"`
	} `json:"data"`
}

func setupImportApp(t *testing.T) *apptest.AppEnv {
	t.Helper()
	e, _ := setupImportAppWithStore(t)
	return e
}

// setupImportAppWithStore hands the suite the same object store the server
// holds, so a scenario can make an uploaded file disappear the way a retention
// sweep or an operator would.
func setupImportAppWithStore(t *testing.T) (*apptest.AppEnv, blobstore.Store) {
	t.Helper()
	store := blobstore.NewMemory()
	e := apptest.SetupAppWithOptions(t, compose.WithBlobstore(store))
	e.BootstrapWorkspace(t)
	return e, store
}

// uploadCSV posts one file to the upload operation and returns the profile.
func uploadCSV(t *testing.T, e *apptest.AppEnv, object, body string) (importProfileDTO, int) {
	t.Helper()
	var buf bytes.Buffer
	form := multipart.NewWriter(&buf)
	if err := form.WriteField("object", object); err != nil {
		t.Fatalf("writing the object field: %v", err)
	}
	part, err := form.CreateFormFile("file", "estate.csv")
	if err != nil {
		t.Fatalf("creating the file part: %v", err)
	}
	if _, err := part.Write([]byte(body)); err != nil {
		t.Fatalf("writing the file part: %v", err)
	}
	if err := form.Close(); err != nil {
		t.Fatalf("closing the form: %v", err)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, e.TS.URL+"/v1/imports/sources", &buf)
	if err != nil {
		t.Fatalf("building the upload: %v", err)
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	resp, err := e.Client.Do(req)
	if err != nil {
		t.Fatalf("uploading: %v", err)
	}
	defer apptest.CloseBody(t, resp)

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the upload response: %v", err)
	}
	var profile importProfileDTO
	if len(raw) > 0 && resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(raw, &profile); err != nil {
			t.Fatalf("decoding %q: %v", raw, err)
		}
	}
	return profile, resp.StatusCode
}

func createRun(t *testing.T, e *apptest.AppEnv, object string, p importProfileDTO) (importRunDTO, int) {
	t.Helper()
	var run importRunDTO
	status := e.Call(t, http.MethodPost, "/v1/imports", apptest.AnyMap{
		"connector": "csv", "object": object,
		"source_ref": p.SourceRef, "mapping": p.SuggestedMapping,
	}, nil, &run)
	return run, status
}

func leadCount(t *testing.T, e *apptest.AppEnv) int {
	t.Helper()
	var leads leadListDTO
	if status := e.Call(t, http.MethodGet, "/v1/leads?limit=100", nil, nil, &leads); status != http.StatusOK {
		t.Fatalf("GET /v1/leads → %d, want 200", status)
	}
	return len(leads.Data)
}

func importedPersonCount(t *testing.T, e *apptest.AppEnv) int {
	t.Helper()
	var people struct {
		Data []apptest.AnyMap `json:"data"`
	}
	if status := e.Call(t, http.MethodGet, "/v1/people?limit=100", nil, nil, &people); status != http.StatusOK {
		t.Fatalf("GET /v1/people → %d, want 200", status)
	}
	return len(people.Data)
}

const prospectCSV = "Email,Full Name,Title\n" +
	"ada@lovelace.example,Ada Lovelace,Analyst\n" +
	"grace@hopper.example,Grace Hopper,Rear Admiral\n" +
	"katherine@johnson.example,Katherine Johnson,Mathematician\n"

// IEM-AC-7 and AC-M5, the two promises that make an import safe to run against
// a real estate: the dry run writes NOTHING, and what the commit writes are
// LEADS — not people (ADR-0008 anti-pollution).
func TestCSVImportDryRunWritesNothingAndCommitsLeads(t *testing.T) {
	e := setupImportApp(t)
	peopleBefore := importedPersonCount(t, e)

	profile, status := uploadCSV(t, e, "lead", prospectCSV)
	if status != http.StatusOK {
		t.Fatalf("upload → %d, want 200", status)
	}
	if profile.RowsProfiled != 3 || len(profile.Columns) != 3 {
		t.Fatalf("profile = %+v, want 3 rows and 3 columns", profile)
	}
	if profile.SuggestedMapping["Email"] != "email" || profile.SuggestedMapping["Full Name"] != "full_name" {
		t.Fatalf("suggested mapping = %v, want the normalized-name matches", profile.SuggestedMapping)
	}
	if profile.SourceRef == "" {
		t.Fatal("the upload returned no source_ref, so nothing can reference the stored file")
	}

	run, status := createRun(t, e, "lead", profile)
	if status != http.StatusAccepted {
		t.Fatalf("create run → %d, want 202", status)
	}
	if run.Status != "awaiting_approval" {
		t.Fatalf("status = %q, want awaiting_approval", run.Status)
	}

	// The whole point of a dry run: it has told us what will happen and has
	// written none of it.
	if got := leadCount(t, e); got != 0 {
		t.Fatalf("the dry run created %d leads; a validation pass writes nothing", got)
	}

	var report importReportDTO
	if status := e.Call(t, http.MethodGet, "/v1/imports/"+run.Id+"/report", nil, nil, &report); status != http.StatusOK {
		t.Fatalf("report → %d, want 200", status)
	}
	if report.Disposition.Created != 3 {
		t.Fatalf("report = %+v, want 3 rows the commit will create", report)
	}
	if report.SourceKeyUsed != "Email" {
		t.Fatalf("source key = %q, want the column mapped onto email", report.SourceKeyUsed)
	}

	var approved importRunDTO
	if status := e.Call(t, http.MethodPost, "/v1/imports/"+run.Id+"/approve", nil, nil, &approved); status != http.StatusAccepted {
		t.Fatalf("approve → %d, want 202", status)
	}
	if got := leadCount(t, e); got != 3 {
		t.Fatalf("leads after approval = %d, want 3", got)
	}
	if got := importedPersonCount(t, e); got != peopleBefore {
		t.Fatalf("people = %d, was %d — a bulk import creates leads, never people (ADR-0008)", got, peopleBefore)
	}
}

// IEM-AC-9: the same file twice creates nothing the second time, and a
// CORRECTED file rewrites what changed. The second half is the one a frozen
// snapshot never has to answer — and the one a customer hits the moment they
// fix a typo and upload again.
func TestCSVImportReRunConvergesAndAnEditedFileUpdates(t *testing.T) {
	e := setupImportApp(t)

	profile, _ := uploadCSV(t, e, "lead", prospectCSV)
	run, _ := createRun(t, e, "lead", profile)
	if status := e.Call(t, http.MethodPost, "/v1/imports/"+run.Id+"/approve", nil, nil, nil); status != http.StatusAccepted {
		t.Fatalf("first approve → %d, want 202", status)
	}
	if got := leadCount(t, e); got != 3 {
		t.Fatalf("leads = %d, want 3", got)
	}

	// The identical file again: recognized, and nothing created.
	same, _ := uploadCSV(t, e, "lead", prospectCSV)
	sameRun, _ := createRun(t, e, "lead", same)
	if status := e.Call(t, http.MethodPost, "/v1/imports/"+sameRun.Id+"/approve", nil, nil, nil); status != http.StatusAccepted {
		t.Fatalf("second approve → %d, want 202", status)
	}
	if got := leadCount(t, e); got != 3 {
		t.Fatalf("leads after re-import = %d, want 3 — the record map exists to prevent this", got)
	}

	// The corrected file: one title fixed, and the record must follow it.
	corrected := "Email,Full Name,Title\n" +
		"ada@lovelace.example,Ada Lovelace,Analyst\n" +
		"grace@hopper.example,Grace Hopper,Rear Admiral (upper half)\n" +
		"katherine@johnson.example,Katherine Johnson,Mathematician\n"
	edited, _ := uploadCSV(t, e, "lead", corrected)
	editedRun, _ := createRun(t, e, "lead", edited)
	if status := e.Call(t, http.MethodPost, "/v1/imports/"+editedRun.Id+"/approve", nil, nil, nil); status != http.StatusAccepted {
		t.Fatalf("third approve → %d, want 202", status)
	}

	var leads leadListDTO
	if status := e.Call(t, http.MethodGet, "/v1/leads?limit=100", nil, nil, &leads); status != http.StatusOK {
		t.Fatalf("GET /v1/leads → %d", status)
	}
	if len(leads.Data) != 3 {
		t.Fatalf("leads = %d, want 3 — a correction updates, it does not duplicate", len(leads.Data))
	}
	found := false
	for _, l := range leads.Data {
		if l.Email == "grace@hopper.example" {
			found = true
			if l.Title != "Rear Admiral (upper half)" {
				t.Fatalf("title = %q, want the corrected value — an editable source that reports 'unchanged' loses the customer's fix", l.Title)
			}
		}
	}
	if !found {
		t.Fatal("the corrected lead is missing entirely")
	}
}

// A row nobody can identify is disclosed with its line, and the file it came in
// still lands the rows that ARE identifiable. Half a file imported under a
// success message is the failure this reports its way out of.
func TestCSVImportDisclosesUnidentifiableRows(t *testing.T) {
	e := setupImportApp(t)

	ragged := "Email,Full Name\n" +
		",No Address Here\n" +
		"real@example.test,Real Person\n"
	profile, status := uploadCSV(t, e, "lead", ragged)
	if status != http.StatusOK {
		t.Fatalf("upload → %d, want 200", status)
	}
	run, status := createRun(t, e, "lead", profile)
	if status != http.StatusAccepted {
		t.Fatalf("create run → %d, want 202", status)
	}

	var report importReportDTO
	if status := e.Call(t, http.MethodGet, "/v1/imports/"+run.Id+"/report", nil, nil, &report); status != http.StatusOK {
		t.Fatalf("report → %d, want 200", status)
	}
	if report.Disposition.Skipped != 1 || len(report.Issues) != 1 {
		t.Fatalf("report = %+v, want exactly one disclosed skip", report)
	}
	if report.Issues[0].Line != 2 {
		t.Fatalf("skip names line %d, want 2 — a human has to be able to open the file to it", report.Issues[0].Line)
	}
	if report.Disposition.Created != 1 {
		t.Fatalf("created = %d, want the one identifiable row still landing", report.Disposition.Created)
	}
}

// IEM-WIRE-5: approval is valid ONLY from awaiting_approval. Approving twice
// means the second approver is acting on a state nobody judged.
func TestCSVImportApprovalIsValidOnlyOnce(t *testing.T) {
	e := setupImportApp(t)

	profile, _ := uploadCSV(t, e, "lead", prospectCSV)
	run, _ := createRun(t, e, "lead", profile)
	if status := e.Call(t, http.MethodPost, "/v1/imports/"+run.Id+"/approve", nil, nil, nil); status != http.StatusAccepted {
		t.Fatalf("first approve → %d, want 202", status)
	}
	if status := e.Call(t, http.MethodPost, "/v1/imports/"+run.Id+"/approve", nil, nil, nil); status != http.StatusConflict {
		t.Fatalf("second approve → %d, want 409", status)
	}
}

// A run this installation does not have answers not-found — the same answer a
// run it may not read gets, which is what keeps existence undisclosed.
func TestCSVImportUnknownRunIsNotFound(t *testing.T) {
	e := setupImportApp(t)

	const absent = "00000000-0000-7000-8000-000000000000"
	if status := e.Call(t, http.MethodGet, "/v1/imports/"+absent, nil, nil, nil); status != http.StatusNotFound {
		t.Fatalf("GET an absent run → %d, want 404", status)
	}
	if status := e.Call(t, http.MethodGet, "/v1/imports/"+absent+"/report", nil, nil, nil); status != http.StatusNotFound {
		t.Fatalf("report of an absent run → %d, want 404", status)
	}
}

// A mapping that names a field the object does not have is refused at the door,
// not at row 40,000 of a commit.
func TestCSVImportRefusesAnImpossibleMapping(t *testing.T) {
	e := setupImportApp(t)

	profile, _ := uploadCSV(t, e, "lead", prospectCSV)
	status := e.Call(t, http.MethodPost, "/v1/imports", apptest.AnyMap{
		"connector": "csv", "object": "lead",
		"source_ref": profile.SourceRef,
		"mapping":    map[string]string{"Email": "email", "Title": "annual_revenue"},
	}, nil, nil)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("an unknown target → %d, want 422", status)
	}
}

// A source reference minted for another installation is not a door into this
// one. The blobstore treats keys as opaque bytes by design — the key IS the
// tenant boundary — so this refusal is the only thing between a caller and
// somebody else's uploaded estate.
func TestCSVImportRefusesAForeignSourceReference(t *testing.T) {
	e := setupImportApp(t)

	profile, _ := uploadCSV(t, e, "lead", prospectCSV)
	foreign := "11111111-1111-7111-8111-111111111111/import/" + path.Base(profile.SourceRef)

	status := e.Call(t, http.MethodPost, "/v1/imports", apptest.AnyMap{
		"connector": "csv", "object": "lead",
		"source_ref": foreign, "mapping": profile.SuggestedMapping,
	}, nil, nil)
	// Not-found, not forbidden: a caller may not learn whether a reference they
	// were never given exists.
	if status != http.StatusNotFound {
		t.Fatalf("a foreign source_ref → %d, want 404", status)
	}
	if got := leadCount(t, e); got != 0 {
		t.Fatalf("leads = %d, want 0 — nothing may land from a foreign source", got)
	}
}

// The dry run says what the commit WILL do, so a second staging of a file
// already imported must predict nothing new — not "will update everything",
// which is what classifying by existence alone would report.
func TestCSVImportPredictsUnchangedRatherThanUpdatingEverything(t *testing.T) {
	e := setupImportApp(t)

	first, _ := uploadCSV(t, e, "lead", prospectCSV)
	run, _ := createRun(t, e, "lead", first)
	if status := e.Call(t, http.MethodPost, "/v1/imports/"+run.Id+"/approve", nil, nil, nil); status != http.StatusAccepted {
		t.Fatalf("approve → %d, want 202", status)
	}

	again, _ := uploadCSV(t, e, "lead", prospectCSV)
	second, _ := createRun(t, e, "lead", again)

	var report importReportDTO
	if status := e.Call(t, http.MethodGet, "/v1/imports/"+second.Id+"/report", nil, nil, &report); status != http.StatusOK {
		t.Fatalf("report → %d, want 200", status)
	}
	if report.Disposition.Unchanged != 3 || report.Disposition.Created != 0 || report.Disposition.Updated != 0 {
		t.Fatalf("prediction = %+v, want 3 unchanged and nothing else: the file has not changed", report.Disposition)
	}
}

// A run whose validation could not finish is recorded as failed. Left in
// `validating` it would be an orphan: approve refuses it, resume refuses it,
// and nothing else could ever move it.
func TestCSVImportRecordsAValidationThatCouldNotFinish(t *testing.T) {
	e, store := setupImportAppWithStore(t)

	profile, _ := uploadCSV(t, e, "lead", prospectCSV)
	if err := store.Delete(context.Background(), profile.SourceRef); err != nil {
		t.Fatalf("removing the stored upload: %v", err)
	}

	status := e.Call(t, http.MethodPost, "/v1/imports", apptest.AnyMap{
		"connector": "csv", "object": "lead",
		"source_ref": profile.SourceRef, "mapping": profile.SuggestedMapping,
	}, nil, nil)
	if status != http.StatusNotFound {
		t.Fatalf("a vanished upload → %d, want 404", status)
	}

	// The run row exists — it was created before the file was read — and it
	// must not be sitting in validating.
	var runs []importRunDTO
	if err := apptest.InWorkspace(e, t, e.Slug, func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(), `SELECT id::text, status FROM import_run WHERE connector = 'csv'`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r importRunDTO
			if err := rows.Scan(&r.Id, &r.Status); err != nil {
				return err
			}
			runs = append(runs, r)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("reading the runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %+v, want the one this attempt opened", runs)
	}
	if runs[0].Status != "failed" {
		t.Fatalf("status = %q, want failed — a validating run nothing can move is an orphan", runs[0].Status)
	}
}

// Every run response carries who opened it. The database stamps it; a surface
// that drops it leaves the governance question "who imported this" unanswerable
// from the API the operator actually reads.
func TestCSVImportRunNamesWhoOpenedIt(t *testing.T) {
	e := setupImportApp(t)

	profile, _ := uploadCSV(t, e, "lead", prospectCSV)
	var run struct {
		Id         string `json:"id"`
		CapturedBy string `json:"captured_by"`
	}
	if status := e.Call(t, http.MethodPost, "/v1/imports", apptest.AnyMap{
		"connector": "csv", "object": "lead",
		"source_ref": profile.SourceRef, "mapping": profile.SuggestedMapping,
	}, nil, &run); status != http.StatusAccepted {
		t.Fatalf("create run → %d, want 202", status)
	}
	if run.CapturedBy == "" {
		t.Fatal("the run does not say who opened it")
	}
}

type organizationListDTO struct {
	Data []struct {
		DisplayName string `json:"display_name"`
		LegalName   string `json:"legal_name"`
		Industry    string `json:"industry"`
		Description string `json:"description"`
	} `json:"data"`
}

func organizations(t *testing.T, e *apptest.AppEnv) organizationListDTO {
	t.Helper()
	var orgs organizationListDTO
	if status := e.Call(t, http.MethodGet, "/v1/organizations?limit=100", nil, nil, &orgs); status != http.StatusOK {
		t.Fatalf("GET /v1/organizations → %d, want 200", status)
	}
	return orgs
}

// The second object an import can land, end to end — and the fields a create
// path is easy to forget: legal_name and description reach the stored record
// on the FIRST import, not only when a second upload happens to patch them.
func TestCSVImportLandsOrganizationsWithEveryMappedField(t *testing.T) {
	e := setupImportApp(t)

	const file = "Company,Legal Name,Industry,Description\n" +
		"Initech,Initech GmbH,software,They make software\n" +
		"Umbrella,Umbrella AG,biotech,They make other things\n"
	profile, status := uploadCSV(t, e, "organization", file)
	if status != http.StatusOK {
		t.Fatalf("upload → %d, want 200", status)
	}
	// "Company" matches no organization field by name, so the human maps it.
	mapping := map[string]string{
		"Company": "display_name", "Legal Name": "legal_name",
		"Industry": "industry", "Description": "description",
	}
	var run importRunDTO
	if status := e.Call(t, http.MethodPost, "/v1/imports", apptest.AnyMap{
		"connector": "csv", "object": "organization",
		"source_ref": profile.SourceRef, "mapping": mapping,
	}, nil, &run); status != http.StatusAccepted {
		t.Fatalf("create run → %d, want 202", status)
	}
	before := len(organizations(t, e).Data)
	if status := e.Call(t, http.MethodPost, "/v1/imports/"+run.Id+"/approve", nil, nil, nil); status != http.StatusAccepted {
		t.Fatalf("approve → %d, want 202", status)
	}

	orgs := organizations(t, e)
	if len(orgs.Data) != before+2 {
		t.Fatalf("organizations = %d, want %d", len(orgs.Data), before+2)
	}
	var found bool
	for _, o := range orgs.Data {
		if o.DisplayName != "Initech" {
			continue
		}
		found = true
		if o.LegalName != "Initech GmbH" || o.Industry != "software" || o.Description != "They make software" {
			t.Fatalf("stored %+v — a mapped column that lands on neither create nor update is a column the import lied about", o)
		}
	}
	if !found {
		t.Fatal("the imported organization is missing")
	}

	// A corrected file rewrites the fields that changed, on the object whose
	// natural key is its own name.
	const corrected = "Company,Legal Name,Industry,Description\n" +
		"Initech,Initech SE,software,They make software\n" +
		"Umbrella,Umbrella AG,biotech,They make other things\n"
	edited, _ := uploadCSV(t, e, "organization", corrected)
	var editedRun importRunDTO
	if status := e.Call(t, http.MethodPost, "/v1/imports", apptest.AnyMap{
		"connector": "csv", "object": "organization",
		"source_ref": edited.SourceRef, "mapping": mapping,
	}, nil, &editedRun); status != http.StatusAccepted {
		t.Fatalf("create corrected run → %d, want 202", status)
	}
	var report importReportDTO
	if status := e.Call(t, http.MethodGet, "/v1/imports/"+editedRun.Id+"/report", nil, nil, &report); status != http.StatusOK {
		t.Fatalf("report → %d", status)
	}
	if report.Disposition.Updated != 1 || report.Disposition.Unchanged != 1 {
		t.Fatalf("prediction = %+v, want exactly the one row that changed", report.Disposition)
	}
	if status := e.Call(t, http.MethodPost, "/v1/imports/"+editedRun.Id+"/approve", nil, nil, nil); status != http.StatusAccepted {
		t.Fatalf("approve corrected → %d, want 202", status)
	}
	for _, o := range organizations(t, e).Data {
		if o.DisplayName == "Initech" && o.LegalName != "Initech SE" {
			t.Fatalf("legal name = %q, want the corrected value", o.LegalName)
		}
	}
	if got := len(organizations(t, e).Data); got != before+2 {
		t.Fatalf("organizations = %d, want %d — a correction updates, it does not duplicate", got, before+2)
	}
}

// The upload refuses what it cannot profile, and says which part of the request
// was wrong rather than failing somewhere later with a run already created.
func TestCSVImportUploadRefusesWhatItCannotUse(t *testing.T) {
	e := setupImportApp(t)

	if _, status := uploadCSV(t, e, "deal", prospectCSV); status != http.StatusUnprocessableEntity {
		t.Fatalf("an unsupported object → %d, want 422", status)
	}
	if _, status := uploadCSV(t, e, "lead", ""); status != http.StatusUnprocessableEntity {
		t.Fatalf("an empty file → %d, want 422", status)
	}
	if _, status := uploadCSV(t, e, "lead", "Email,Email\na@x.test,b@x.test\n"); status != http.StatusUnprocessableEntity {
		t.Fatalf("a duplicate header → %d, want 422", status)
	}
}
