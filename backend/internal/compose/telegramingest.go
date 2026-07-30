// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The Telegram ingest worker (design §6.3): the other half of Task 8's
// webhook, and the only place that closes the loop from a persisted raw
// update to a captured activity. It re-establishes the workspace context
// from its job args exactly as CaptureSyncArgs does (jobs_capture.go), reads
// back what the webhook wrote in the SAME transaction it was written in,
// joins the bot id the delivery pinned onto the payload (capture/telegram's
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
	"github.com/gradionhq/margince/backend/internal/modules/people"
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
	pool   *pgxpool.Pool
	sink   connector.Sink
	people *people.Store
	log    *slog.Logger
}

// newTelegramIngestWorker builds the worker over the SAME fully-guarded Sink
// every other capture connector shares (newCaptureSink) — Telegram is one
// more source into the one chokepoint, not a second one. people is the SAME
// module the Sink's channel ensurer resolves through (compose/capture.go's
// peopleEnsurer) — composed here directly rather than through an interface
// seam because this IS the composition layer people.Store already reaches
// into for that ensurer.
func newTelegramIngestWorker(pool *pgxpool.Pool, cfg CaptureConfig, log *slog.Logger) *telegramIngestWorker {
	return &telegramIngestWorker{pool: pool, sink: newCaptureSink(pool, cfg), people: people.NewStore(pool), log: log}
}

// Work re-establishes the workspace context from job.Args (never inherited
// from ctx, which carries none — the job queue is not a request), reads back
// the raw update the webhook persisted, and normalizes+captures. Every
// identity the message is keyed on comes from the args, which were stamped at
// delivery. Every failure past that point — a missing raw row,
// a decode fault, a Sink error including a unique-constraint
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
	rawID, err := ids.Parse(job.Args.RawCaptureID)
	if err != nil {
		return fmt.Errorf("telegram_ingest: raw capture id: %w", err)
	}

	wsCtx := principal.WithWorkspaceID(ctx, ws)

	var payload []byte
	err = database.WithWorkspaceTx(wsCtx, w.pool, func(tx pgx.Tx) error {
		var err error
		payload, err = capture.GetRawCapturePayloadTx(wsCtx, tx, rawID)
		return err
	})
	if err != nil {
		return fmt.Errorf("telegram_ingest: reading the raw payload: %w", err)
	}

	// job.Args.BotID, never a read of the connection row: the bot that received
	// this update is pinned at delivery, and the row's channel_id is mutable
	// (ReplaceToken re-points a live connection at a different bot). See
	// TelegramIngestArgs for what re-keying an already-delivered message costs.
	raw, err := telegram.BuildRawEnvelope(job.Args.BotID, payload)
	if err != nil {
		return fmt.Errorf("telegram_ingest: building the normalize envelope: %w", err)
	}

	// Everything past the read is a WRITE, and every write in this repo is
	// attributed: the connector principal and one correlation id per update
	// are established here, once, so both branches below — the reachability
	// change and the captured message — commit under the same named actor
	// and the same trace. A write reached under wsCtx alone would have no
	// actor for storekit to stamp.
	actorCtx := principal.WithCorrelationID(principal.WithActor(wsCtx, telegramChannelPrincipal()), ids.NewV7())

	// A my_chat_member update is not a message (design §4.2 D9): classify it
	// BEFORE Normalize ever runs, so it can never take the message path or
	// mint an activity. Every other update kind falls through unchanged.
	membership, isMembership, err := telegram.ParseMembership(raw)
	if err != nil {
		return fmt.Errorf("telegram_ingest: parsing membership: %w", err)
	}
	if isMembership {
		return w.applyMembership(actorCtx, membership)
	}

	records, err := telegram.Normalize(actorCtx, raw)
	if err != nil {
		if errors.Is(err, connector.ErrSkip) {
			// A deliberate exclusion — an update kind neither the membership
			// classification above nor Normalize itself parses as a message
			// (an edited_message, say) — counted, never a fault.
			return nil
		}
		return fmt.Errorf("telegram_ingest: normalizing: %w", err)
	}

	return w.captureRecords(actorCtx, records)
}

