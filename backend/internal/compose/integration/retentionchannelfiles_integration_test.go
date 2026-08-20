// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The retention pin for channel correspondence: a message-kind activity is
// subject to retention exactly as a mail one is.
//
// The property is FREE today — the `activity/` selector filters on no kind, and
// the statutory floor shields every kind but task and note — and nothing
// asserted it. Adding a kind filter to that selector would keep every channel
// conversation forever while mail was purged on schedule, and no test would
// fail. It rides the fixture and the policy seed in retention_integration_test.go.

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

// seedAgedCorrespondence plants one mail and one channel activity of the SAME
// age, so the only difference between them is the transport.
//
// Inserted directly, matching seedOverAgeRecords in this suite: what is under
// test is the retention SELECTOR's reading of an activity row, and the row's
// kind and age are its inputs. Nothing here stands in for a writer whose
// behaviour the assertions depend on.
func seedAgedCorrespondence(t *testing.T, e *Env) (mail, channel ids.UUID) {
	t.Helper()
	mail, channel = ids.NewV7(), ids.NewV7()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(context.Background(),
			`INSERT INTO activity (id, kind, subject, body, occurred_at, source, captured_by)
			 VALUES ($1, 'email', 'Old thread', 'text', now() - interval '4000 days', 'capture', 'connector:gmail')`,
			mail); err != nil {
			return err
		}
		_, err := tx.Exec(context.Background(),
			`INSERT INTO activity (id, kind, channel_provider, body, occurred_at, source, captured_by)
			 VALUES ($1, 'message', 'telegram', 'text', now() - interval '4000 days', 'capture', 'connector:telegram')`,
			channel)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return mail, channel
}

// A message-kind activity is retained and released on the same schedule as a
// mail one. Both arms are asserted in ONE test deliberately: the answer comes
// from a single kind-blind query, so the only failure worth catching is that
// query starting to distinguish them — and a query that acts on neither would
// pass two separate tests written the obvious way.
func TestRetentionTreatsAChannelMessageLikeMail(t *testing.T) {
	e := Setup(t)
	SeedRetentionPolicies(t, e)
	mail, channel := seedAgedCorrespondence(t, e)

	svc := privacy.NewRetentionService(e.DB(), nil, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err := svc.EvaluateInstallation(RetentionPassCtx(e.WS)); err != nil {
		t.Fatal(err)
	}

	var mailArchived, channelArchived bool
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(context.Background(),
			`SELECT archived_at IS NOT NULL FROM activity WHERE id = $1`, mail).Scan(&mailArchived); err != nil {
			return err
		}
		return tx.QueryRow(context.Background(),
			`SELECT archived_at IS NOT NULL FROM activity WHERE id = $1`, channel).Scan(&channelArchived)
	})
	if err != nil {
		t.Fatal(err)
	}
	// The mail arm is the control: it proves the pass acted at all, so a green
	// channel arm below cannot be a pass that acted on nothing.
	if !mailArchived {
		t.Fatal("the over-age mail activity was not archived, so this pass acted on nothing and proves nothing about the channel arm")
	}
	if !channelArchived {
		t.Error("an over-age CHANNEL message survived a retention pass that archived its mail twin: " +
			"channel correspondence would be kept forever while mail is purged on schedule")
	}
}
