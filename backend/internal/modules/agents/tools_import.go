// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The import verbs: bringing a spreadsheet of records into the estate, which
// the tool surface could not begin.
//
// Every import operation was human-only, so an assistant handed a CSV could
// describe what to do with it and do none of it. The whole migrate-in flow —
// the one a customer runs once, on their first day, with the most data at
// stake — was reachable only by a person clicking through four screens.
//
// THE FILE ARRIVES AS TEXT, not as an upload. uploadImportSource is multipart
// and stays human-only, because an assistant holding a spreadsheet's contents
// cannot perform a file upload and asking it to construct a multipart body
// would be a worse door than none. `preview_import` takes the CSV as a string
// instead: the assistant already has the characters, and this is the shape it
// can actually produce.
//
// THE DRY RUN IS NOT OPTIONAL AND CANNOT BE SKIPPED. preview_import writes no
// domain rows by construction (AC-M5) — it validates the mapping against the
// live estate and produces a report. commit_import is confirm-first and takes
// a run id, so the thing a person approves is the run whose report they read.
// There is no verb that imports without producing a report first, and adding
// one would defeat the only review this flow has.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// Imports is the seam onto the migration module's CSV paths. The composition
// root implements it over the same handlers the REST transport uses, so both
// doors run one dry run and one commit.
type Imports interface {
	// ProfileSource stores CSV text and reads back what its columns contain,
	// with a proposed mapping. The agent-shaped half of uploadImportSource.
	ProfileSource(ctx context.Context, object, csv string) (crmcontracts.ImportSourceProfile, error)
	// StageRun validates a mapping against the estate and parks the run for a
	// human. Writes no domain rows.
	StageRun(ctx context.Context, req crmcontracts.CreateImportRunRequest) (crmcontracts.ImportRun, error)
	// ReadRun answers one run's lifecycle state.
	ReadRun(ctx context.Context, id ids.UUID) (crmcontracts.ImportRun, error)
	// ReadReport answers what the run will do, or did.
	ReadReport(ctx context.Context, id ids.UUID) (crmcontracts.ImportRunReport, error)
	// Commit approves a validated run and starts the write.
	Commit(ctx context.Context, id ids.UUID) (crmcontracts.ImportRun, error)
}

// RegisterImportTools joins the import verbs to the surface.
//
// Unconditionally, unlike the tag and whoami registrars: the contract DECLARES
// these four verbs, so a registry that skipped them would advertise something
// tools/list cannot offer — the mismatch TestEveryDeclaredToolVerbIsRegistered
// exists to catch. A deployment with no object store still serves them and
// refuses the three that need the source file, naming what is missing.
func RegisterImportTools(r *Registry, imports Imports) {
	r.Register(previewImport{imports: imports})
	r.Register(readImportRun{imports: imports})
	r.Register(readImportReport{imports: imports})
	r.Register(commitImport{imports: imports})
}

// importObjectEnum is what a file's rows may be, and it is SHORT on purpose.
//
// `person` and `deal` are not here, and the omission IS ADR-0008: a bulk file
// of contacts lands as leads, which a human promotes, so machine-sourced rows
// cannot be smuggled past the qualification step by picking an enum value. The
// first cut of this tool listed all four, which would have advertised a door
// the REST contract does not have — TestEveryToolEnumMatchesTheContractItMirrors
// caught it, which is what that gate is for.
var importObjectEnum = []string{importObjectOrganization, importObjectLead}

// The two things a file's rows may be, spelled once. They mirror the
// contract's ImportObject enum, and the tool's schema is built from them so
// the two cannot drift.
const (
	importObjectOrganization = "organization"
	importObjectLead         = "lead"
)

// maxImportCSVBytes caps the pasted file.
//
// BYTES, not characters — len() on a Go string counts bytes, and calling this
// a character count would be a lie that matters: a UTF-8 file of German or
// Vietnamese company names is refused sooner than the number suggests. The
// refusal says bytes.
//
// The bound is well under the REST upload limit (uploads.csv_import_mb, 10 MB
// by default) and under the MCP transport's own 8 MiB body: text travelling
// inside a JSON tool argument rides the same request as the rest of the
// conversation, and a file large enough to matter belongs on the upload door
// with a person at it.
const maxImportCSVBytes = 1_000_000

type previewImport struct{ imports Imports }

