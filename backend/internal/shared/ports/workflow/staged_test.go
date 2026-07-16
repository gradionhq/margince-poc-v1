// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package workflow_test

import (
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// A staged error must stay matchable by errors.As across module boundaries:
// the Surface-B runner suspends on the id, so a caller that only sees the
// unwrapped sentinel has lost the approval it needs to resume.
func TestStagedApprovalErrorCarriesTheApprovalIDThroughErrorsAs(t *testing.T) {
	id := ids.NewV7()
	var err error = &workflow.StagedApprovalError{ApprovalID: ids.ApprovalID{UUID: id}}

	var staged *workflow.StagedApprovalError
	if !errors.As(err, &staged) {
		t.Fatal("errors.As did not match StagedApprovalError")
	}
	if staged.ApprovalID.UUID != id {
		t.Errorf("ApprovalID = %v, want %v", staged.ApprovalID.UUID, id)
	}
	if !errors.Is(err, apperrors.ErrRequiresApproval) {
		t.Error("staged error must unwrap to ErrRequiresApproval")
	}
}
