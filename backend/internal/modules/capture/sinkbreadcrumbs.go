// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The OPERATOR's record of what a capture gate decided.
//
// Distinct from the member-facing trace beside it (sinktrace.go), and the two
// are not redundant: a breadcrumb says what the pipeline did and names a
// natural key, and is read by whoever is debugging an installation. A trace
// says whose message it was and is read by the member whose message it was.
// Neither can be derived from the other.

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// logBreadcrumbTx records one capture-gate decision on the caller's capture
// transaction. Every tier outcome a human might have to explain — a suppression,
// a T1 spare that overrode one — commits with the activity it is about, so a
// rolled-back capture never leaves a breadcrumb for a message that does not
// exist, and no gate has to borrow a second pool connection while holding one.
// extra carries breadcrumb-specific fields (at most one map; the variadic is
// there so the seven callers that need nothing beyond the reason stay unchanged).
// A key colliding with the three fixed fields is ignored — the fixed shape is
// what makes these rows queryable across actions.
func (s *Sink) logBreadcrumbTx(ctx context.Context, tx pgx.Tx, action string, rec connector.NormalizedRecord, reason string, extra ...map[string]any) error {
	detail := map[string]any{
		fieldReason:       reason,
		fieldSourceSystem: rec.NaturalKey.SourceSystem,
		fieldSourceID:     rec.NaturalKey.SourceID,
	}
	for _, m := range extra {
		for k, v := range m {
			if _, fixed := detail[k]; !fixed {
				detail[k] = v
			}
		}
	}
	_, err := storekit.LogSystem(ctx, tx, action, detail)
	if err != nil {
		return fmt.Errorf("capture: recording the %s breadcrumb: %w", action, err)
	}
	return nil
}

// logEnsureFault records an auto-create failure in system_log — the
// activity is already committed and stays; the nightly reconcile re-runs
// the resolver over link-less connector activities.
func (s *Sink) logEnsureFault(ctx context.Context, rec connector.NormalizedRecord, cause error) {
	detail := map[string]any{
		fieldReason:       "counterparty_ensure_failed",
		fieldSourceSystem: rec.NaturalKey.SourceSystem,
		"error":           cause.Error(),
	}
	// A Telegram private-chat natural key embeds the customer's account id.
	// This fault can be recorded after an erasure committed between capture and
	// the asynchronous ensure, so retaining the key here would recreate the
	// identifier the suppression gate just kept out of the domain rows.
	if rec.Counterparty.ChannelIdentity.Provider == "" {
		detail[fieldSourceID] = rec.NaturalKey.SourceID
	}
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		_, logErr := storekit.LogSystem(ctx, tx, "capture_ensure_fault", detail)
		return logErr
	})
	if err != nil {
		// The ledger itself failed — nothing left but the process log; the
		// nightly reconcile still finds the link-less activity.
		slog.ErrorContext(ctx, "capture: recording ensure fault", "err", err, "cause", cause)
	}
}
