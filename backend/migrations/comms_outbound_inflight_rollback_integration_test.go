// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package migrations

// 0156's rollback proven directly. The in-flight marker is the ONLY record that
// a channel message may already be with the customer, and a channel seam has no
// prior-send lookup to reconstruct it from. So reversing the migration is not a
// schema question: the rows whose outcome was never learned have to be closed
// before the evidence goes, or a re-applied 0156 hands them back to the runner
// looking untried and the customer gets a second copy.
//
// `migrate down` defaults to one step and reverts newest-first, so 0156 comes
// off ALONE — 0155's blanket DELETE of the channel rows does not run, and those
// rows are still there to be redelivered.

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
)

func TestRollingBackTheInflightMarkerParksUnlearnedSends(t *testing.T) {
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
	ws := seedWorkspace(t, conn, "inflight-rollback")

	// A transmission whose outcome was never learned: the worker committed the
	// marker and died before the provider answered.
	unlearned := seedChannelDeliveryForRollback(t, conn, ws, true)
	// And one the dispatcher never picked up, which a rollback must leave alone —
	// parking it would strand a message nobody ever tried to send.
	untried := seedChannelDeliveryForRollback(t, conn, ws, false)

	reverted, err := dbmigrate.Down(ctx, conn, core, 1)
	if err != nil {
		t.Fatalf("reverting 0156: %v", err)
	}
	if reverted != 1 {
		t.Fatalf("reverted %d migrations, want exactly 1 — this test is no longer exercising 0156 alone", reverted)
	}

	status, reason := readDeliveryOutcome(t, conn, ws, unlearned)
	if status != "parked" {
		t.Errorf("a delivery whose outcome was never learned is %q after the rollback; the runner will send it again", status)
	}
	if !strings.Contains(reason, "never confirmed") || !strings.Contains(reason, "will not be retried") {
		t.Errorf("park reason = %q; it must say the outcome is unknown and that nothing will retry it", reason)
	}

	if status, _ := readDeliveryOutcome(t, conn, ws, untried); status != "pending" {
		t.Errorf("a delivery that never reached the provider is %q after the rollback, want pending", status)
	}
}

// seedChannelDeliveryForRollback writes one pending channel-shaped delivery and
// the activity it reports on. inFlight decides whether a transmission was
// outstanding when the rollback ran — the only thing that tells the two rows
// apart.
func seedChannelDeliveryForRollback(t *testing.T, conn *pgx.Conn, ws string, inFlight bool) string {
	t.Helper()
	var deliveryID string
	if err := withGUC(t, conn, ws, func(tx pgx.Tx) error {
		ctx := context.Background()
		wsClause := `NULLIF(current_setting('app.workspace_id', true), '')::uuid`
		var userID, activityID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO app_user (workspace_id, email, display_name)
			VALUES (`+wsClause+`, 'rep-' || gen_random_uuid() || '@inflight.test', 'Rep')
			RETURNING id`).Scan(&userID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO activity (workspace_id, kind, body, direction, occurred_at, source, captured_by)
			VALUES (`+wsClause+`, 'telegram', 'Shipping Monday.', 'outbound', now(), 'manual', 'human:test')
			RETURNING id`).Scan(&activityID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			INSERT INTO comms_outbound
			  (id, workspace_id, activity_id, user_id, provider, channel_user_id,
			   body, consent_purpose, cc, references_chain, status, inflight_at)
			VALUES (uuidv7(), `+wsClause+`, $1, $2, 'telegram', '770011',
			        'Shipping Monday.', 'transactional', NULL, NULL, 'pending',
			        CASE WHEN $3 THEN now() END)
			RETURNING id`, activityID, userID, inFlight).Scan(&deliveryID)
	}); err != nil {
		t.Fatalf("seeding a channel delivery (in flight = %v): %v", inFlight, err)
	}
	return deliveryID
}

func readDeliveryOutcome(t *testing.T, conn *pgx.Conn, ws, id string) (status, reason string) {
	t.Helper()
	if err := withGUC(t, conn, ws, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT status, coalesce(reason, '') FROM comms_outbound WHERE id = $1`, id).Scan(&status, &reason)
	}); err != nil {
		t.Fatalf("reading delivery %s: %v", id, err)
	}
	return status, reason
}
