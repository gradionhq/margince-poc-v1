// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Telegram ingress by long poll (design v2 §3). Telegram's two ingress modes are
// mutually exclusive per bot — it answers 409 to getUpdates while a webhook is
// registered, and only one getUpdates consumer may hold a bot at a time — so
// this is emphatically NOT Gmail's shape, where Pub/Sub only says "something
// changed" and push therefore layers over a poll that never stops. There is one
// ingress mode here, and it is this one.
//
// The SCHEDULING shape, though, is the one this repo already runs for Gmail's
// sync (jobs_capture.go): a periodic dispatcher that due-scans the fleet and
// enqueues one job per connection, rather than a long-running consumer of its
// own. Three guarantees, three mechanisms:
//
//   - ONE getUpdates per bot — TelegramPollArgs declares UniqueOpts on the args
//     TYPE, so a second poll job for the same connection cannot be in flight.
//     That uniqueness IS how Telegram's rule is satisfied.
//   - Replicas never double-poll — the dispatcher is periodic, and River elects
//     one leader for periodic jobs across the cluster, exactly as the sweeps do.
//   - Near-real-time without a busy loop — a 25s long poll costs one held worker
//     per bot instead of a hot loop. It is not continuous: a poll that COMES BACK
//     with updates ends its job, so the next one waits for the dispatcher's tick.
//     The honest bound is therefore one dispatcher interval of latency per inbound
//     message, and a backlog draining at one Bot API batch per tick.
//
// What a poll must never do is acknowledge a batch it has not stored: see
// telegramPollWorker.persist.

package compose

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/capture/telegram"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

const (
	// telegramPollTimeoutSeconds is how long Telegram holds the connection open
	// with nothing to report. Long enough that a 30s dispatcher tick is
	// effectively continuous; short enough to leave room inside the job timeout.
	telegramPollTimeoutSeconds = 25
	// telegramPollJobTimeout bounds one poll job. It must EXCEED the long poll
	// plus the headroom the client adds around it: a poll cancelled by its own
	// job timeout returns nothing, so its offset never advances and the
	// connection retries forever without making progress. Asserted rather than
	// assumed — telegrampoll_test.go.
	telegramPollJobTimeout = 2 * time.Minute
	// telegramPollSweepInterval is the dispatcher's cadence. Paired with the
	// long poll above it leaves at most a few seconds of every minute in which
	// a connection is not waiting on Telegram.
	telegramPollSweepInterval = 30 * time.Second
)

// registerTelegramPoll wires the pull-ingress half of the worker role: the
// leader-elected dispatcher and the per-connection poll worker, plus the periodic
// tick that drives them.
//
// Both halves are gated on the vault TOGETHER, and neither is registered without
// it: a dispatcher with no poll worker would queue jobs nothing works, and a poll
// worker with no way to unseal a bot's token could only fail every job it was
// handed. A nil api takes the real Bot API client — the acceptance suites pass a
// fake, because a poller left on the real client would reach api.telegram.org from
// a test run.
func registerTelegramPoll(workers *river.Workers, periodic []*river.PeriodicJob, pool *pgxpool.Pool, vault keyvault.Vault, api telegram.API, log *slog.Logger) []*river.PeriodicJob {
	if vault == nil {
		return periodic
	}
	if api == nil {
		api = telegram.NewAPI(nil, "")
	}
	river.AddWorker(workers, &telegramPollSweepWorker{pool: pool, log: log})
	river.AddWorker(workers, newTelegramPollWorker(pool, vault, api, ambientTelegramEnqueuer{}, log))
	return append(periodic, river.NewPeriodicJob(
		river.PeriodicInterval(telegramPollSweepInterval),
		func() (river.JobArgs, *river.InsertOpts) { return TelegramPollSweepArgs{}, sweepInsertOpts() },
		&river.PeriodicJobOpts{RunOnStart: true},
	))
}

