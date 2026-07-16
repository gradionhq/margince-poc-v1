// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// workflow_run.detail (migration 0074) is one jsonb shape for every
// reasoned outcome the engine ever records: a human-readable reason,
// plus — only while a run is parked behind a staged 🟡 approval — the
// approval's id as a real field. Before this column, both payloads rode
// the `error` text column and the staging pointer was matched back by
// exact string equality (workflows_blocked.go); a jsonb field the
// matcher can query structurally survives wording changes and still
// reads as a sentence to a human looking at the row directly.

import (
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// runDetail is the ONE encoding every writer produces and every reader
// consumes for workflow_run.detail.
type runDetail struct {
	Reason     string          `json:"reason,omitempty"`
	ApprovalID *ids.ApprovalID `json:"approval_id,omitempty"`
}

// reasonDetail encodes a plain failure/skip/block reason with no staging
// pointer attached.
func reasonDetail(reason string) ([]byte, error) {
	return json.Marshal(runDetail{Reason: reason})
}

// stagedApprovalDetail is the ONE spelling of the run-row staging
// pointer: the Apply path stamps it when a run parks (runOne,
// workflows.go), and MarkRunBlocked (workflows_blocked.go) matches on
// its approval_id field, so the linkage can never drift apart.
func stagedApprovalDetail(approvalID ids.ApprovalID) ([]byte, error) {
	return json.Marshal(runDetail{
		Reason:     "staged as approval " + approvalID.String() + "; awaiting the human decision",
		ApprovalID: &approvalID,
	})
}

// parseRunDetail unmarshals a row's raw detail. A run with no reason (a
// clean applied firing) stores NULL, which scans as an empty raw slice —
// that parses to the zero value, not an error. Anything else that fails
// to unmarshal is a malformed row and must surface, never silently read
// back as empty (the backfilled and freshly-written shapes both decode
// through this one function).
func parseRunDetail(raw []byte) (runDetail, error) {
	if len(raw) == 0 {
		return runDetail{}, nil
	}
	var d runDetail
	if err := json.Unmarshal(raw, &d); err != nil {
		return runDetail{}, fmt.Errorf("automation: workflow_run.detail: %w", err)
	}
	return d, nil
}

// decodeRunDetail renders a row's detail as the reason text the run-
// history read surfaces to a caller (automations_runs.go): the
// approval_id field is engine-internal wiring for MarkRunBlocked, never
// part of the caller-facing shape.
func decodeRunDetail(raw []byte) (*string, error) {
	d, err := parseRunDetail(raw)
	if err != nil {
		return nil, err
	}
	if d.Reason == "" {
		return nil, nil
	}
	return &d.Reason, nil
}
