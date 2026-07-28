// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The deferral CEILINGS (ADR-0072/A118): how many open questions a workspace,
// and any one sender domain inside it, may hold at once.
//
// They live apart from pending.go because they answer a different question from
// the rest of the ledger. Every other rule in this module is about one row's
// lifecycle; these two are about the SIZE of the work list, and the party who
// grows it is an outsider — anyone who can mail the connected mailbox from fresh
// addresses mints ledger rows, and every row is a promised model call.

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// PendingDeferralCap bounds how many open questions one workspace may hold at
// once. Every deferral is a promised model call, and the party who creates them
// is an OUTSIDER: anyone who can mail the connected mailbox from fresh addresses
// mints ledger rows, so without a ceiling a stranger sets the workspace's AI
// spend. At the cap capture stops queueing questions — the messages still land
// on the timeline, they simply go unjudged, which is the safe direction to fail:
// a backlog of unanswered questions is recoverable, junk records are not.
const PendingDeferralCap = 500

// PendingDeferralDomainCap bounds how many of those open questions may come from
// a SINGLE sender domain. Without it the workspace ceiling is a weapon rather
// than a guardrail: one throwaway domain fills all 500 slots and every genuine
// new sender afterwards goes unjudged. A domain at its share stops queueing;
// every other domain is unaffected.
const PendingDeferralDomainCap = 50

// Which ceiling turned a message away — recorded on the operator breadcrumb so
// "the queue is full" and "one domain is flooding it" are never the same event.
const (
	CapReasonWorkspace = "workspace_ceiling"
	CapReasonDomain    = "domain_ceiling"
)

// capRefusesNewQuestion reports whether the ceiling turns this message away. The
// ceiling applies to NEW questions only: a further message from an
// already-deferred sender joins the open question and adds nothing to the count.
// It answers WHICH ceiling refused, because the two mean different things to an
// operator: the workspace ceiling says the whole queue is full, the domain
// ceiling says one sender's domain is flooding it and everyone else still gets
// through. Empty string means nothing refused.
func capRefusesNewQuestion(ctx context.Context, tx pgx.Tx, email, domain string) (string, error) {
	open, err := hasOpenQuestion(ctx, tx, email)
	if err != nil || open {
		return "", err
	}
	full, err := atDeferralCap(ctx, tx)
	if err != nil {
		return "", err
	}
	if full {
		return CapReasonWorkspace, nil
	}
	flooded, err := atDomainDeferralCap(ctx, tx, domain)
	if err != nil {
		return "", err
	}
	if flooded {
		return CapReasonDomain, nil
	}
	return "", nil
}

// lockWorkspaceDeferrals serializes the count-then-insert of new deferrals
// within one workspace. Advisory and transaction-scoped, the same shape the
// last-active-admin guard uses: the ceiling is a count of rows nobody has
// inserted yet, which no row lock can express.
func lockWorkspaceDeferrals(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtext('margince:capture-deferrals:' || current_setting('app.workspace_id', true))::bigint)`); err != nil {
		return fmt.Errorf("capture: serializing the deferral ceiling: %w", err)
	}
	return nil
}

// hasOpenQuestion reports whether this address already has a live disposition,
// which a further message joins rather than adding to the ceiling.
func hasOpenQuestion(ctx context.Context, tx pgx.Tx, email string) (bool, error) {
	var open bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM capture_pending_counterparty
		   WHERE email = $1 AND status IN ('pending', 'unsure'))`, email).Scan(&open)
	if err != nil {
		return false, fmt.Errorf("capture: checking for an open disposition: %w", err)
	}
	return open, nil
}

// atDeferralCap reports whether this workspace already holds its ceiling of open
// questions. Counts the live states, not just 'pending': an 'unsure' row is a
// question a human still owes an answer to, so a workspace cannot walk past the
// bound by leaving its review queue unattended.
func atDeferralCap(ctx context.Context, tx pgx.Tx) (bool, error) {
	var live int
	err := tx.QueryRow(ctx, `
		SELECT count(*) FROM capture_pending_counterparty
		 WHERE status IN ('pending', 'unsure')`).Scan(&live)
	if err != nil {
		return false, fmt.Errorf("capture: counting open dispositions: %w", err)
	}
	return live >= PendingDeferralCap, nil
}

// atDomainDeferralCap reports whether ONE sender domain already holds its share
// of the workspace's open questions.
//
// The workspace ceiling alone fails in a way an outsider can steer: mailing from
// 500 fresh addresses at one throwaway domain fills the whole queue, and from
// then on no NEW corporate-domain sender is deferred at all. Nothing is hidden or
// destroyed by that — the mail still lands — but the flood is self-sustaining and
// costs the workspace its deferral machinery until someone clears the ledger by
// hand. Capping per domain means a flood can only ever consume its own share.
//
// An address with no domain is not subject to it: there is nothing to count by,
// and the workspace ceiling still bounds those rows.
func atDomainDeferralCap(ctx context.Context, tx pgx.Tx, domain string) (bool, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return false, nil
	}
	var live int
	err := tx.QueryRow(ctx, `
		SELECT count(*) FROM capture_pending_counterparty
		 WHERE domain = $1 AND status IN ('pending', 'unsure')`, domain).Scan(&live)
	if err != nil {
		return false, fmt.Errorf("capture: counting open dispositions for a domain: %w", err)
	}
	return live >= PendingDeferralDomainCap, nil
}