// TelegramPollSweepArgs schedules one DISPATCH pass: due-scan the fleet for live
// channel connections and enqueue one poll job each. The dispatcher never polls
// inline — per-connection jobs isolate one bot's outage from every other bot's
// traffic, and only a per-connection job can carry the per-bot uniqueness
// Telegram's one-consumer rule needs.
type TelegramPollSweepArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (TelegramPollSweepArgs) Kind() string { return "telegram_poll_sweep" }

// FleetWide marks this a dispatcher: it enumerates and enqueues,
// and does no tenant work of its own (jobs.FleetWide).
func (TelegramPollSweepArgs) FleetWide() {}

// TelegramPollArgs polls ONE connection. The workspace travels in the args
// because a job queue is not a request and carries no tenant to inherit; the
// cursor deliberately does not, because it moves (see LoadChannelPollTarget).
type TelegramPollArgs struct {
	Workspace    ids.UUID `json:"workspace_id"`
	ConnectionID string   `json:"connection_id"`
}

// Kind is the stable job identifier River persists in river_job.
func (TelegramPollArgs) Kind() string { return "telegram_poll" }

// WorkspaceID binds this connection's poll to its tenant (jobs.WorkspaceScoped).
func (a TelegramPollArgs) WorkspaceID() ids.UUID { return a.Workspace }

// InsertOpts binds the uniqueness to the ARGS TYPE rather than to the one call
// site that enqueues today, because it is not a scheduling nicety: Telegram
// permits exactly one getUpdates consumer per bot, and two in-flight polls for
// one connection would steal each other's batches — each acknowledging updates
// the other was about to store. Declaring it here gives every inserter the rule.
//
// The state window excludes `completed` (activeSweepStates): a finished poll must
// not stop the next tick enqueueing the following one, or ingress would run
// exactly once per process.
func (TelegramPollArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: activeSweepStates}}
}

// telegramPollSweepWorker is the dispatcher: due-scan, then one insert per live
// connection. One connection's failed enqueue is logged and skipped; only a
// fleet-enumeration failure is returned, so River retries the tick rather than
// the fleet silently going unpolled.
type telegramPollSweepWorker struct {
	river.WorkerDefaults[TelegramPollSweepArgs]
	pool *pgxpool.Pool
	log  *slog.Logger
}

func (w *telegramPollSweepWorker) Work(ctx context.Context, _ *river.Job[TelegramPollSweepArgs]) error {
	client := river.ClientFromContext[pgx.Tx](ctx)
	due, enumErr := capture.DueChannelConnections(ctx, w.pool, capture.ProviderTelegram)
	for _, d := range due {
		// No InsertOpts here: TelegramPollArgs declares its own, so an inserter
		// cannot forget the per-bot uniqueness by omission.
		if _, err := client.Insert(ctx, TelegramPollArgs{
			Workspace: d.WorkspaceID, ConnectionID: d.ID.String(),
		}, nil); err != nil {
			w.log.WarnContext(ctx, "telegram poll enqueue failed", "connection", d.ID.String(), "err", err)
		}
	}
	return enumErr
}

// telegramEnqueuer is the slice of River's insert surface the poller needs,
// narrowed so a test can inject a failure INSIDE the real transaction without a
// real river client — mirrors deepReadEnqueuer (deepreadtransport.go).
type telegramEnqueuer interface {
	EnqueueTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) error
}

// ambientTelegramEnqueuer enqueues the ingest job through the River client that
// is working the current job, so the poll worker needs no second client of its
// own — and the insert rides the caller's transaction, which is what makes the
// raw row and its ingest job commit together or not at all.
type ambientTelegramEnqueuer struct{}

