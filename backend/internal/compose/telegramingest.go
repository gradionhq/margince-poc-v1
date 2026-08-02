// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The Telegram ingest worker (design §6.3): the other half of the poller
// (telegrampoll.go), and the only place that closes the loop from a persisted raw
// update to a captured activity. It re-establishes the workspace context from its
// job args exactly as CaptureSyncArgs does (jobs_capture.go), reads back what the
// poll wrote in the SAME transaction it was written in, joins the bot id that poll
// pinned onto the payload (capture/telegram's Normalize is pure and knows nothing
// of connections), and hands every resulting record to the ONE guarded Sink every
// capture path shares.

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
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// TelegramIngestArgs is the durable work request one poll's transaction enqueues
// alongside each raw row. The worker below re-establishes the workspace context
// from these args (as CaptureSyncArgs already does) and runs Normalize → Sink.
// RawCaptureID names the exact row to normalize, so a re-delivered update that
// refreshed an existing raw_capture row (rather than minting a new one) still
// points the job at the right payload.
//
// BotID is the bot that RECEIVED this update, pinned here by the poll that read
// it, and never resolved from the connection row when the job runs. ReplaceToken
// re-points a live connection at a different bot in place, so the row's channel_id
// is mutable state: a job reading it later would build the message's natural key
// and thread_key from whichever bot is current, filing one bot's message into
// another bot's conversation — and because Telegram's message ids restart per chat
// per bot, that re-keyed natural key can equal a real message of the new bot's,
// whereupon the Sink's idempotent upsert merges two different customers' messages
// into one activity. The bot an update arrived through is a fact about that
// arrival, so it travels with it.
//
// A field constant per raw row does not weaken river's ByArgs dedupe: every
// re-delivery of one update resolves to the same raw row and therefore the same
// bot.
type TelegramIngestArgs struct {
	Workspace ids.UUID `json:"workspace_id"`
	// ConnectionID names which connection read the update. The worker resolves
	// nothing from it — that is the point of BotID — but it is the operational link
	// between a queued job and the connection an operator is looking at, and the
	// key the ingest jobs are queried by.
	ConnectionID string `json:"connection_id"`
	BotID        string `json:"bot_id"`
	RawCaptureID string `json:"raw_capture_id"`
}

// Kind names this job to River.
func (TelegramIngestArgs) Kind() string { return "telegram_ingest" }

// WorkspaceID binds this raw capture's ingest to its tenant (jobs.WorkspaceScoped).
func (a TelegramIngestArgs) WorkspaceID() ids.UUID { return a.Workspace }

// telegramIngestWorker consumes TelegramIngestArgs: args → raw
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
// the raw update the poll persisted, and normalizes+captures. Every
// identity the message is keyed on comes from the args, which were stamped when
// the update was read. Every failure past that point — a missing raw row,
// a decode fault, a Sink error including a unique-constraint
// race the Sink's own idempotent upserts did not absorb — is returned
// unswallowed: River's retry is Telegram's ONLY recovery path (there is no
// history API to re-fetch a dropped update from), so treating any of these
// as poison would silently lose a customer's message rather than redeliver
// it (design §6.3).
func (w *telegramIngestWorker) Work(ctx context.Context, job *river.Job[TelegramIngestArgs]) error {
	rawID, err := ids.Parse(job.Args.RawCaptureID)
	if err != nil {
		return fmt.Errorf("telegram_ingest: raw capture id: %w", err)
	}

	wsCtx, err := workspaceJobCtx(ctx, job.Args)
	if err != nil {
		return err
	}

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
	// this update is pinned by the poll that read it, and the row's channel_id is mutable
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
		return w.applyMembership(actorCtx, job.Args.BotID, membership)
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
			return fmt.Errorf("telegram_ingest: normalized record carries %T, want telegram.ActivityFields", rec.Fields)
		}
		rec.Fields = capture.ActivityFields{
			Kind: fields.Kind, Body: fields.Body, OccurredAt: fields.OccurredAt, Direction: fields.Direction,
		}
		if _, err := w.sink.Upsert(actorCtx, rec); err != nil {
			if errors.Is(err, connector.ErrSkip) {
				// A deliberate refusal, counted like Normalize's above and like
				// every other connector's — not a fault. The Sink refuses a
				// record naming an account on the erasure suppression list, and
				// returning that as a job error would retry it, fail the retry
				// differently once the raw row is purged, and leave an operator
				// a permanently errored job that reads like an outage.
				//
				// The log names the job, never the record: a channel record's
				// natural key embeds the account id, which is precisely what the
				// erasure removed.
				w.log.InfoContext(actorCtx, "telegram_ingest: refused a record naming an erased channel account")
				continue
			}
			// River persists returned job errors. A Telegram natural key contains
			// the private-chat id, which is the customer's account id, so it must
			// not leave this worker even when capture itself failed.
			return fmt.Errorf("telegram_ingest: capturing an update: %w", err)
		}
	}
	return nil
}

// applyMembership carries out the reachability change a my_chat_member
// update reports (design §4.2 D9): kicked sets blocked_at, member clears it.
// Only a PRIVATE-chat update reaches here — ParseMembership declines every
// other chat type — so the group-only standings are not a case this switch
// has to answer. A status it does not classify is logged rather than
// absorbed, so one Telegram adds later is visible instead of falling through
// quietly.
//
// actorCtx carries the workspace this job's args resolved plus the connector
// principal Work established, so SetChannelIdentityBlocked runs under the
// SAME tenant as the read and under a named actor its audit row can be
// attributed to. It gets its own transaction: there is no invariant tying
// this write to the earlier read, unlike a captured record's atomic write
// shape.
//
// The bot the update arrived through travels with it from the job args, for
// the same reason the envelope reads it from there: the connection row's
// channel_id is mutable, and the watermark this write orders itself by is that
// bot's own update sequence.
func (w *telegramIngestWorker) applyMembership(actorCtx context.Context, botID string, m telegram.Membership) error {
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
		// The account lock and the update watermark answer two different
		// questions and neither substitutes for the other: the lock keeps this
		// write from interleaving with an erasure of the same human, the
		// watermark keeps two transitions from applying in the wrong order.
		if err := storekit.LockChannelIdentities(actorCtx, tx, []storekit.ChannelIdentityKey{
			{Provider: m.Identity.Provider, ChannelUserID: m.Identity.ChannelUserID},
		}); err != nil {
			return err
		}
		return w.people.SetChannelIdentityBlocked(actorCtx, tx, m.Identity, blocked, botID, m.UpdateID)
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
