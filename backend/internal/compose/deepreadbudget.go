// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func (w *siteDeepReadWorker) Work(ctx context.Context, job *river.Job[SiteDeepReadArgs]) (workErr error) {
	// River recovers a worker panic, but it cannot close this module's
	// claimed dossier. Recover at the ownership boundary so an unexpected
	// provider/parser panic becomes a terminal failed read instead of a row
	// that tells the browser "reading" forever.
	workCtx := deepReadWorkerCtx(ctx, job.Args)
	defer func() {
		if recovered := recover(); recovered != nil {
			cause := fmt.Errorf("site deep read panic: %v", recovered)
			if w.log != nil {
				w.log.ErrorContext(workCtx, "site deep read panic recovered",
					"read", job.Args.SiteReadID.String(), "panic", recovered, "stack", string(debug.Stack()))
			}
			workErr = w.fail(workCtx, job.Args.SiteReadID, cause)
		}
	}()
	err := w.run(workCtx, job.Args)
	var deferral *ai.BudgetDeferralError
	if !errors.As(err, &deferral) {
		return err
	}
	now := time.Now()
	if w.now != nil {
		now = w.now()
	}
	delay := deferral.NextAttemptAt.Sub(now)
	if delay < 0 {
		delay = 0
	}
	return river.JobSnooze(delay)
}

func (w *siteDeepReadWorker) deferForBudget(ctx context.Context, readID ids.UUID, cause error) (bool, error) {
	var deferral *ai.BudgetDeferralError
	if !errors.As(cause, &deferral) {
		return false, nil
	}
	tctx, cancel := terminalCtx(ctx)
	defer cancel()
	if err := w.people.DeferSiteRead(tctx, readID, deferral.NextAttemptAt); err != nil {
		return true, errors.Join(cause, fmt.Errorf("recording budget deferral on the dossier: %w", err))
	}
	return true, cause
}
