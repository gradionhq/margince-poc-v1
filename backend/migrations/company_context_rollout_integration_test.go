// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package migrations

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
)

func TestCompanyContextBackfillAddsOnlyMissingAnchorProvenance(t *testing.T) {
	ownerDSN, _ := dsns(t)
	conn := connect(t, ownerDSN)
	resetSchema(t, conn)
	ctx := context.Background()
	core, err := Core()
	if err != nil {
		t.Fatalf("loading core: %v", err)
	}
	if _, err := dbmigrate.Up(ctx, conn, core); err != nil {
		t.Fatalf("up: %v", err)
	}
	rolloutIndex := -1
	for i, migration := range core.Migrations {
		if migration.Version == "0105" {
			rolloutIndex = i
			break
		}
	}
	if rolloutIndex < 0 {
		t.Fatal("core migrations contain no 0105 company-context rollout")
	}
	if _, err := dbmigrate.Down(ctx, conn, core, len(core.Migrations)-rolloutIndex); err != nil {
		t.Fatalf("down to pre-0105: %v", err)
	}

	workspaceID := seedWorkspace(t, conn, "company-context-backfill")
	var organizationID string
	if err := conn.QueryRow(ctx, `INSERT INTO organization
		(workspace_id, display_name, legal_name, industry, address_line1,
		 source, captured_by, is_anchor)
		VALUES ($1, 'Acme', 'Acme GmbH', 'Manufacturing', 'Factory Road 1',
		 'manual', 'human:owner', true)
		RETURNING id`, workspaceID).Scan(&organizationID); err != nil {
		t.Fatalf("seed anchor: %v", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO organization_profile_field
		(workspace_id, organization_id, field, value, evidence_snippet, source_url,
		 confidence, source, captured_by)
		VALUES ($1, $2, 'industry', 'Human manufacturing', '', '', 1, 'human', 'human:owner')`,
		workspaceID, organizationID); err != nil {
		t.Fatalf("seed human profile field: %v", err)
	}

	if _, err := dbmigrate.Up(ctx, conn, core); err != nil {
		t.Fatalf("apply 0105: %v", err)
	}
	rows, err := conn.Query(ctx, `SELECT field, value, source, captured_by
		FROM organization_profile_field WHERE organization_id = $1`, organizationID)
	if err != nil {
		t.Fatalf("read backfill: %v", err)
	}
	defer rows.Close()
	type profileValue struct{ value, source, capturedBy string }
	got := map[string]profileValue{}
	for rows.Next() {
		var field string
		var value profileValue
		if err := rows.Scan(&field, &value.value, &value.source, &value.capturedBy); err != nil {
			t.Fatalf("scan backfill: %v", err)
		}
		got[field] = value
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate backfill: %v", err)
	}
	for field, want := range map[string]string{
		"display_name": "Acme", "legal_name": "Acme GmbH", "registered_address": "Factory Road 1",
	} {
		value, found := got[field]
		if !found || value.value != want || value.source != "migration" || value.capturedBy != "system:migration-0105" {
			t.Errorf("backfilled %s = %+v, want %q with migration provenance", field, value, want)
		}
	}
	industry := got["industry"]
	if industry.value != "Human manufacturing" || industry.source != "human" || industry.capturedBy != "human:owner" {
		t.Errorf("human industry was overwritten: %+v", industry)
	}
}