func (t previewImport) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "preview_import", Title: "Preview an import", Version: toolVersionV1,
		Description:   previewImportCopy.render(),
		RequiredScope: principal.ScopeWrite, Tier: mcp.TierAutoExecute,
		OpenAPIOp: "createImportRun",
		InputSchema: schema(`{"type":"object","required":["object","csv"],"properties":{
			"object":{"type":"string","enum":["` + importObjectOrganization + `","` + importObjectLead + `"]},
			"csv":{"type":"string","description":"The file's contents, header row first."},
			"mapping":{"type":"object","additionalProperties":{"type":"string"},
			  "description":"Source column name → field name. Omit to accept the proposal this call would make."}},
			"additionalProperties":false}`),
		OutputSchema: schemaFor[ImportPreviewResult](),
	}
}

func (t previewImport) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args struct {
		Object  string            `json:"object"`
		CSV     string            `json:"csv"`
		Mapping map[string]string `json:"mapping"`
	}
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	if err := refuseUnimportableObject(args.Object); err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.CSV) == "" {
		return nil, &BadArgsError{Cause: errors.New(
			"`csv` is empty; send the file's contents with its header row first")}
	}
	if len(args.CSV) > maxImportCSVBytes {
		return nil, &BadArgsError{Cause: fmt.Errorf(
			"this file is %d bytes and one pasted import takes at most %d; "+
				"upload it in the web app instead, or split it",
			len(args.CSV), maxImportCSVBytes)}
	}

	profile, err := t.imports.ProfileSource(ctx, args.Object, args.CSV)
	if err != nil {
		return nil, err
	}
	// The caller's mapping wins where it names a column, and the proposal
	// fills the rest. A caller that sends none gets the proposal whole —
	// which is honest rather than convenient, because the report says what
	// each column became and a person reads it before anything commits.
	mapping := make(map[string]string, len(profile.SuggestedMapping))
	for column, field := range profile.SuggestedMapping {
		mapping[column] = field
	}
	for column, field := range args.Mapping {
		mapping[column] = field
	}

	run, err := t.imports.StageRun(ctx, crmcontracts.CreateImportRunRequest{
		Connector: crmcontracts.CreateImportRunRequestConnector(importConnectorCSV),
		Object:    profile.Object,
		SourceRef: profile.SourceRef,
		Mapping:   mapping,
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(ImportPreviewResult{
		Run:      importRunResult(run),
		Mapping:  mapping,
		Columns:  columnNames(profile),
		Unmapped: unmappedColumns(profile, mapping),
	})
}

// importConnectorCSV is the connector a pasted file is: the same one the
// upload door uses, because it IS the same path — only the way the bytes
// arrived differs.
const importConnectorCSV = "csv"

// refuseUnimportableObject holds `object` to the vocabulary, naming the whole
// of it rather than saying the value is invalid.
func refuseUnimportableObject(object string) error {
	for _, allowed := range importObjectEnum {
		if object == allowed {
			return nil
		}
	}
	return &BadArgsError{Cause: fmt.Errorf("`object` must be one of %s",
		strings.Join(importObjectEnum, ", "))}
}

// columnNames lists what the file actually contains, so a caller that guessed
// a mapping can see the header it guessed against.
func columnNames(p crmcontracts.ImportSourceProfile) []string {
	out := make([]string, 0, len(p.Columns))
	for _, c := range p.Columns {
		out = append(out, c.Header)
	}
	return out
}

// unmappedColumns names the columns nothing will read.
//
// It is reported rather than refused: a file usually carries columns this
// product has no field for, and dropping them is the normal outcome. What is
// not acceptable is dropping them SILENTLY — a caller who mistyped a field
// name would otherwise see a clean report and a column quietly missing.
func unmappedColumns(p crmcontracts.ImportSourceProfile, mapping map[string]string) []string {
	var out []string
	for _, c := range p.Columns {
		if field, ok := mapping[c.Header]; !ok || field == "" {
			out = append(out, c.Header)
		}
	}
	return out
}

type readImportRun struct{ imports Imports }

func (t readImportRun) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "read_import_run", Title: "Read an import run", Version: toolVersionV1,
		Description:   readImportRunCopy.render(),
		RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
		OpenAPIOp:    "getImportRun",
		InputSchema:  schema(`{"type":"object","required":["run_id"],"properties":{"run_id":{"type":"string","format":"uuid"}},"additionalProperties":false}`),
		OutputSchema: schemaFor[ImportRunResult](),
	}
}

func (t readImportRun) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	id, err := importRunArg(in)
	if err != nil {
		return nil, err
	}
	run, err := t.imports.ReadRun(ctx, id)
	if err != nil {
		return nil, err
	}
	return json.Marshal(importRunResult(run))
}

