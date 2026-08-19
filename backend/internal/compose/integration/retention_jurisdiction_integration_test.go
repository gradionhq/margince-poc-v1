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
)

// init mirrors the arming the composed boot performs: the registry is
// process-global, so registering once arms the floor for THIS BINARY — and one
// package is one binary. A retention suite in a sibling package runs with no pack
// registered, which is not a weaker version of this suite but the opposite of it:
// a destructive pass over correspondence would go green precisely because the
// floor that shields it is absent. A suite that moves out takes this with it.
func init() {
	RegisterGoBDFloorPack()
}

func TestStatutoryFloorShieldsCorrespondenceFromDestruction(t *testing.T) {
	e := Setup(t)
	email, note, janEmail, message := ids.NewV7(), ids.NewV7(), ids.NewV7(), ids.NewV7()
	// unlinkedEmail is the case A165/ADR-0114 NARROWED the floor to exclude: a
	// 400-day-old email that concerns no commercial transaction. §257 HGB
	// obliges retention of a Handelsbrief, and a scheduling mail or a marketing
	// enquiry is not one — shielding it was the over-retention the EDPB names
	// as applying the legal-obligation exception without case-by-case
	// assessment. It must be erased where its linked sibling survives, and
	// that contrast is the whole point of seeding both.
	unlinkedEmail := ids.NewV7()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		if _, err := tx.Exec(ctx, `
			INSERT INTO retention_policy (object_type, category, retain_days, action)
			VALUES ('activity', NULL, 100, 'erase')`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO activity (id, kind, subject, body, occurred_at, source, captured_by)
			VALUES ($1, 'email', 'Order confirmation', 'commercial content', now() - interval '400 days', 'capture', 'connector:t')`,
			email); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO activity (id, kind, subject, body, occurred_at, source, captured_by)
			VALUES ($1, 'note', 'Old scratch note', 'ephemeral', now() - interval '400 days', 'capture', 'connector:t')`,
			note); err != nil {
			return err
		}
		// A channel message is a Handelsbrief too, and ADR-0107/A158 RATIFIES
		// that rather than leaving it to fall out of the predicate's shape.
		// The rule is stated as an exclusion (everything but task and note), so
		// the narrowing carried every transport into the floor at once where
		// telegram and whatsapp used to enter it by name — and that is correct:
		// a message to a customer is external business correspondence whichever
		// transport carried it. Pinned here so a later narrowing of the
		// predicate to a NAMED list fails instead of silently unshielding it.
		if _, err := tx.Exec(ctx, `
			INSERT INTO activity (id, kind, channel_provider, subject, body, occurred_at, source, captured_by)
			VALUES ($1, 'message', 'telegram', NULL, 'commercial content', now() - interval '400 days', 'capture', 'connector:t')`,
			message); err != nil {
			return err
		}
		// The §147(4) boundary: a January email six-and-a-half years old.
		// An occurrence-anchored 6y floor would already expose it; the
		// calendar-year-end anchor keeps it until its year's end + 6y.
		if _, err := tx.Exec(ctx, `
			INSERT INTO activity (id, kind, subject, body, occurred_at, source, captured_by)
			VALUES ($1, 'email', 'January Handelsbrief', 'commercial content',
			        date_trunc('year', now() - interval '6 years') + interval '14 days', 'capture', 'connector:t')`,
			janEmail); err != nil {
			return err
		}
		// The control: same age, same kind, no transaction behind it.
		if _, err := tx.Exec(ctx, `
			INSERT INTO activity (id, kind, subject, body, occurred_at, source, captured_by)
			VALUES ($1, 'email', 'Lunch next Tuesday?', 'no transaction here', now() - interval '400 days', 'capture', 'connector:t')`,
			unlinkedEmail); err != nil {
			return err
		}
		// What makes the other three Handelsbriefe: a WON deal they are filed
		// against. Before A165 the predicate shielded by exclusion and needed
		// no deal at all; now the transaction is the thing the obligation
		// hangs off, so the fixture has to supply one or it proves nothing.
		pipeline, stage, deal := ids.NewV7(), ids.NewV7(), ids.NewV7()
		if _, err := tx.Exec(ctx, `
			INSERT INTO pipeline (id, name, is_default, position)
			VALUES ($1, 'Default', true, 0)`, pipeline); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO stage (id, pipeline_id, name, position, semantic, win_probability)
			VALUES ($1, $2, 'Closed Won', 0, 'won', 100)`, stage, pipeline); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO deal (id, name, status, pipeline_id, stage_id, closed_at, source, captured_by)
			VALUES ($1, 'Acme rollout', 'won', $2, $3, now(), 'api', 'human:t')`,
			deal, pipeline, stage); err != nil {
			return err
		}
		for _, a := range []ids.UUID{email, janEmail, message} {
			if _, err := tx.Exec(ctx, `
				INSERT INTO activity_link (activity_id, entity_type, deal_id)
				VALUES ($1, 'deal', $2)`, a, deal); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	svc := privacy.NewRetentionService(e.DB(), nil, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err := svc.EvaluateInstallation(RetentionPassCtx(e.WS)); err != nil {
		t.Fatal(err)
	}

	var emailBody, noteBody, janBody, messageBody, unlinkedBody *string
	err = database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(context.Background(), `SELECT body FROM activity WHERE id = $1`, email).Scan(&emailBody); err != nil {
			return err
		}
		if err := tx.QueryRow(context.Background(), `SELECT body FROM activity WHERE id = $1`, janEmail).Scan(&janBody); err != nil {
			return err
		}
		if err := tx.QueryRow(context.Background(), `SELECT body FROM activity WHERE id = $1`, message).Scan(&messageBody); err != nil {
			return err
		}
		if err := tx.QueryRow(context.Background(), `SELECT body FROM activity WHERE id = $1`, unlinkedEmail).Scan(&unlinkedBody); err != nil {
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
	if janBody == nil {
		t.Error("the §147(4) anchor failed: a January email inside its calendar-year-end window was destroyed (occurrence anchoring erases it ~11 months early)")
	}
	if messageBody == nil {
		t.Error("the GoBD floor failed: a 400-day-old channel message was destroyed against the 6-year statute — a message is correspondence whatever transport carried it")
	}
	if noteBody != nil {
		t.Error("the floor over-shielded: a plain note past the policy age survived")
	}
	if unlinkedBody != nil {
		t.Error("the floor over-shielded: a 400-day-old email concerning no transaction survived — A165 narrowed the floor to Handelsbriefe, and shielding ordinary mail is the over-retention it removed")
	}
}
