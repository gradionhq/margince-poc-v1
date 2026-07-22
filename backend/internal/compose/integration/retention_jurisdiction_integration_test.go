// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The jurisdiction seam under the retention engine: with a pack
// declaring a commercial-correspondence floor registered, a destructive
// retention action must not touch commercial correspondence (email
// activities) younger than that floor — however aggressive the
// workspace's own policy is. Archiving is untouched: it RETAINS, which
// is what the statute wants.

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/jurisdiction"
)

// gobdFloorPack is the test's own six-calendar-year correspondence
// floor — the same span the de extension declares. The engine is what
// this suite proves; the de unit's statutory CONTENT is pinned by its
// own test lane (extensions/de), and the backend never imports an
// extension module (TestCompositionWiredOnlyFromCmd), so the floor is
// registered here the way the boot reconciliation would.
type gobdFloorPack struct{}

func (gobdFloorPack) Code() jurisdiction.Code { return "zq" }

func (gobdFloorPack) Retention() jurisdiction.Retention { return gobdFloorClasses{} }

type gobdFloorClasses struct{}

func (gobdFloorClasses) Classes() []jurisdiction.RetentionClass {
	return []jurisdiction.RetentionClass{
		{Name: "commercial_correspondence", Keep: jurisdiction.Period{Years: 6}},
	}
}

// init mirrors the arming the composed boot performs: the registry is
// process-global, so registering once arms the floor for this binary.
func init() {
	jurisdiction.Register(gobdFloorPack{})
}

func TestStatutoryFloorShieldsCorrespondenceFromDestruction(t *testing.T) {
	e := Setup(t)
	email, note := ids.NewV7(), ids.NewV7()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		wsClause := `NULLIF(current_setting('app.workspace_id', true), '')::uuid`
		if _, err := tx.Exec(ctx, `
			INSERT INTO retention_policy (workspace_id, object_type, category, retain_days, action)
			VALUES (`+wsClause+`, 'activity', NULL, 100, 'erase')`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO activity (id, workspace_id, kind, subject, body, occurred_at, source, captured_by)
			VALUES ($1, `+wsClause+`, 'email', 'Order confirmation', 'commercial content', now() - interval '400 days', 'capture', 'connector:t')`,
			email); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO activity (id, workspace_id, kind, subject, body, occurred_at, source, captured_by)
			VALUES ($1, `+wsClause+`, 'note', 'Old scratch note', 'ephemeral', now() - interval '400 days', 'capture', 'connector:t')`,
			note)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	svc := privacy.NewRetentionService(e.Pool, nil, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err := svc.Evaluate(context.Background()); err != nil {
		t.Fatal(err)
	}

	var emailBody, noteBody *string
	err = database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(context.Background(), `SELECT body FROM activity WHERE id = $1`, email).Scan(&emailBody); err != nil {
			return err
		}
		return tx.QueryRow(context.Background(), `SELECT body FROM activity WHERE id = $1`, note).Scan(&noteBody)
	})
	if err != nil {
		t.Fatal(err)
	}
	if emailBody == nil {
		t.Error("the GoBD floor failed: a 400-day-old email was destroyed against the 6-year statute")
	}
	if noteBody != nil {
		t.Error("the floor over-shielded: a plain note past the policy age survived")
	}
}
