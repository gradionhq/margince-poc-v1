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
//nolint:ireturn // the call is the product, same as every other family here
func NewImportCall(cmd ImportCommand) GovernedCall {
	return bind[ImportCommand](importResolver{}, cmd)
}

type importResolver struct{}

// Subject names what the approval binds to.
//
// A commit binds to its run: the report a person read belongs to that id, and
// an approval that named only "an import" could be redeemed against a
// different run — one whose report nobody saw.
//
// A preview binds to nothing, because there is nothing yet: the run it will
// create does not exist when the call is staged. That is honest rather than a
// gap, and it is safe because a preview writes no domain rows.
func (importResolver) Subject(_ context.Context, cmd ImportCommand) (StageInfo, error) {
	if cmd.Verb == ImportVerbCommit {
		return StageInfo{
			TargetType: importRunRecordType,
			TargetID:   cmd.RunID,
			Summary: fmt.Sprintf("Import %s records from the file staged as run %s",
				cmd.Object, cmd.RunID),
		}, nil
	}
	return StageInfo{
		TargetType: importRunRecordType,
		Summary:    fmt.Sprintf("Check a file of %s records against this workspace, writing nothing", cmd.Object),
	}, nil
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
