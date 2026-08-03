// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// telegramIngestWorker over real migrated Postgres: the worker's own
// contract is transactional (re-establishing tenant context from job args,
// and never swallowing a Sink failure), neither of which a mock connection
// can prove — a mock only proves the mock's own bookkeeping.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// telegramIngestFixture seeds one connected channel_connection (bot "42")
// and one raw_capture row holding a text message from chat 1001, message id
// 7, sender 555 — the same numbers normalize_test.go's fixture uses, so a
// captured activity's natural key is checkable against the identical
// literal. Returns the ids the job args name.
func telegramIngestFixture(t *testing.T, e *integration.Env) (connID, rawID ids.UUID) {
	t.Helper()
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	connID, rawID = ids.NewV7(), ids.NewV7()
	err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO channel_connection
				(id, workspace_id, provider, channel_id, channel_label, credential_ref, status, connected_by)
			VALUES ($1, $2, 'telegram', '42', 'acme_bot', 'cred-ref', 'connected', $3)`,
			connID, e.WS, e.Rep1); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO raw_capture (id, workspace_id, source_system, source_id, payload)
			VALUES ($1, $2, 'telegram', '100', $3)`,
			rawID, e.WS, []byte(telegramIngestUpdateJSON))
		return err
	})
	if err != nil {
		t.Fatalf("seeding the channel connection and raw capture: %v", err)
	}
	return connID, rawID
}

const telegramIngestUpdateJSON = `{
	"update_id": 100,
	"message": {
		"message_id": 7,
		"chat": {"id": 1001, "type": "private", "username": "annlee"},
		"from": {"id": 555, "username": "annlee", "first_name": "Ann", "last_name": "Lee"},
		"date": 1690000000,
		"text": "hello"
	}
}`

// telegramIngestKickedJSON is what Telegram actually posts when customer 556
// blocks bot 42 — the design §4.2 D9 reachability signal, never a message.
//
// The shape is the whole point of the fixture. my_chat_member reports a change
// to THE BOT's own membership, so both chat members here are bot 42 and the
// customer appears only as the private chat (whose id IS their user id) and as
// `from`. A fixture that instead put 556 in new_chat_member.user would agree
// with a parser reading the identity from there and prove nothing about
// production, where that field is the bot and the reachability write lands on
// no Person at all.
const telegramIngestKickedJSON = `{
	"update_id": 200,
	"my_chat_member": {
		"chat": {"id": 556, "type": "private", "username": "blockeduser", "first_name": "Blocks"},
		"from": {"id": 556, "username": "blockeduser", "first_name": "Blocks"},
		"date": 1690000200,
		"old_chat_member": {"user": {"id": 42, "is_bot": true, "username": "acme_bot"}, "status": "member"},
		"new_chat_member": {"user": {"id": 42, "is_bot": true, "username": "acme_bot"}, "status": "kicked"}
	}
}`