type readImportReport struct{ imports Imports }

func (t readImportReport) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "read_import_report", Title: "Read an import report", Version: toolVersionV1,
		Description:   readImportReportCopy.render(),
		RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
		OpenAPIOp:    "getImportRunReport",
		InputSchema:  schema(`{"type":"object","required":["run_id"],"properties":{"run_id":{"type":"string","format":"uuid"}},"additionalProperties":false}`),
		OutputSchema: schemaFor[ImportReportResult](),
	}
}

func (t readImportReport) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	id, err := importRunArg(in)
	if err != nil {
		return nil, err
	}
	report, err := t.imports.ReadReport(ctx, id)
	if err != nil {
		return nil, err
	}
	return json.Marshal(ImportReportResult{Report: report})
}

type commitImport struct{ imports Imports }

func (t commitImport) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "commit_import", Title: "Commit an import", Version: toolVersionV1,
		Description:   commitImportCopy.render(),
		RequiredScope: principal.ScopeWrite, Tier: mcp.TierConfirmationRequired,
		OpenAPIOp: "approveImportRun",
		InputSchema: schema(`{"type":"object","required":["run_id"],"properties":{
			"run_id":{"type":"string","format":"uuid"},
			"approval_id":{"type":"string","format":"uuid","description":"Set on retry after approval"}},
			"additionalProperties":false}`),
		OutputSchema: schemaFor[ImportRunResult](),
	}
}

// StageInfo describes the approval a commit asks for, through the SAME
// command the REST door builds — so the sentence a person decides on is
// identical whichever door staged it.
func (t commitImport) StageInfo(ctx context.Context, in json.RawMessage) (StageInfo, error) {
	id, err := importRunArg(in)
	if err != nil {
		return StageInfo{}, err
	}
	return StageSubject(ctx, NewImportCall(t.imports, ImportCommand{
		Verb: ImportVerbCommit, RunID: id,
	}))
}

func (t commitImport) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	id, err := importRunArg(in)
	if err != nil {
		return nil, err
	}
	// Checked again on the approved retry. StageInfo's check is the courtesy
	// that avoids spending an approval; this one is the rule, because Handle
	// is what a granted approval re-enters and the run's state can have moved
	// while the person was deciding.
	run, err := t.imports.ReadRun(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := refuseUncommittableRun(run); err != nil {
		return nil, err
	}
	committed, err := t.imports.Commit(ctx, id)
	if err != nil {
		return nil, err
	}
	return json.Marshal(importRunResult(committed))
}

// importRunRecordType is the word the contract's admission policy uses for an
// import run, so a staged approval is row-scoped to the run it names.
const importRunRecordType = "import_run"

// awaitingApproval is the one state a commit may start from.
const awaitingApproval = "awaiting_approval"

// refuseUncommittableRun holds a commit to a run that has actually been
// dry-run.
//
// This is G — the dry run stays mandatory — expressed where it cannot be
// skipped: a run only reaches awaiting_approval by producing a report, so
// requiring that state IS requiring the report. The refusal says which state
// the run is in, because "already running" and "already finished" are the two
// a caller most needs told apart.
func refuseUncommittableRun(run crmcontracts.ImportRun) error {
	if string(run.Status) == awaitingApproval {
		return nil
	}
	return &BadArgsError{Cause: fmt.Errorf(
		"import run %s is %s, and a commit starts only from %s — "+
			"an import commits what a dry run reported, so there is always a report first",
		run.Id, string(run.Status), awaitingApproval)}
}

// importRunArg reads the one argument every id-bearing import verb takes.
func importRunArg(in json.RawMessage) (ids.UUID, error) {
	var args struct {
		RunID string `json:"run_id"`
	}
	if err := decodeArgs(in, &args); err != nil {
		return ids.UUID{}, err
	}
	id, err := ids.Parse(args.RunID)
	if err != nil {
		return ids.UUID{}, &BadArgsError{Cause: errors.New(
			"`run_id` must be the uuid preview_import answered with")}
	}
	return id, nil
}

// importRunResult reshapes one run for the wire.
func importRunResult(run crmcontracts.ImportRun) ImportRunResult {
	out := ImportRunResult{
		RunID:      run.Id.String(),
		Object:     string(run.Object),
		State:      string(run.Status),
		Checkpoint: run.Checkpoint,
	}
	if run.Error != nil {
		out.Error = *run.Error
	}
	return out
}