func (ambientTelegramEnqueuer) EnqueueTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) error {
	// Safely, not the panicking form: this runs inside a transaction whose
	// rollback is the recovery path, and a panic there would take the worker down
	// instead of returning the batch for redelivery.
	client, err := river.ClientFromContextSafely[pgx.Tx](ctx)
	if err != nil {
		return fmt.Errorf("telegram_poll: no River client on the job context: %w", err)
	}
	if _, err := client.InsertTx(ctx, tx, args, opts); err != nil {
		return fmt.Errorf("telegram_poll: enqueueing %s: %w", args.Kind(), err)
	}
	return nil
}

// telegramPollWorker runs one connection's long poll and commits whatever came
// back. The provider seam is an interface so the suites drive a fake rather than
// the live Bot API, and inserter is the narrow enqueue slice (telegramEnqueuer)
// so a test can fail the enqueue INSIDE the real transaction.
type telegramPollWorker struct {
	river.WorkerDefaults[TelegramPollArgs]
	pool     *pgxpool.Pool
	vault    keyvault.Vault
	api      telegram.API
	inserter telegramEnqueuer
	log      *slog.Logger
}

func newTelegramPollWorker(pool *pgxpool.Pool, vault keyvault.Vault, api telegram.API, inserter telegramEnqueuer, log *slog.Logger) *telegramPollWorker {
	return &telegramPollWorker{pool: pool, vault: vault, api: api, inserter: inserter, log: log}
}

// Timeout overrides River's one-minute default so a long poll is never cancelled
// by the job that is running it. See telegramPollJobTimeout.
func (*telegramPollWorker) Timeout(*river.Job[TelegramPollArgs]) time.Duration {
	return telegramPollJobTimeout
}

// Work resolves the connection, long-polls it, and persists what came back.
//
// Every identity the batch is keyed on comes from the row read here, not from
// the args: ReplaceToken re-points a live connection at a different bot in place,
// so the bot a poll actually spoke to is a fact about THIS poll and must be read
// with the token it used.
func (w *telegramPollWorker) Work(ctx context.Context, job *river.Job[TelegramPollArgs]) error {
	connID, err := ids.Parse(job.Args.ConnectionID)
	if err != nil {
		return fmt.Errorf("telegram_poll: connection id: %w", err)
	}
	wsCtx, err := workspaceJobCtx(ctx, job.Args)
	if err != nil {
		return err
	}

	target, err := capture.LoadChannelPollTarget(wsCtx, w.pool, capture.ProviderTelegram, connID)
	if errors.Is(err, apperrors.ErrNotFound) {
		// Disconnected between the dispatcher's scan and this job — the ordinary
		// race, and there is nothing left to poll.
		return nil
	}
	if err != nil {
		return err
	}

	token, err := w.vault.Get(wsCtx, ids.From[ids.WorkspaceKind](job.Args.Workspace), target.CredentialRef)
	if err != nil {
		return fmt.Errorf("telegram_poll: unsealing the bot token for connection %s: %w", connID, err)
	}

	batch, highest, err := w.api.GetUpdates(ctx, string(token),
		target.PollOffset, telegramPollTimeoutSeconds, telegram.AllowedUpdates())
	if err != nil {
		return w.answerPollFailure(wsCtx, target, string(token), err)
	}
	if len(batch) == 0 {
		// The long poll timed out with nothing to report — the ordinary outcome.
		// The cursor does not move: an empty batch acknowledges nothing.
		return nil
	}
	// A NON-empty batch goes through persist even when nothing in it could be
	// numbered, so every refusal is logged. Returning early on highest == 0 would
	// drop such a batch silently, and it would arrive again on every poll —
	// unacknowledgeable and invisible.
	return w.persist(wsCtx, target, batch, highest)
}

