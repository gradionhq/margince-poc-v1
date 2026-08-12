// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// A per-workspace job carries its workspace in its args, and the handle it
// runs under must be bound to THAT workspace — not to whatever an installation
// resolver would answer. A fleet pass runs one job per workspace, so the
// resolver refuses outright while more than one exists, and would be answering
// a question the job already answered.
func TestAPerWorkspaceJobBindsTheWorkspaceItNames(t *testing.T) {
	t.Parallel()
	ws := ids.NewV7()

	db, err := workspaceJobDB(nil, PrivacyRetentionWorkspaceArgs{Workspace: ws})
	if err != nil {
		t.Fatalf("a job naming a workspace was refused: %v", err)
	}
	got, err := db.Workspace(context.Background())
	if err != nil {
		t.Fatalf("resolving the bound workspace: %v", err)
	}
	if got.UUID != ws {
		t.Errorf("bound workspace = %s, want the one the job names (%s)", got, ws)
	}
}

// A job that declares itself workspace-scoped and carries no workspace is a
// programming error, and it is refused before any transaction opens — the same
// refusal workspaceJobCtx makes, for the same reason: a pass bound to the zero
// workspace reads nothing and reports success.
func TestAWorkspaceScopedJobWithNoWorkspaceIsRefused(t *testing.T) {
	t.Parallel()

	_, err := workspaceJobDB(nil, PrivacyRetentionWorkspaceArgs{})
	if err == nil {
		t.Fatal("a workspace-scoped job with no workspace was bound; it must be refused before any SQL runs")
	}
	if !strings.Contains(err.Error(), PrivacyRetentionWorkspaceArgs{}.Kind()) {
		t.Errorf("the refusal must name the job kind, got %q", err)
	}
}