// The job queue is not a request: ctx carries no ambient workspace, so the
// worker must build its tenant context ENTIRELY from job.Args — a worker
// that instead inherited (or defaulted) would either fail outright under
// RLS or, worse, silently land the activity in the wrong tenant.
func TestIngestWorkerReestablishesWorkspaceContextFromArgs(t *testing.T) {
	e := integration.Setup(t)
	connID, rawID := telegramIngestFixture(t, e)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	worker := newTelegramIngestWorker(e.Pool, CaptureConfig{}, quiet)
	// context.Background(): deliberately no principal.WithWorkspaceID here —
	// proving the worker itself resolves the tenant from job.Args, not from
	// whatever the caller happened to have bound.
	err := worker.Work(context.Background(), &river.Job[TelegramIngestArgs]{
		Args: TelegramIngestArgs{Workspace: e.WS, ConnectionID: connID.String(), BotID: "42", RawCaptureID: rawID.String()},
	})
	if err != nil {
		t.Fatalf("Work: %v", err)
	}

	// Read back under the SAME workspace the args named — if the worker had
	// resolved the wrong tenant (or none), this activity would not be here.
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	var sourceID string
	err = database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT source_id FROM activity WHERE workspace_id = $1 AND source_system = 'telegram'`, e.WS,
		).Scan(&sourceID)
	})
	if err != nil {
		t.Fatalf("reading back the captured activity: %v", err)
	}
	if sourceID != "42:1001:7" {
		t.Errorf("activity.source_id = %q, want %q", sourceID, "42:1001:7")
	}
}

// uniqueViolationSink is a connector.Sink stand-in whose Upsert always fails
// with the exact SQLSTATE a real dedupe race would raise — proving the
// worker's OWN contract (propagate, never swallow) without needing to
// actually win a race against Postgres.
type uniqueViolationSink struct{}

func (uniqueViolationSink) Upsert(context.Context, connector.NormalizedRecord) (datasource.EntityRef, error) {
	return datasource.EntityRef{}, &pgconn.PgError{Code: "23505", ConstraintName: "uq_person_channel_identity"}
}

// A unique violation during capture is retryable, never poison (design
// §6.3): two concurrent first messages from one new sender both resolve to
// no-match, and the partial unique index breaks the tie exactly as
// uq_person_email_dedupe does for mail. The loser MUST redeliver so its lane
// hits the winner — classifying it as poison (swallowing it, logging and
// returning nil) would silently drop a customer's message.
func TestIngestWorkerTreatsAUniqueViolationAsRetryable(t *testing.T) {
	e := integration.Setup(t)
	connID, rawID := telegramIngestFixture(t, e)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	worker := &telegramIngestWorker{pool: e.Pool, sink: uniqueViolationSink{}, log: quiet}
	err := worker.Work(context.Background(), &river.Job[TelegramIngestArgs]{
		Args: TelegramIngestArgs{Workspace: e.WS, ConnectionID: connID.String(), BotID: "42", RawCaptureID: rawID.String()},
	})
	if err == nil {
		t.Fatal("Work returned nil — a unique violation must propagate so River redelivers, not be swallowed")
	}
	if strings.Contains(err.Error(), "42:1001:7") {
		t.Errorf("the persisted River error contains the Telegram natural key %q", "42:1001:7")
	}
	if !storekit.IsUniqueViolation(err) {
		t.Errorf("got %v, want an error still classifiable as a unique violation (the worker rewrapped rather than propagated it)", err)
	}
	// errors.Is confirms the SAME error rides the chain, not merely a
	// same-shaped replacement the worker minted itself.
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != "uq_person_channel_identity" {
		t.Errorf("got %v, want the original constraint name preserved", err)
	}
}

// TestIngestWorkerAppliesMembershipWithoutCapturingAnActivity is the wiring
// proof design §4.2 D9 depends on: a my_chat_member update reaches the SAME
// worker path a message does, yet it must never produce an activity, and it
// must land as the reachability change it actually is.
func TestIngestWorkerAppliesMembershipWithoutCapturingAnActivity(t *testing.T) {
	e := integration.Setup(t)
	connID, _ := telegramIngestFixture(t, e)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	// A prior message already bound sender 556 to a person — the
	// my_chat_member update below reports THAT account blocking the bot.
	person := e.SeedPerson(t, "Blocks The Bot", nil)
	e.WsExec(t, `
		INSERT INTO person_channel_identity
		  (workspace_id, person_id, provider, channel_user_id, username, source, captured_by)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
		        $1, 'telegram', '556', 'blockeduser', 'telegram', 'connector:telegram')`,
		person)

	rawID := ids.NewV7()
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO raw_capture (id, workspace_id, source_system, source_id, payload)
			VALUES ($1, $2, 'telegram', '200', $3)`,
			rawID, e.WS, []byte(telegramIngestKickedJSON))
		return err
	}); err != nil {
		t.Fatalf("seeding the raw membership update: %v", err)
	}

	worker := newTelegramIngestWorker(e.Pool, CaptureConfig{}, quiet)
	err := worker.Work(context.Background(), &river.Job[TelegramIngestArgs]{
		Args: TelegramIngestArgs{Workspace: e.WS, ConnectionID: connID.String(), BotID: "42", RawCaptureID: rawID.String()},
	})
	if err != nil {
		t.Fatalf("Work: %v", err)
	}

	if n := e.WsCount(t, `SELECT count(*) FROM activity WHERE workspace_id = $1 AND source_system = 'telegram'`, e.WS); n != 0 {
		t.Errorf("%d activity rows after a my_chat_member update, want 0 — it is a reachability signal, never a message", n)
	}
	if n := e.WsCount(t, `
		SELECT count(*) FROM person_channel_identity
		 WHERE channel_user_id = '556' AND archived_at IS NULL AND blocked_at IS NOT NULL`); n != 1 {
		t.Errorf("%d channel identity rows carry blocked_at after a kicked status, want 1 — "+
			"the update's identity is the private chat's, and the bot's id (42) matches no Person", n)
	}
}

// telegramIngestGroupJSON is a message in a supergroup the bot was added to:
// Telegram delivers it under the same bare `message` update a private chat
// uses, which is why the exclusion cannot be expressed in allowed_updates.
const telegramIngestGroupJSON = `{
	"update_id": 300,
	"message": {
		"message_id": 12,
		"chat": {"id": -1001234567890, "type": "supergroup", "title": "Acme Support"},
		"from": {"id": 557, "username": "groupmember", "first_name": "Group"},
		"date": 1690000500,
		"text": "/help please"
	}
}`

// Group chats are out of scope (design §1) and the worker must ACK the delivery
// while capturing nothing: a captured group message mints a Person per member
// the bot's privacy mode happens to show, files the activity under the group's
// thread, and then routes the rep's reply through the sender's channel identity
// to their PRIVATE chat — answering somewhere other than where it was read, or
// refused outright by a user who never started the bot.
func TestIngestWorkerCapturesNothingFromAGroupChat(t *testing.T) {
	e := integration.Setup(t)
	connID, _ := telegramIngestFixture(t, e)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	rawID := ids.NewV7()
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO raw_capture (id, workspace_id, source_system, source_id, payload)
			VALUES ($1, $2, 'telegram', '300', $3)`,
			rawID, e.WS, []byte(telegramIngestGroupJSON))
		return err
	}); err != nil {
		t.Fatalf("seeding the raw group-chat update: %v", err)
	}

	worker := newTelegramIngestWorker(e.Pool, CaptureConfig{}, quiet)
	// nil, not an error: a scope exclusion is a deliberate skip, and River
	// retrying it forever would be a fault report for working as designed.
	if err := worker.Work(context.Background(), &river.Job[TelegramIngestArgs]{
		Args: TelegramIngestArgs{Workspace: e.WS, ConnectionID: connID.String(), BotID: "42", RawCaptureID: rawID.String()},
	}); err != nil {
		t.Fatalf("Work: %v", err)
	}

	if n := e.WsCount(t, `SELECT count(*) FROM activity WHERE workspace_id = $1 AND source_system = 'telegram'`, e.WS); n != 0 {
		t.Errorf("%d activity rows after a group-chat message, want 0", n)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM person_channel_identity WHERE channel_user_id = '557'`); n != 0 {
		t.Errorf("%d channel identities minted for a group member, want 0", n)
	}
}