// persist commits the batch and the new cursor in ONE transaction.
//
// This is the whole correctness story of a pull ingress. getUpdates(offset =
// highest + 1) IS the acknowledgement: asking for the next offset is what tells
// Telegram to forget everything below it, and Telegram has no history API to
// re-fetch a forgotten update from. So the cursor may only move in the
// transaction that made the batch durable. A crash before commit re-delivers the
// identical batch, which the per-bot (bot, update_id) natural key folds
// idempotently; a crash after commit loses nothing, because the next poll starts
// where this one ended.
//
// The refusals are classified BEFORE the transaction opens (classifyTelegramBatch)
// and logged here, so nothing a refused update names ever reaches a row.
func (w *telegramPollWorker) persist(wsCtx context.Context, target capture.ChannelPollTarget, batch []json.RawMessage, highest int64) error {
	keep, refused := classifyTelegramBatch(batch)
	for _, r := range refused {
		// This log line is the ONLY trace a refused update leaves, deliberately:
		// an update naming no subject the erasure and SAR lanes can reach must not
		// be stored, not even quarantined. It names the update's number and the
		// reason, never anything out of the payload.
		w.log.WarnContext(wsCtx, "telegram_poll: refused an update and advanced past it",
			"connection", target.ID.String(), "update_id", r.updateID, "because", r.because, "err", r.err)
	}

	return database.WithWorkspaceTx(wsCtx, w.pool, func(tx pgx.Tx) error {
		// FIRST statement of the transaction, for the whole batch at once. The
		// lock is the mutex between this ingest and an erasure of the same human,
		// and LockChannelIdentities takes its keys in a sorted, deduplicated order
		// precisely so the two sides cannot deadlock. Taking locks incrementally
		// as the loop below discovered accounts would take them in Telegram's
		// arrival order instead of a chosen one, which is the deadlock this
		// ordering exists to rule out.
		if err := storekit.LockChannelIdentities(wsCtx, tx, telegramLockKeys(polledBatchAccounts(keep))); err != nil {
			return err
		}
		for _, update := range keep {
			suppressed, err := telegramAnySubjectSuppressed(wsCtx, tx, update.accounts)
			if err != nil {
				return err
			}
			if suppressed {
				// An erased subject. Nothing is preserved for an operator to
				// inspect and the log names no account: an installation told to
				// stop profiling a human does not get to keep their words in a
				// quarantine table, nor their id in a log line.
				w.log.InfoContext(wsCtx, "telegram_poll: refused an update whose subject has been erased",
					"connection", target.ID.String(), "update_id", update.updateID)
				continue
			}
			rawID, err := capture.InsertRawCaptureTx(wsCtx, tx, capture.RawRecord{
				SourceSystem: capture.ProviderTelegram,
				SourceID:     telegramRawSourceID(target.ChannelID, update.updateID),
				Payload:      update.raw,
			})
			if err != nil {
				return err
			}
			if err := w.inserter.EnqueueTx(wsCtx, tx, TelegramIngestArgs{
				Workspace:    target.WorkspaceID,
				ConnectionID: target.ID.String(),
				BotID:        target.ChannelID,
				RawCaptureID: rawID.String(),
			}, &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true}}); err != nil {
				return err
			}
		}
		if highest == 0 {
			// Nothing in the batch carried a number, so there is nothing this side
			// could acknowledge — and inventing one would tell Telegram to forget an
			// update nobody read. The refusals above are logged; the batch will
			// arrive again until Telegram's own retention drops it.
			return nil
		}
		return capture.AdvanceChannelPollOffsetTx(wsCtx, tx, target.ID, target.ChannelID, highest+1)
	})
}

