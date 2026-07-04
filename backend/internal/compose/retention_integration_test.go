// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The retention engine + legal hold (data-model §3.4): over-age records
// get their policy's single action with a per-record audit trail, a
// legal_hold row is never auto-acted, and an unknown policy scope is
// skipped loudly rather than half-applied.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// seedRetentionPolicies plants the same rows the workspace bootstrap
// seeds (the HTTP-path exactness is asserted in the consent e2e suite).
func seedRetentionPolicies(t *testing.T, e *authzEnv) {
	t.Helper()
	err := database.WithWorkspaceTx(e.admin(), e.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO retention_policy (workspace_id, object_type, category, retain_days, action)
			SELECT NULLIF(current_setting('app.workspace_id', true), '')::uuid, v.o, v.c, v.d, v.a
			FROM (VALUES
			  ('lead', 'unconverted', 365, 'anonymize'),
			  ('activity', NULL, 1095, 'archive'),
			  ('activity', 'transcript', 365, 'erase'),
			  ('person', 'no_consent_no_deal', 730, 'anonymize'),
			  ('deal', 'lost', 1825, 'archive')
			) AS v(o, c, d, a)`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRetentionActsOnOverAgeRecordsAndHonorsLegalHold(t *testing.T) {
	e := setupAuthz(t)
	seedRetentionPolicies(t, e)

	staleLead, heldLead := ids.NewV7(), ids.NewV7()
	staleDeal := ids.NewV7()
	transcript := ids.NewV7()
	err := database.WithWorkspaceTx(e.admin(), e.pool, func(tx pgx.Tx) error {
		wsClause := `NULLIF(current_setting('app.workspace_id', true), '')::uuid`
		for _, stmt := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO lead (id, workspace_id, full_name, email, status, source, captured_by, created_at)
			  VALUES ($1, ` + wsClause + `, 'Old Cold Lead', 'cold@old.example', 'new', 'manual', 'human:x', now() - interval '400 days')`,
				[]any{staleLead}},
			{`INSERT INTO lead (id, workspace_id, full_name, status, legal_hold, source, captured_by, created_at)
			  VALUES ($1, ` + wsClause + `, 'Held Lead', 'new', true, 'manual', 'human:x', now() - interval '400 days')`,
				[]any{heldLead}},
			{`INSERT INTO activity (id, workspace_id, kind, subject, body, occurred_at, source, source_system, source_id, captured_by)
			  VALUES ($1, ` + wsClause + `, 'note', 'Transcript', 'sensitive words', now() - interval '400 days', 'capture', 'transcript', 't-1', 'connector:t')`,
				[]any{transcript}},
		} {
			if _, err := tx.Exec(context.Background(), stmt.sql, stmt.args...); err != nil {
				return fmt.Errorf("%s: %w", stmt.sql[:40], err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// A pipeline+stage pair carries the aged-out lost deal.
	pipelineID, stageID := ids.NewV7(), ids.NewV7()
	err = database.WithWorkspaceTx(e.admin(), e.pool, func(tx pgx.Tx) error {
		wsClause := `NULLIF(current_setting('app.workspace_id', true), '')::uuid`
		if _, err := tx.Exec(context.Background(),
			`INSERT INTO pipeline (id, workspace_id, name, is_default) VALUES ($1, `+wsClause+`, 'Retention P', true)`,
			pipelineID); err != nil {
			return err
		}
		if _, err := tx.Exec(context.Background(),
			`INSERT INTO stage (id, workspace_id, pipeline_id, name, position, semantic) VALUES ($1, `+wsClause+`, $2, 'Lost', 1, 'lost')`,
			stageID, pipelineID); err != nil {
			return err
		}
		_, err := tx.Exec(context.Background(), `
			INSERT INTO deal (id, workspace_id, name, pipeline_id, stage_id, status, lost_reason, closed_at, source, captured_by)
			VALUES ($1, `+wsClause+`, 'Retention Deal', $2, $3, 'lost', 'stale', now() - interval '2000 days', 'manual', 'human:x')`,
			staleDeal, pipelineID, stageID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	svc := NewRetentionService(e.pool, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err := svc.Evaluate(context.Background()); err != nil {
		t.Fatal(err)
	}

	var leadName string
	var heldName string
	var dealArchived, transcriptBodyGone bool
	var retentionAudits int
	err = database.WithWorkspaceTx(e.admin(), e.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(context.Background(), `SELECT full_name FROM lead WHERE id = $1`, staleLead).Scan(&leadName); err != nil {
			return err
		}
		if err := tx.QueryRow(context.Background(), `SELECT full_name FROM lead WHERE id = $1`, heldLead).Scan(&heldName); err != nil {
			return err
		}
		if err := tx.QueryRow(context.Background(), `SELECT archived_at IS NOT NULL FROM deal WHERE id = $1`, staleDeal).Scan(&dealArchived); err != nil {
			return err
		}
		if err := tx.QueryRow(context.Background(), `SELECT body IS NULL FROM activity WHERE id = $1`, transcript).Scan(&transcriptBodyGone); err != nil {
			return err
		}
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM audit_log WHERE after ? 'retention_action'`).Scan(&retentionAudits)
	})
	if err != nil {
		t.Fatal(err)
	}
	if leadName != "Anonymized Lead" {
		t.Errorf("over-age lead not anonymized: %q", leadName)
	}
	if heldName != "Held Lead" {
		t.Errorf("legal-held lead was acted on: %q", heldName)
	}
	if !dealArchived {
		t.Error("over-age lost deal not archived")
	}
	if !transcriptBodyGone {
		t.Error("over-age transcript body not erased")
	}
	if retentionAudits < 3 {
		t.Errorf("retention audits = %d, want one per action (≥3)", retentionAudits)
	}

	// A second pass is idempotent: everything due is already acted.
	if err := svc.Evaluate(context.Background()); err != nil {
		t.Fatal(err)
	}
	var second int
	err = database.WithWorkspaceTx(e.admin(), e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM audit_log WHERE after ? 'retention_action'`).Scan(&second)
	})
	if err != nil || second != retentionAudits {
		t.Fatalf("second pass re-acted: %d → %d audits (%v)", retentionAudits, second, err)
	}
}
