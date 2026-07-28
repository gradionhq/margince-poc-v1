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
// runs; the worst outcome here is a company whose dossier waits for the sweep.
// Every give-up says so in the log, because a trigger that silently declines is
// indistinguishable from one that ran.
func (t *autoEnrichTrigger) organizationCaptured(ctx context.Context, orgID ids.OrganizationID, domain string) {
	if domain == "" {
		return
	}
	// The read is the system's, not the connector's: it is attributed to
	// `system:capture_auto_enrich` exactly as the sweep's is, so the dossier's
	// provenance says what caused it rather than who happened to be syncing.
	// The correlation id is deliberately NOT re-minted — keeping the capture's
	// ties this dossier to the message that asked for it.
	enrichCtx := principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalSystem, ID: systemAutoEnrichActor,
	})

	settings, err := t.settings.Get(enrichCtx)
	if err != nil {
		t.log.WarnContext(ctx, "capture auto-enrich: reading the setting failed, leaving the org to the sweep",
			"org", orgID.String(), "err", err)
		return
	}
	if !settings.AutoEnrich {
		return
	}
	// The same atomically-reserved daily cap the sweep spends from — one budget,
	// whichever path spends it, or the trigger would be a way around the
	// ADR-0020 guardrail rather than a faster route through it.
	reserved, err := t.autoEnrich.ReserveBudget(enrichCtx, t.dailyCap)
	if err != nil {
		t.log.WarnContext(ctx, "capture auto-enrich: reserving the daily budget failed, leaving the org to the sweep",
			"org", orgID.String(), "err", err)
		return
	}
	if !reserved {
		// The day's reads are spent. Said at debug, not warn: on a backfill that
		// mints hundreds of companies this is the NORMAL state after the first
		// few, and a warning per company would bury the faults that matter.
		t.log.DebugContext(ctx, "capture auto-enrich: daily cap reached, the sweep takes this org",
			"org", orgID.String())
		return
	}
	if err := startAutoEnrichRead(enrichCtx, t.people, t.autoEnrich, orgID, domain); err != nil {
		// The reserved slot is spent without a read to show for it — a
		// conservative under-spend, and the org stays due for the sweep.
		t.log.WarnContext(ctx, "capture auto-enrich: on-capture trigger failed, leaving the org to the sweep",
			"org", orgID.String(), "err", err)
	}
}
