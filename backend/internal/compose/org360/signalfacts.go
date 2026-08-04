// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

// What the account's open signals say, read once for the three surfaces that
// ask: the strip states the worst, the health section counts the commitments,
// and the contradiction rule asks whether the contract ended. Three reads
// would let them describe three different instants of the same account.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/signals"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// signalFacts is what the open signals say about the account. Readable is
// false when the caller may not read signals at all, which is why the counts
// are not enough on their own: zero commitments and no permission to see them
// are different answers, and the page must not present one as the other.
type signalFacts struct {
	Readable        bool
	OpenCommitments int
	ContractEnded   bool
	// Worst is the most serious signal standing open, or absent when nothing
	// is. Severity first, then the newest of that severity — an older warning
	// that has been sitting there is less news than the one that just arrived.
	Worst    signalHeadline
	HasWorst bool
}

// signalHeadline is one signal as the strip states it.
type signalHeadline struct {
	Kind     string
	Severity string
	Summary  string
}

// readSignalFacts counts the things one side said they would do and nobody has
// closed — open `commitment_made` signals on this account.
//
// It reports counted=false rather than zero when the caller cannot read
// signals, following pendingApprovals' shape in this package: zero would say
// the account owes nothing, which is a claim about the account rather than
// about what this reader was allowed to see.
func readSignalFacts(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID) (signalFacts, error) {
	allowed, err := granted(ctx, "signal")
	if err != nil {
		return signalFacts{}, err
	}
	if !allowed {
		return signalFacts{}, nil
	}
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	orgPos := arg(orgID)
	// The SUBJECT row scope, not the account's. A signal's subject can be a
	// person or a deal the caller may not see, resolved onto an account they
	// can — and every figure below is drawn from the signal's own text. The
	// canonical org predicate comes from the signals module for the same
	// reason: a second spelling of "belongs to this account" would decide
	// visibility differently from the list this page links to.
	scope, err := auth.SignalScopeClause(ctx, "s", arg)
	if err != nil {
		return signalFacts{}, err
	}
	if scope == "" {
		scope = scopeAll
	}
	facts := signalFacts{Readable: true}
	var kind, severity, summary *string
	// One read serves three readers: the strip states the worst, the health
	// section counts the commitments, and the contradiction rule asks whether
	// the contract ended. Three queries would let them describe three instants.
	if err := tx.QueryRow(ctx, fmt.Sprintf(`
		WITH open_signals AS (
			SELECT s.id, s.kind, s.severity, s.summary, s.detected_at FROM signal s
			 WHERE %[1]s AND s.status = 'open' AND s.archived_at IS NULL AND %[2]s
		)
		SELECT (SELECT count(*) FROM open_signals WHERE kind = 'commitment_made'),
		       EXISTS (SELECT 1 FROM open_signals WHERE kind = 'contract_ended'),
		       worst.kind, worst.severity, worst.summary
		  FROM (SELECT 1) one
		  LEFT JOIN LATERAL (
			SELECT kind, severity, summary FROM open_signals
			 ORDER BY CASE severity WHEN 'urgent' THEN 0 WHEN 'warn' THEN 1 ELSE 2 END,
			          detected_at DESC, id DESC
			 LIMIT 1) worst ON true`,
		signals.OfOrganizationWhere(orgPos), scope), args...).
		Scan(&facts.OpenCommitments, &facts.ContractEnded,
			&kind, &severity, &summary); err != nil {
		return signalFacts{}, fmt.Errorf("read the account's open signals: %w", err)
	}
	if kind != nil && severity != nil && summary != nil {
		facts.Worst = signalHeadline{Kind: *kind, Severity: *severity, Summary: *summary}
		facts.HasWorst = true
	}
	return facts, nil
}
