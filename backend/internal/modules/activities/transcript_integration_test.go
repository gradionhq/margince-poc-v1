// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package activities

// A transcript lands through the real write shape exactly like any other
// logged activity — normalized, linked, and carrying the source_system the
// privacy module's activity/transcript retention selector
// (internal/modules/privacy/retentionselectors.go) keys its sweep on. This
// proves the two sides of that contract actually agree: this module writes
// what that selector expects, not just what a unit test asserts in isolation.

import (
	"context"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestLogActivityNormalizesAndStoresATranscript(t *testing.T) {
	e := setupSend(t)
	ctx := e.as(principal.RowScopeAll)

	raw := "Anna: Let's ship by Friday.   \r\nBen: Works for me.\r\n"
	sourceSystem := "transcript"
	// Through LogActivityInputFrom, exactly as the HTTP handler and the MCP
	// provider path both do (mapping.go's own doc comment) — a test that
	// hand-built LogActivityInput would skip the mapping and prove nothing
	// about what a real caller sends.
	in, err := LogActivityInputFrom(crmcontracts.CreateActivityRequest{
		Kind: "meeting", Body: &raw, SourceSystem: &sourceSystem, Source: "ui",
	})
	if err != nil {
		t.Fatalf("LogActivityInputFrom: %v", err)
	}
	activity, created, err := e.store(nil).LogActivity(ctx, in)
	if err != nil {
		t.Fatalf("LogActivity: %v", err)
	}
	if !created {
		t.Fatal("created = false on the first write")
	}
	if activity.Body == nil || *activity.Body != "Anna: Let's ship by Friday.\nBen: Works for me." {
		t.Errorf("Body = %v, want the normalized form", activity.Body)
	}

	// The exact predicate activity/transcript's selector runs
	// (retentionselectors.go: `source_system = 'transcript' AND body IS NOT
	// NULL`) — reading the raw column proves this write satisfies it, not
	// just that the Go struct looks right.
	var storedSourceSystem string
	var storedBody *string
	if err := e.owner.QueryRow(context.Background(),
		`SELECT source_system, body FROM activity WHERE id = $1`, activity.Id,
	).Scan(&storedSourceSystem, &storedBody); err != nil {
		t.Fatalf("reading back the row: %v", err)
	}
	if storedSourceSystem != "transcript" {
		t.Errorf("source_system = %q, want transcript — the retention selector would never see this row", storedSourceSystem)
	}
	if storedBody == nil {
		t.Fatal("body is NULL — the retention selector's body IS NOT NULL clause would skip this row")
	}
}
