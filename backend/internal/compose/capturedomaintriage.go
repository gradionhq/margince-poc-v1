// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Triage-on-capture (the trigger half): the moment a captured mail meets a
// domain nobody has judged, its site read is queued — rather than waiting for
// the next daily sweep to notice the open question.
//
// The shape is the auto-enrich trigger's, and for the same reasons: it is
// best-effort in the enqueue direction because the sweep is the reconciler, and
// it does no crawling itself. The difference is what a miss costs. A missed
// enrich leaves a company without a dossier; a missed triage leaves a PERSON
// whose company nobody has decided on, so the sweep matters more here, not less.

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// domainTriageTrigger queues the read that decides whether a domain capture
// just met deserves a company.
type domainTriageTrigger struct {
	people     *people.Store
	settings   *capture.SettingsStore
	autoEnrich *capture.AutoEnrichStore
	dailyCap   int
	log        *slog.Logger
}

func newDomainTriageTrigger(pool *pgxpool.Pool, log *slog.Logger) *domainTriageTrigger {
	return &domainTriageTrigger{
		people:     people.NewStore(pool),
		settings:   capture.NewSettings(pool),
		autoEnrich: capture.NewAutoEnrichStore(pool),
		dailyCap:   autoEnrichDailyCap,
		log:        log,
	}
}

// domainPending queues the triage read for a domain whose organization question
// the ensure ladder just opened.
//
// It never returns an error, and that is the contract rather than laziness: it
// is called from the capture pipeline's post-commit step, which must never fail
// a capture. The message and the person are already committed by the time this
// runs; the worst outcome is a domain whose verdict waits for the sweep.
func (t *domainTriageTrigger) domainPending(ctx context.Context, domain string) {
	if domain == "" {
		return
	}
	if err := t.queueTriage(ctx, domain); err != nil {
		// Reported without promising the sweep will take it: a MarkTriageQueued
		// that fails after the read started leaves a live dossier, which
		// ListDueDomains excludes, and an operator told "the sweep has it" would
		// stop looking in exactly that case.
		t.log.WarnContext(ctx, "domain triage: on-capture trigger failed",
			"domain", domain, "err", err)
	}
}

// queueTriage runs the gates that cost a query and queues the read past them.
//
// A nil return is not "it queued": the setting being off and the day's cap being
// spent are ordinary answers, not faults, and both leave the question for the
// sweep. Only a genuine failure comes back as an error.
func (t *domainTriageTrigger) queueTriage(ctx context.Context, domain string) error {
	triageCtx := principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalSystem, ID: systemDomainTriageActor,
	})
	settings, err := t.settings.Get(triageCtx)
	if err != nil {
		return err
	}
	if !settings.AutoEnrich {
		// The operator's "don't crawl" is honoured, and the question is still
		// closed: the worker's no-model path settles it from what the workspace
		// already knows. Queue it anyway rather than leaving the domain pending
		// forever — an installation with the setting off would otherwise never
		// create a company for anyone again.
		return t.startOrRefund(triageCtx, domain, capture.BudgetSlot{})
	}
	// The same atomically-reserved daily cap the enrich sweep spends from — one
	// budget, whichever path spends it, or triage would be a way around the
	// ADR-0020 guardrail rather than another claimant on it.
	slot, err := t.autoEnrich.ReserveBudget(triageCtx, t.dailyCap)
	if err != nil {
		return err
	}
	if !slot.Reserved {
		// Debug, not warn: on a backfill big enough to exhaust the day this is
		// the NORMAL state for every domain after the cap, and a warning apiece
		// would bury the faults that matter.
		t.log.DebugContext(ctx, "domain triage: daily cap reached, the sweep takes this domain",
			"domain", domain)
		return nil
	}
	return t.startOrRefund(triageCtx, domain, slot)
}

// startOrRefund queues one triage read, returning the budget slot when nothing
// was started. A zero slot is the setting-off path, which reserved nothing and
// therefore has nothing to give back.
func (t *domainTriageTrigger) startOrRefund(ctx context.Context, domain string, slot capture.BudgetSlot) error {
	started, err := startDomainTriageRead(ctx, t.people, domain)
	if started || !slot.Reserved {
		return err
	}
	refundCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refundTimeout)
	defer cancel()
	refundErr := t.autoEnrich.ReleaseBudget(refundCtx, slot)
	if err != nil {
		return errors.Join(err, refundErr)
	}
	return refundErr
}

