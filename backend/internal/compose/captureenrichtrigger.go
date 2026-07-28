// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Enrich-on-capture (ADR-0072/A118 §9, the trigger half): the moment a captured
// mail mints a NEW company, its web dossier is queued — rather than waiting for
// the next daily sweep to notice it.
//
// The sweep is unchanged and still the reconciler. This trigger is
// at-least-once in the other direction: it may miss (no ambient River client,
// the day's cap already spent, a fault), and every miss is simply an
// organization the next sweep finds due. That is the whole reason it is allowed
// to be best-effort — there is a pass whose job is to be right, so this one only
// has to be quick.
//
// What it does NOT do is crawl. It reserves a budget slot, writes the dossier
// row, queues the job and arms the cursor — a handful of statements. The pages
// and the model call happen in the deep-read worker, on its own job, so the
// capture that caused it never waits for a website to answer.

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// autoEnrichTrigger queues the dossier for a company capture has just created.
type autoEnrichTrigger struct {
	people     *people.Store
	settings   *capture.SettingsStore
	autoEnrich *capture.AutoEnrichStore
	dailyCap   int
	log        *slog.Logger
}

func newAutoEnrichTrigger(pool *pgxpool.Pool, log *slog.Logger) *autoEnrichTrigger {
	return &autoEnrichTrigger{
		people:     people.NewStore(pool),
		settings:   capture.NewSettings(pool),
		autoEnrich: capture.NewAutoEnrichStore(pool),
		dailyCap:   autoEnrichDailyCap,
		log:        log,
	}
}

// organizationCaptured queues the read for a freshly created company.
//
// It never returns an error, and that is the contract rather than laziness: it
// is called from the capture pipeline's post-commit step, which must never fail
// a capture. The message and its records are already committed by the time this
// runs; the worst outcome is a company whose dossier waits for the sweep.
func (t *autoEnrichTrigger) organizationCaptured(ctx context.Context, orgID ids.OrganizationID, domain string) {
	// Nothing to read without a domain. Whether this process can enqueue at all
	// is answered later, by startAutoEnrichRead — asking it up front would save
	// three round trips per captured company and cost the ability to observe the
	// trigger in any test, since no test process binds a River client. Every
	// production capture runs inside a River job, so the early exit would save
	// nothing where it matters.
	if domain == "" {
		return
	}
	if err := t.queueRead(ctx, orgID, domain); err != nil {
		// One place to report from — but it deliberately does NOT promise the
		// sweep will take this organization. Most of these faults leave nothing
		// queued, and the sweep does take it; a MarkQueued that fails after the
		// read started leaves a live site_read, which ListDueOrgs excludes. An
		// operator told "the sweep has it" would stop looking in exactly the case
		// where nothing is coming.
		t.log.WarnContext(ctx, "capture auto-enrich: on-capture trigger failed",
			"org", orgID.String(), "err", err)
	}
}

// queueRead runs the gates that cost a query and queues the read past them.
//
// A nil return is not "it queued": the setting being off and the day's cap being
// spent are ordinary answers, not faults, and they are what the sweep exists to
// pick up. Only a genuine failure comes back as an error.
func (t *autoEnrichTrigger) queueRead(ctx context.Context, orgID ids.OrganizationID, domain string) error {
	// The read is the system's, not the connector's: attributed to
	// `system:capture_auto_enrich` exactly as the sweep's is, so the dossier's
	// provenance says what caused it rather than who happened to be syncing. The
	// correlation id is deliberately NOT re-minted: keeping the capture's ties
	// this dossier back to the message that asked for it.
	enrichCtx := principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalSystem, ID: systemAutoEnrichActor,
	})
	settings, err := t.settings.Get(enrichCtx)
	if err != nil {
		return err
	}
	if !settings.AutoEnrich {
		return nil
	}
	// The same atomically-reserved daily cap the sweep spends from — one budget,
	// whichever path spends it, or the trigger would be a way around the ADR-0020
	// guardrail rather than a faster route through it.
	slot, err := t.autoEnrich.ReserveBudget(enrichCtx, t.dailyCap)
	if err != nil {
		return err
	}
	if !slot.Reserved {
		// Debug, not warn: on a backfill big enough to exhaust the day this is the
		// NORMAL state for every company after the cap, and a warning apiece
		// would bury the faults that matter.
		t.log.DebugContext(ctx, "capture auto-enrich: daily cap reached, the sweep takes this org",
			"org", orgID.String())
		return nil
	}
	// One rule for a reserved slot, shared with the sweep: it buys a read or it
	// goes back, and the refund survives the cancellation that may have caused
	// the failure it compensates for.
	return startEnrichOrRefund(enrichCtx, t.people, t.autoEnrich, orgID, domain, slot)
}
