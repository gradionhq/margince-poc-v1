// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// How a deep read ends when it does not produce a dossier, and the one question
// that decides whether an automatic read should run at all. Both are about
// stopping rather than reading, which is why they sit apart from the worker's
// crawl-and-extract path.

import (
	"context"
	"errors"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// fail records the terminal failure on the dossier and returns the cause
// so River logs it on the job. A retry after a recorded failure is safe
// by construction — BeginSiteRead CAS-misses and the attempt no-ops.
// autoEnrichMaxPages is the page ceiling every AUTOMATIC read runs under
// (ADR-0072 §9). A read nobody asked for should cost a fraction of one somebody
// did: the setting is on by default and sweeps up to ten organizations a day
// per workspace, so the deployment-wide crawler budget is the wrong unit here.
const autoEnrichMaxPages = 12

// pageCeiling is the page cap for one run: the automatic lane's own ceiling,
// else whatever the job asked for. requestedBy comes from the CLAIMED dossier
// row, not the job payload — the row is what says this read was automatic, and
// a payload that disagreed would otherwise buy the wider budget. Both only narrow — withPageCeiling ignores a
// value that is not lower than the configured cap, so neither this nor a job
// payload can spend more than the operator allowed.
func (w *siteDeepReadWorker) pageCeiling(requestedBy string, askedFor int) int {
	if isSystemRead(requestedBy) {
		if askedFor > 0 && askedFor < autoEnrichMaxPages {
			return askedFor
		}
		return autoEnrichMaxPages
	}
	return askedFor
}

// autoEnrichEnabled re-reads the workspace's auto-enrich setting.
func (w *siteDeepReadWorker) autoEnrichEnabled(ctx context.Context) (bool, error) {
	settings, err := w.settings.Get(ctx)
	if err != nil {
		return false, err
	}
	return settings.AutoEnrich, nil
}

// abandon closes a read nobody wants any more. Distinct from fail: nothing went
// wrong, an operator withdrew the standing decision that queued it, and a
// failure is something to investigate while this is not.
func (w *siteDeepReadWorker) abandon(ctx context.Context, readID ids.UUID, reason string) error {
	tctx, cancel := terminalCtx(ctx)
	defer cancel()
	if err := w.people.FinishSiteRead(tctx, readID, people.FinishSiteReadInput{Status: "cancelled"}); err != nil {
		return fmt.Errorf("site deep read %s: recording the cancellation: %w", readID, err)
	}
	w.log.InfoContext(ctx, "site deep read cancelled before spending", "read", readID.String(), "reason", reason)
	return nil
}

func (w *siteDeepReadWorker) fail(ctx context.Context, readID ids.UUID, cause error) error {
	tctx, cancel := terminalCtx(ctx)
	defer cancel()
	if err := w.people.FinishSiteRead(tctx, readID, people.FinishSiteReadInput{Status: "failed"}); err != nil {
		return errors.Join(cause, fmt.Errorf("recording the failure on the dossier: %w", err))
	}
	// The dossier is terminal now, and a failed read is one no confirmation
	// accepts — so a mark this read stored before it failed is bytes nobody can
	// ever adopt.
	w.reclaimParkedLogo(tctx, readID)
	return cause
}