// captureRecords hands every normalized record to the one guarded Sink,
// translating the Fields type on the way.
func (w *telegramIngestWorker) captureRecords(actorCtx context.Context, records []connector.NormalizedRecord) error {
	for _, rec := range records {
		// Normalize returns its own package-local mirror of
		// capture.ActivityFields (normalize.go explains why: capture already
		// imports capture/telegram, so the reverse import would cycle). This
		// is the one place that legitimately imports both, so the 1:1
		// translation into the concrete type Sink.Upsert's switch recognizes
		// happens here, immediately before the record reaches it.
		fields, ok := rec.Fields.(telegram.ActivityFields)
		if !ok {
			// Discarding the assertion here would translate an unrecognized
			// Fields type into a zero-valued activity: a captured message with
			// no body, no direction and no occurrence time, committed as though
			// it were real. Failing names the type instead.
			return fmt.Errorf("telegram_ingest: update %s carries %T, want telegram.ActivityFields",
				rec.NaturalKey.SourceID, rec.Fields)
		}
		rec.Fields = capture.ActivityFields{
			Kind: fields.Kind, Body: fields.Body, OccurredAt: fields.OccurredAt, Direction: fields.Direction,
		}
		if _, err := w.sink.Upsert(actorCtx, rec); err != nil {
			return fmt.Errorf("telegram_ingest: capturing update %s: %w", rec.NaturalKey.SourceID, err)
		}
	}
	return nil
}

// applyMembership carries out the reachability change a my_chat_member
// update reports (design §4.2 D9): kicked sets blocked_at, member clears it.
// Every other status a chat_member update can name — left, restricted,
// administrator, creator — is a real value of the SAME Telegram field on a
// GROUP chat's update; a private bot chat never sends the latter three, and
// "left" changes nothing this system tracks (there is no reachability edge
// crossed, and no prior message means no row to touch either way). None of
// them are silently absorbed: an update this worker does not classify as
// kicked/member is logged so a status Telegram adds later is visible instead
// of quietly falling through.
//
// actorCtx carries the workspace this job's args resolved plus the connector
// principal Work established, so SetChannelIdentityBlocked runs under the
// SAME tenant as the read and under a named actor its audit row can be
// attributed to. It gets its own transaction: there is no invariant tying
// this write to the earlier read, unlike a captured record's atomic write
// shape.
func (w *telegramIngestWorker) applyMembership(actorCtx context.Context, m telegram.Membership) error {
	var blocked bool
	switch m.Status {
	case telegram.StatusKicked:
		blocked = true
	case telegram.StatusMember:
		blocked = false
	default:
		w.log.Warn("telegram_ingest: unhandled my_chat_member status", "status", m.Status)
		return nil
	}
	err := database.WithWorkspaceTx(actorCtx, w.pool, func(tx pgx.Tx) error {
		return w.people.SetChannelIdentityBlocked(actorCtx, tx, m.Identity, blocked)
	})
	if err != nil {
		return fmt.Errorf("telegram_ingest: applying membership status %q: %w", m.Status, err)
	}
	return nil
}

// telegramChannelPrincipal is design §6.4's workspace-channel connector
// identity: deliberately NOT channel_connection.connected_by. That column is
// audit-only on the connection row itself (channelconn.go's channelActor
// comment) — reusing it as this principal's UserID/OnBehalfOf would make
// every captured message look like the connecting admin's own row-scoped
// activity, which is exactly the "owned record" §4.1 forbids. Its
// permissions are the fixed minimum this worker exercises — the activity it
// captures, and the person the channel ensure auto-creates for an unmatched
// sender (design D1) — and workspace-wide (RowScopeAll): a channel message
// belongs to the whole workspace a single bot serves, not to whichever human
// happened to run Connect.
func telegramChannelPrincipal() principal.Principal {
	return principal.Principal{
		Type: principal.PrincipalConnector,
		ID:   telegram.CapturedByTelegram,
		Permissions: principal.Permissions{
			RoleKeys: []string{"channel"},
			Objects: map[string]principal.ObjectGrant{
				tableActivity: {Create: true},
				tablePerson:   {Create: true},
			},
			RowScope: principal.RowScopeAll,
		},
	}
}