// startDomainTriageRead queues ONE triage read for one domain and arms its retry
// cursor. Shared by the trigger and the sweep, because they differ only in what
// made the domain interesting — the read they ask for, the ceiling it runs
// under, the principal it is attributed to, and the cursor that stops it being
// asked for twice must all be the one spelling.
//
// The dossier and the River job are one transaction, so a crash can never leave
// a dossier without its job; the in-flight uniqueness index dedupes a concurrent
// start, and since the seed url is derived from the domain, that index is
// per-domain uniqueness.
//
// It reports whether a read was actually STARTED, as opposed to joined onto one
// already in flight. A join means the caller's budget slot bought nothing, and
// arming the cursor again would charge a second attempt for one crawl.
func startDomainTriageRead(ctx context.Context, peopleStore *people.Store, domain string) (bool, error) {
	client, err := river.ClientFromContextSafely[pgx.Tx](ctx)
	if err != nil {
		return false, err
	}
	_, joined, err := peopleStore.StartDomainTriageSiteRead(ctx, domain, systemDomainTriageActor,
		func(ctx context.Context, tx pgx.Tx, read people.SiteRead) error {
			_, insErr := client.InsertTx(ctx, tx, SiteDeepReadArgs{
				Workspace:   storekit.MustWorkspace(ctx),
				SiteReadID:  read.ID,
				RequestedBy: read.RequestedBy,
				// Declared in the payload as well as enforced at the worker: a
				// job carries what it was queued to cost, so an operator
				// reading river_job sees the ceiling without inferring it.
				MaxPages: autoEnrichMaxPages,
			}, siteDeepReadInsertOpts())
			return insErr
		})
	if err != nil {
		return false, err
	}
	if joined {
		return false, nil
	}
	return true, peopleStore.MarkTriageQueued(ctx, domain)
}

// triageSweepPageSize bounds one sweep pass over open domain questions. The
// same order of magnitude as the org sweep's page: a workspace that met a
// hundred new domains in a day gets them all inside a few passes, and one pass
// never holds a long transaction.
const triageSweepPageSize = 50

// sweepDomainTriage is the reconciler half: every domain still owed a verdict
// whose retry is due gets its read queued, under the same daily cap. It is what
// makes the trigger allowed to be best-effort.
func (w *captureAutoEnrichSweepWorker) sweepDomainTriage(ctx context.Context, dailyCap int) error {
	// First, close the questions no crawl will ever answer. A domain that used
	// every attempt drops out of the due scan below, so leaving it pending
	// would strand its people without a company and without a reason on the
	// row. It is settled from what the workspace already knows, exactly as a
	// worker forbidden to look would settle it.
	exhausted, err := w.people.ExhaustedDomains(ctx, triageSweepPageSize)
	if err != nil {
		return err
	}
	for _, domain := range exhausted {
		if _, err := w.people.ResolveUnreadableDomainTriage(ctx, people.ResolveDomainTriageInput{
			Domain:  domain.Domain,
			SeedURL: people.TriageSeedURL(domain.Domain),
			Evidence: "every attempt to read this site failed, so the question was answered " +
				"from what the workspace already knew",
		}); err != nil {
			w.log.WarnContext(ctx, "domain triage: could not settle an exhausted domain",
				"domain", domain.Domain, "err", err)
		}
	}

	due, err := w.people.ListDueDomains(ctx, triageSweepPageSize)
	if err != nil {
		return err
	}
	for _, domain := range due {
		slot, err := w.autoEnrich.ReserveBudget(ctx, dailyCap)
		if err != nil {
			return err
		}
		if !slot.Reserved {
			// The day is spent. Every remaining domain keeps its due date and
			// the next pass takes it.
			return nil
		}
		// A read this sweep can see as due is either absent or STALE — the due
		// query only admits a domain whose dossier stopped being believed. A
		// stale one has to be retired before the start, or the start joins the
		// very row that is stuck and refunds, and the domain is offered again
		// on every pass for ever without anything moving.
		if err := w.people.RetireStaleTriageRead(ctx, domain.Domain); err != nil {
			w.log.WarnContext(ctx, "domain triage: could not retire a stale dossier",
				"domain", domain.Domain, "err", err)
		}
		started, startErr := startDomainTriageRead(ctx, w.people, domain.Domain)
		if !started {
			refundCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refundTimeout)
			releaseErr := w.autoEnrich.ReleaseBudget(refundCtx, slot)
			cancel()
			startErr = errors.Join(startErr, releaseErr)
		}
		if startErr != nil {
			// One domain's failure must not stop the pass: the others are
			// independent questions and the next pass retries this one.
			w.log.WarnContext(ctx, "domain triage: the sweep could not queue a read",
				"domain", domain.Domain, "err", startErr)
		}
	}
	return nil
}
