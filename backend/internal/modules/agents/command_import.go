// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The import commands: what an approval of a migrate-in call binds to.
//
// An import run is not a record. It has no owner, no row scope of its own, no
// custom fields — it is a unit of work over the estate, and the estate is what
// the approval is really about. So these commands do not go through the record
// seam the create/patch/archive family uses; they name the run, and the run's
// own report is what the person approving reads.
//
// TWO OPERATIONS REACH HERE and they are asymmetric on purpose.
// createImportRun writes no domain rows (AC-M5), so its approval is a
// formality the tier never asks for — it is registered because the fitness
// test requires every agent-reachable mutating route to decode into something
// that can say what an approval would bind to, and answering "nothing" for a
// route that touches the estate is exactly the gap that test exists to close.
// approveImportRun is the one that writes, and its approval names the run.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ImportCommand is one call against an import run.
//
// Verb distinguishes staging a dry run from committing it, because the two
// bind approvals to different things: a dry run has no run yet, and a commit
// names one.
type ImportCommand struct {
	Verb  string
	RunID ids.UUID
	// Object is what the file's rows are, carried so the summary a person
	// reads says "import 400 organizations" rather than "import a file".
	Object string
}

// The two verbs an ImportCommand can carry.
const (
	ImportVerbPreview = "preview"
	ImportVerbCommit  = "commit"
)

// NewImportCall binds one import command to the resolver that speaks it.
//
// The seam is REQUIRED for a commit, because the summary a person decides on
// is written from the run's report — see Subject. Both doors pass the same one,
// so a commit staged over REST and one staged over MCP describe the import
// identically. A preview needs no seam and may pass nil.
//
//nolint:ireturn // the call is the product, same as every other family here
func NewImportCall(imports Imports, cmd ImportCommand) GovernedCall {
	return bind[ImportCommand](importResolver{imports: imports}, cmd)
}

type importResolver struct{ imports Imports }

// Subject names what the approval binds to.
//
// A commit binds to its run: the report a person read belongs to that id, and
// an approval that named only "an import" could be redeemed against a
// different run — one whose report nobody saw.
//
// A preview binds to nothing, because there is nothing yet: the run it will
// create does not exist when the call is staged. That is honest rather than a
// gap, and it is safe because a preview writes no domain rows.
// THE SUMMARY CARRIES THE REPORT'S COUNTS for a commit, and that is the whole
// reason this resolver holds a seam. The approval row a person sees in the
// inbox IS its summary — nothing renders the report beside it — so a summary
// saying only "import run <uuid>" asks somebody to authorise a bulk write to
// their estate without telling them what it does. They would be clicking yes
// on a number they never saw.
func (r importResolver) Subject(ctx context.Context, cmd ImportCommand) (StageInfo, error) {
	if cmd.Verb == ImportVerbCommit {
		object, report, err := r.reportFor(ctx, cmd)
		if err != nil {
			return StageInfo{}, err
		}
		return StageInfo{
			TargetType: importRunRecordType,
			TargetID:   cmd.RunID,
			Summary:    describeImport(object, report),
		}, nil
	}
	return StageInfo{
		TargetType: importRunRecordType,
		Summary:    fmt.Sprintf("Check a file of %s records against this workspace, writing nothing", cmd.Object),
	}, nil
}

// reportFor reads what the run will do, and what its rows are.
//
// A run awaiting approval HAS a report — reaching that state is what produces
// one. Failing to read it means something is wrong with the run, and staging an
// approval whose summary cannot say what the import does is worse than
// refusing: it is the exact blind yes this method exists to prevent.
func (r importResolver) reportFor(
	ctx context.Context, cmd ImportCommand,
) (string, crmcontracts.ImportRunReport, error) {
	if r.imports == nil {
		return "", crmcontracts.ImportRunReport{}, fmt.Errorf(
			"no import seam is wired, so the approval for run %s could not say what it would do", cmd.RunID)
	}
	run, err := r.imports.ReadRun(ctx, cmd.RunID)
	if err != nil {
		return "", crmcontracts.ImportRunReport{}, err
	}
	if err := refuseUncommittableRun(run); err != nil {
		return "", crmcontracts.ImportRunReport{}, err
	}
	report, err := r.imports.ReadReport(ctx, cmd.RunID)
	if err != nil {
		return "", crmcontracts.ImportRunReport{}, fmt.Errorf(
			"reading the report of import run %s before asking for approval: %w", cmd.RunID, err)
	}
	return string(run.Object), report, nil
}

// describeImport writes the sentence a person decides on.
//
// Plain counts, in the order that matters to somebody protecting their data:
// what is new, what changes under them, what is left alone, and what could not
// be used. The unusable count is never omitted when it is non-zero, even
// though it is the least flattering number — a summary that quietly drops it
// reads as a clean import.
func describeImport(object string, report crmcontracts.ImportRunReport) string {
	d := report.Disposition
	parts := []string{fmt.Sprintf("create %d", d.Created)}
	if d.Updated > 0 {
		parts = append(parts, fmt.Sprintf("update %d", d.Updated))
	}
	if d.Unchanged > 0 {
		parts = append(parts, fmt.Sprintf("leave %d unchanged", d.Unchanged))
	}
	if d.Skipped > 0 {
		parts = append(parts, fmt.Sprintf("skip %d", d.Skipped))
	}
	summary := fmt.Sprintf("Import %d rows as %s records: %s",
		report.RowsRead, object, strings.Join(parts, ", "))
	if len(report.Issues) > 0 {
		summary += fmt.Sprintf(". %d row(s) could not be used", len(report.Issues))
	}
	return summary
}

// Guards is where a family refuses a call no approval could carry out. There
// is nothing to refuse here that the handler does not already refuse better:
// the run's state is the only precondition, the handler reads it, and reading
// it twice would let the two disagree.
func (importResolver) Guards(context.Context, ImportCommand) error { return nil }

// DecodeImportPreview reads POST /v1/imports into the preview command.
//
// Only `object` is read. The mapping and the file are what the call DOES, not
// what an approval of it binds to, and a summary quoting a thousand-row CSV
// back at a person is not a summary.
func DecodeImportPreview(body []byte) (ImportCommand, error) {
	var req struct {
		Object string `json:"object"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return ImportCommand{}, &BadArgsError{Cause: fmt.Errorf("reading the import request: %w", err)}
	}
	return ImportCommand{Verb: ImportVerbPreview, Object: req.Object}, nil
}
