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
	return cause
}
