// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The passes that exist only because this deployment wired what they need.
//
// Every job here sits behind a config test, and the nil IS the wiring rather
// than a degraded state: an installation that bought no model runs no classify
// pass, one with no Gmail registry polls no mailbox, one with no vault
// reconciles no incumbent mirror. They live apart from NewJobRunner's
// unconditional registrations because the two answer different questions —
// what this product does every hour, and what this operator turned on.

import (
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
)

// addConfiguredJobs registers every config-gated worker and returns their
// periodic entries. It appends nothing for an option this deployment left
// unset, which is how an unconfigured installation runs exactly the passes it
// can actually serve.
func addConfiguredJobs(
	workers *river.Workers, pool *pgxpool.Pool, cfg JobRunnerConfig, log *slog.Logger,
) []*river.PeriodicJob {
	var periodic []*river.PeriodicJob
	periodic = append(periodic, addModelCaptureJobs(workers, pool, cfg, log)...)
	periodic = append(periodic, addMailboxJobs(workers, pool, cfg, log)...)
	periodic = append(periodic, addMirrorJobs(workers, pool, cfg, log)...)
	return periodic
}

// addModelCaptureJobs registers the capture passes that need a model lane: classify,
// enrich, the name promotion the enrich arc feeds, and the counterparty
// verdict. An installation that bought no model runs none of them and still
// captures mail.
func addModelCaptureJobs(
	workers *river.Workers, pool *pgxpool.Pool, cfg JobRunnerConfig, log *slog.Logger,
) []*river.PeriodicJob {
	var periodic []*river.PeriodicJob
	if cfg.ClassifyBrain != nil {
		river.AddWorker(workers, &captureClassifyWorker{pool: pool})
		river.AddWorker(workers, &captureClassifyWorkspaceWorker{
			classifier: NewCaptureClassifier(pool, cfg.ClassifyBrain, log),
		})
		// The hourly catch-up pass (ADR-0063): the nightly suite reruns the
		// same engine; the backlog index makes an empty pass one cheap probe.
		periodic = append(periodic, river.NewPeriodicJob(
			river.PeriodicInterval(time.Hour),
			func() (river.JobArgs, *river.InsertOpts) { return CaptureClassifyArgs{}, sweepInsertOpts() },
			&river.PeriodicJobOpts{RunOnStart: true},
		))
	}

	if cfg.EnrichBrain != nil {
		river.AddWorker(workers, &captureEnrichWorker{pool: pool})
		river.AddWorker(workers, &captureEnrichWorkspaceWorker{
			enricher: NewCaptureEnricher(pool, cfg.EnrichBrain, log),
		})
		// Daily (the ADR-0063 nightly cadence rides the same job until the
		// nightly dispatcher lands); run-on-start clears any backlog early.
		periodic = append(periodic, river.NewPeriodicJob(
			river.PeriodicInterval(24*time.Hour),
			func() (river.JobArgs, *river.InsertOpts) { return CaptureEnrichArgs{}, sweepInsertOpts() },
			&river.PeriodicJobOpts{RunOnStart: true},
		))
	}

	{
		// Registered unconditionally: the promotion weighs evidence rows the
		// enrich pass already wrote, so it needs no model. Gating it on a brain
		// would leave an AI-less deployment unable to act on signatures it had
		// already collected.
		river.AddWorker(workers, &orgNamePromotionWorker{pool: pool})
		river.AddWorker(workers, &orgNamePromotionWorkspaceWorker{promoter: NewOrgNamePromoter(pool, log)})
		// Daily, after the enrich pass has had a night to collect signatures;
		// run-on-start so a deployment with a backlog acts on it immediately.
		periodic = append(periodic, river.NewPeriodicJob(
			river.PeriodicInterval(24*time.Hour),
			func() (river.JobArgs, *river.InsertOpts) { return OrgNamePromotionArgs{}, sweepInsertOpts() },
			&river.PeriodicJobOpts{RunOnStart: true},
		))
	}

	{
		// Registered unconditionally: only the JUDGING stage needs a model, and
		// the worker skips it when none is configured. Gating the whole worker on
		// a brain would mean an AI-less deployment never staged a review for an
		// existing unsure row and never redacted mail it had already hidden.
		verdicts := NewCounterpartyVerdictEngine(pool, cfg.VerdictBrain, log)
		river.AddWorker(workers, &counterpartyVerdictWorker{pool: pool})
		river.AddWorker(workers, &counterpartyVerdictWorkspaceWorker{engine: verdicts})
		// Hourly, like classify: the ledger's due-index makes an empty pass one
		// cheap probe, and a deferred sender should not wait a day to become a
		// record. Each workspace's job runs every stage in dependency order —
		// judging fills the unsure backlog that staging then offers, and
		// redaction only ever acts on windows that closed before this tick.
		periodic = append(periodic, river.NewPeriodicJob(
			river.PeriodicInterval(time.Hour),
			func() (river.JobArgs, *river.InsertOpts) { return CounterpartyVerdictArgs{}, sweepInsertOpts() },
			&river.PeriodicJobOpts{RunOnStart: true},
		))
	}

	// Telegram ingress PULLS (telegrampoll.go), and a poll needs the vault that
	// holds the bot's sealed token — so a role without one registers neither half.
	periodic = registerTelegramPoll(workers, periodic, pool, cfg.ChannelVault, cfg.ChannelAPI, log)

	return periodic
}

