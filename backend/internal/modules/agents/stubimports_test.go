// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"context"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// stubImports satisfies the Imports seam for the whole-surface walks. It
// answers a run in the one state a commit accepts, so the conformance walk
// reaches Handle rather than stopping at the state refusal.
type stubImports struct{}

func (stubImports) ProfileSource(context.Context, string, string) (crmcontracts.ImportSourceProfile, error) {
	return crmcontracts.ImportSourceProfile{}, nil
}

func (stubImports) StageRun(context.Context, crmcontracts.CreateImportRunRequest) (crmcontracts.ImportRun, error) {
	return crmcontracts.ImportRun{Status: awaitingApproval}, nil
}

func (stubImports) ReadRun(context.Context, ids.UUID) (crmcontracts.ImportRun, error) {
	return crmcontracts.ImportRun{Status: awaitingApproval}, nil
}

func (stubImports) ReadReport(context.Context, ids.UUID) (crmcontracts.ImportRunReport, error) {
	return crmcontracts.ImportRunReport{}, nil
}

func (stubImports) Commit(context.Context, ids.UUID) (crmcontracts.ImportRun, error) {
	return crmcontracts.ImportRun{Status: "running"}, nil
}
