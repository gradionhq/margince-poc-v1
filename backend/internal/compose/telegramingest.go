// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The Telegram ingest worker (design §6.3): the other half of Task 8's
// webhook, and the only place that closes the loop from a persisted raw
// update to a captured activity. It re-establishes the workspace context
// from its job args exactly as CaptureSyncArgs does (jobs_capture.go), reads
// back what the webhook wrote in the SAME transaction it was written in,
// joins the connection's bot id onto the payload (capture/telegram's
// Normalize is pure and knows nothing of connections), and hands every
// resulting record to the ONE guarded Sink every capture path shares.
package compose

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/capture/telegram"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// telegramIngestWorker consumes Task 8's TelegramIngestArgs: args → raw
// payload → Normalize → Sink. sink is the connector.Sink SEAM, not the
// concrete *capture.Sink, so a test can inject a fake that fails in a
// specific, controlled way (a unique-constraint race) without a real
// Postgres write path to provoke one.
type telegramIngestWorker struct {
	river.WorkerDefaults[TelegramIngestArgs]
	pool *pgxpool.Pool
	sink connector.Sink
	log  *slog.Logger
}

// newTelegramIngestWorker builds the worker over the SAME fully-guarded Sink
// every other capture connector shares (newCaptureSink) — Telegram is one
// more source into the one chokepoint, not a second one.
func newTelegramIngestWorker(pool *pgxpool.Pool, cfg CaptureConfig, log *slog.Logger) *telegramIngestWorker {
	return &telegramIngestWorker{pool: pool, sink: newCaptureSink(pool, cfg), log: log}
}

// Work re-establishes the workspace context from job.Args (never inherited
// from ctx, which carries none — the job queue is not a request), reads back
// the connection's bot id and the raw update the webhook persisted, and
// normalizes+captures. Every failure past that point — a vanished
// connection, a decode fault, a Sink error including a unique-constraint
// race the Sink's own idempotent upserts did not absorb — is returned
// unswallowed: River's retry is Telegram's ONLY recovery path (there is no
// history API to re-fetch a dropped update from), so treating any of these
// as poison would silently lose a customer's message rather than redeliver
// it (design §6.3).
func (w *telegramIngestWorker) Work(ctx context.Context, job *river.Job[TelegramIngestArgs]) error {
	ws, err := ids.Parse(job.Args.Workspace)
	if err != nil {
		return fmt.Errorf("telegram_ingest: workspace id: %w", err)
	}
	connID, err := ids.Parse(job.Args.ConnectionID)
	if err != nil {
		return fmt.Errorf("telegram_ingest: connection id: %w", err)
	}
	rawID, err := ids.Parse(job.Args.RawCaptureID)
	if err != nil {
		return fmt.Errorf("telegram_ingest: raw capture id: %w", err)
	}

	wsCtx := principal.WithWorkspaceID(ctx, ws)

	var botID string
	var payload []byte
	err = database.WithWorkspaceTx(wsCtx, w.pool, func(tx pgx.Tx) error {
		var err error
		botID, err = capture.ChannelBotID(ctx, tx, connID)
		if err != nil {
			return err
		}
		payload, err = capture.GetRawCapturePayloadTx(ctx, tx, rawID)
		return err
	})
	if err != nil {
		return fmt.Errorf("telegram_ingest: reading the connection and raw payload: %w", err)
	}

	raw, err := telegram.BuildRawEnvelope(botID, payload)
	if err != nil {
		return fmt.Errorf("telegram_ingest: building the normalize envelope: %w", err)
	}
	records, err := telegram.Normalize(ctx, raw)
	if err != nil {
		if errors.Is(err, connector.ErrSkip) {
			// A deliberate exclusion (my_chat_member, any update kind this
			// pass does not capture as a message) — counted, never a fault.
			return nil
		}
		return fmt.Errorf("telegram_ingest: normalizing: %w", err)
	}

	actorCtx := principal.WithCorrelationID(principal.WithActor(wsCtx, telegramChannelPrincipal()), ids.NewV7())
	for _, rec := range records {
		// Normalize returns its own package-local mirror of
		// capture.ActivityFields (normalize.go explains why: capture already
		// imports capture/telegram, so the reverse import would cycle). This
		// is the one place that legitimately imports both, so the 1:1
		// translation into the concrete type Sink.Upsert's switch recognizes
		// happens here, immediately before the record reaches it.
		fields, _ := rec.Fields.(telegram.ActivityFields)
		rec.Fields = capture.ActivityFields{
			Kind: fields.Kind, Body: fields.Body, OccurredAt: fields.OccurredAt, Direction: fields.Direction,
		}
		if _, err := w.sink.Upsert(actorCtx, rec); err != nil {
			return fmt.Errorf("telegram_ingest: capturing update %s: %w", rec.NaturalKey.SourceID, err)
		}
	}
	return nil
}

// telegramChannelPrincipal is design §6.4's workspace-channel connector
// identity: deliberately NOT channel_connection.connected_by. That column is
// audit-only on the connection row itself (channelconn.go's channelActor
// comment) — reusing it as this principal's UserID/OnBehalfOf would make
// every captured message look like the connecting admin's own row-scoped
// activity, which is exactly the "owned record" §4.1 forbids. Its
// permissions are the fixed minimum this worker exercises (activity
// creation) and workspace-wide (RowScopeAll): a channel message belongs to
// the whole workspace a single bot serves, not to whichever human happened
// to run Connect.
func telegramChannelPrincipal() principal.Principal {
	return principal.Principal{
		Type: principal.PrincipalConnector,
		ID:   telegram.CapturedByTelegram,
		Permissions: principal.Permissions{
			RoleKeys: []string{"channel"},
			Objects:  map[string]principal.ObjectGrant{"activity": {Create: true}},
			RowScope: principal.RowScopeAll,
		},
	}
}