// answerPollFailure turns what Telegram said about a refused poll into the one
// disposition that matches it (design v2 §6). Each arm is a different remedy, and
// answering the wrong one either wedges a healthy bot or hides a broken one.
func (w *telegramPollWorker) answerPollFailure(wsCtx context.Context, target capture.ChannelPollTarget, token string, err error) error {
	var limited *connector.RateLimitedError
	switch {
	case errors.Is(err, telegram.ErrWebhookActive):
		return w.answerRivalConsumer(wsCtx, target, token, err)
	case errors.Is(err, telegram.ErrTokenRejected):
		// No retry repairs a token Telegram refuses — only an admin re-pasting
		// it. Parking the connection is what ends the retry loop, because the
		// due-scan selects only `connected` rows.
		w.log.WarnContext(wsCtx, "telegram_poll: Telegram rejected the sealed bot token; the connection needs a new one",
			"connection", target.ID.String())
		return w.stopPolling(wsCtx, target, capture.ChannelPollStoppedByBadToken,
			"Telegram rejected the sealed bot token")
	case errors.As(err, &limited) && limited.RetryAfter > 0:
		// Telegram named the interval it wants; a ladder of our own on top of it
		// earns a harder limit. The cursor has not moved, so waiting loses nothing.
		w.log.WarnContext(wsCtx, "telegram_poll: throttled by Telegram",
			"connection", target.ID.String(), "retry_after", limited.RetryAfter)
		return river.JobSnooze(limited.RetryAfter)
	default:
		// An outage or a transport fault: River's ladder owns the retry, and the
		// cursor has not moved, so nothing is lost by waiting for it.
		return fmt.Errorf("telegram_poll: polling connection %s: %w", target.ID, err)
	}
}

// answerRivalConsumer handles Telegram's 409 — something else holds this bot's
// updates. There are two causes and they need different answers, so this
// establishes WHICH rather than inferring it: clear the one cause this
// installation can clear (a registered webhook — pending updates deliberately
// KEPT, they are the customer's messages), then ask again with NO long-poll
// interval, so the second answer is a fact about a bot that provably carries no
// webhook.
//
// The re-ask uses the SAME offset, so it acknowledges nothing: whatever it comes
// back with is still held by Telegram and belongs to the next poll. That is what
// makes it safe to discard, and why the answer is only read for its refusal.
//
// River's attempt counter cannot stand in for this. Any earlier failure — an
// outage, a vault fault — increments it without a clear ever having happened, so a
// bot on its second attempt meeting a genuinely registered webhook would be parked
// under a cause that is not true, and recovered only by hand.
func (w *telegramPollWorker) answerRivalConsumer(wsCtx context.Context, target capture.ChannelPollTarget, token string, err error) error {
	if delErr := w.api.DeleteWebhook(wsCtx, token); delErr != nil {
		return fmt.Errorf("telegram_poll: connection %s is refused by Telegram and its webhook could not be cleared: %w",
			target.ID, errors.Join(err, delErr))
	}
	_, _, reasked := w.api.GetUpdates(wsCtx, token, target.PollOffset, 0, telegram.AllowedUpdates())
	if !errors.Is(reasked, telegram.ErrWebhookActive) {
		// The registration WAS the cause and it is gone. This job's long poll is
		// over either way, so the failure is returned for River's ladder rather
		// than the re-ask's answer being consumed here.
		w.log.WarnContext(wsCtx, "telegram_poll: cleared a webhook that was blocking this bot's polling; retrying",
			"connection", target.ID.String())
		return fmt.Errorf("telegram_poll: connection %s had a webhook registered, now cleared: %w", target.ID, err)
	}
	w.log.ErrorContext(wsCtx, "telegram_poll: another consumer holds this bot's updates even with no webhook registered; polling stopped",
		"connection", target.ID.String())
	return w.stopPolling(wsCtx, target, capture.ChannelPollStoppedByRivalConsumer,
		"another consumer is holding this bot's updates; getUpdates was still refused with no webhook registered")
}

// stopPolling parks a connection under the connector's own principal: the poller
// made this change on its own initiative, so attributing it to the admin who ran
// Connect would put a decision in their audit trail that they did not make.
func (w *telegramPollWorker) stopPolling(wsCtx context.Context, target capture.ChannelPollTarget, status, reason string) error {
	actorCtx := principal.WithCorrelationID(
		principal.WithActor(wsCtx, telegramChannelPrincipal()), ids.NewV7())
	return capture.StopPollingChannelConnection(actorCtx, w.pool, target.ID, status, reason)
}