// addMailboxJobs registers the Gmail poll and its watch renewal. The registry is the
// wiring — no registry, no mailbox to poll.
func addMailboxJobs(
	workers *river.Workers, pool *pgxpool.Pool, cfg JobRunnerConfig, log *slog.Logger,
) []*river.PeriodicJob {
	var periodic []*river.PeriodicJob
	if cfg.GmailRegistry != nil {
		digests := &captureDigestWorker{registry: cfg.GmailRegistry, pool: pool, log: log}
		river.AddWorker(workers, digests)
		river.AddWorker(workers, &captureDigestWorkspaceWorker{digests: digests})
		// The digest builds daily after the overnight passes; run-on-start
		// backfills a missed night so mornings are never silently empty.
		periodic = append(periodic, river.NewPeriodicJob(
			river.PeriodicInterval(24*time.Hour),
			func() (river.JobArgs, *river.InsertOpts) { return CaptureDigestArgs{}, sweepInsertOpts() },
			&river.PeriodicJobOpts{RunOnStart: true},
		))
		river.AddWorker(workers, &gmailSyncWorker{registry: cfg.GmailRegistry, log: log})
		river.AddWorker(workers, &captureSyncWorker{registry: cfg.GmailRegistry, log: log})
		// Backfill jobs are enqueued by the api (start op); the worker role
		// only needs the pager registered.
		river.AddWorker(workers, &captureBackfillWorker{registry: cfg.GmailRegistry, log: log})
		// The dispatcher tick is a cheap indexed due-scan; per-connection
		// pacing lives in the sidecar (next_sync_at = success + interval),
		// so a frequent scan does not mean frequent provider calls. It scans
		// every registered connector, so a Google Calendar connection (the same
		// Google OAuth app) syncs on the identical per-connection path a mailbox
		// does — no gcal-specific job.
		periodic = append(periodic, river.NewPeriodicJob(
			river.PeriodicInterval(dispatchScanInterval),
			func() (river.JobArgs, *river.InsertOpts) { return GmailSyncArgs{}, sweepInsertOpts() },
			&river.PeriodicJobOpts{RunOnStart: true},
		))
		if cfg.GmailWatch.Topic != "" {
			river.AddWorker(workers, &gmailWatchWorker{
				registry: cfg.GmailRegistry, renewWithin: cfg.GmailWatch.RenewWithin, log: log,
			})
			river.AddWorker(workers, &gmailWatchRenewWorker{
				registry: cfg.GmailRegistry, topic: cfg.GmailWatch.Topic,
			})
			periodic = append(periodic, river.NewPeriodicJob(
				river.PeriodicInterval(cfg.GmailWatch.Interval),
				func() (river.JobArgs, *river.InsertOpts) { return GmailWatchArgs{}, sweepInsertOpts() },
				&river.PeriodicJobOpts{RunOnStart: true},
			))
		}
	}

	return periodic
}

// addMirrorJobs registers the incumbent-mirror reconcile. The vault is the wiring:
// without a credential custodian there is no sealed token to resolve a
// connection through, so the poller stays off by omission.
func addMirrorJobs(
	workers *river.Workers, pool *pgxpool.Pool, cfg JobRunnerConfig, log *slog.Logger,
) []*river.PeriodicJob {
	var periodic []*river.PeriodicJob
	if cfg.OverlayVault != nil {
		ms := overlay.NewMirrorStore(pool, unresolvedOwnerEmails{})
		// cmd/worker built the meter over the shared Redis (so the poller's
		// spend and the api's force-fresh spend land on ONE count); fall back
		// to a fail-closed meter if a role wired the poller without one.
		meter := cfg.OverlayMeter
		if meter == nil {
			meter = failClosedOverlayMeter()
		}
		river.AddWorker(workers, &overlayReconcileWorker{pool: pool, log: log})
		river.AddWorker(workers, &overlayReconcileWorkspaceWorker{
			pool: pool, vault: cfg.OverlayVault, ms: ms, meter: meter, log: log,
			newIncumbent: overlayIncumbentFactory(cfg.OverlayBackfillLimit),
		})
		// The webhook-as-signal re-fetch worker (OVA-WIRE-10): consumes the
		// coalesced OverlayRefetchArgs the receiver enqueues, refreshing one
		// record through the same store the poller uses. Registered whenever
		// the overlay vault is present (the receiver only enqueues when the api
		// role has the app secret wired).
		river.AddWorker(workers, &overlayRefetchWorker{pool: pool, vault: cfg.OverlayVault, ms: ms, meter: meter, log: log, newIncumbent: overlayIncumbentFactory(cfg.OverlayBackfillLimit)})
		periodic = append(periodic, river.NewPeriodicJob(
			river.PeriodicInterval(cfg.OverlayInterval),
			func() (river.JobArgs, *river.InsertOpts) { return OverlayReconcileArgs{}, sweepInsertOpts() },
			&river.PeriodicJobOpts{RunOnStart: true},
		))
	}
	return periodic
}
