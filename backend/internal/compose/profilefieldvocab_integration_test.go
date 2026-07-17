// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The extraction vocabulary lives in FOUR copies: the contract ColdStartField
// enum, extractionFieldNames, the gate predicate, and the
// organization_profile_field CHECK constraint. The first three are pinned to
// each other in Go; this test pins the fourth — a field the model can emit
// but the database refuses would pass every unit gate and then fail at the
// accept-write, in production, on the first site that grounds it.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestEveryExtractionFieldIsAccepted_ByTheLiveCheckConstraint(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.Admin()

	err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		orgID := ids.New[ids.OrganizationKind]()
		if _, err := tx.Exec(context.Background(),
			`INSERT INTO organization (id, workspace_id, display_name, source, captured_by)
			 VALUES ($1, $2, 'Vocab Probe', 'manual', 'human:test')`,
			orgID, e.WS); err != nil {
			return err
		}
		for _, field := range extractionFieldNames {
			if _, err := tx.Exec(context.Background(),
				`INSERT INTO organization_profile_field
				   (workspace_id, organization_id, field, value, evidence_snippet, source_url, confidence, captured_by)
				 VALUES ($1, $2, $3, 'v', 'e', '', 0.9, 'agent:test')`,
				e.WS, orgID, field); err != nil {
				t.Errorf("the live CHECK constraint refuses extraction field %q — widen it with the vocabulary (see 0084's pattern)", field)
				return err
			}
		}
		return nil
	})
	if err != nil && !t.Failed() {
		t.Fatal(err)
	}
}
