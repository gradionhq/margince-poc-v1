// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The mail-shaped gates of the tiered creation ladder (ADR-0072 §1), gathered
// in one place: each one reads a mail domain or a mail address, and each is
// therefore about mail alone. A channel record carries neither, which is why it
// never enters the ladder these serve (sinkchannel.go).

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// The breadcrumb action and reason for a message dropped as internal. The
// reason is the `internal_only` value the capture.skipped event carries
// (event-bus EVT-SEM-10); the row names the natural key and nothing else — an
// address, subject or body in the operational ledger would re-create in
// `system_log` exactly the disclosure the drop exists to prevent.
const (
	actionCaptureInternalDropped = "capture_internal_dropped"
	reasonInternalOnly           = "internal_only"
)

// internalOnlyTx reports whether every party to this record is on one of the
// workspace's own mail domains — the zero-rows condition (ADR-0082/A127,
// formulas §20).
//
// Only mail- and meeting-shaped records are judged. A channel record (Telegram,
// WhatsApp) names no mail addresses, and a lead is not correspondence at all;
// both are captured unchanged rather than being measured against a rule that
// cannot describe them.
//
// A record that enumerates no addresses is NOT internal. A connector reporting
// an empty set is saying it could not read the parties, which is a different
// statement from "there were none", and the direction to fail in is toward
// keeping the message.
func (s *Sink) internalOnlyTx(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord) (bool, error) {
	if _, ok := rec.Fields.(ActivityFields); !ok {
		return false, nil
	}
	if len(rec.Addresses) == 0 {
		return false, nil
	}
	own, err := ownDomainsTx(ctx, tx)
	if err != nil {
		return false, err
	}
	return own.AllInternal(rec.Addresses), nil
}

// internalDomainTx reports whether domain is one of the workspace's own mail
// domains (the colleagues gate). Runs on the capture transaction: the tier
// ladder decides and records atomically with the activity it is about.
func (s *Sink) internalDomainTx(ctx context.Context, tx pgx.Tx, domain string) (bool, error) {
	if domain == "" {
		return false, nil
	}
	var internal bool
	err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM workspace_email_domain WHERE domain = lower($1))`,
		domain).Scan(&internal)
	if err != nil {
		return false, fmt.Errorf("capture: internal-domain gate: %w", err)
	}
	return internal, nil
}

// correspondencePositiveTx reports whether the workspace has ever sent mail to
// email — the T1 evidence (ADR-0072 §1). It reads only
// `counterparty_outbound_attested` and never `direction`: direction is derived
// by comparing the forgeable From header against the owner, so honoring it here
// would let a spoofed From:owner message delivered to the inbox whitelist any
// address it names past the T2 suppression gate.
//
// Two writers set that column, and both are unforgeable statements that THIS
// installation sent the message: a connector attesting the mailbox owner's own
// sent copy, and the governed send path itself (activities.SendEmail), whose
// outbound row IS the sent copy — the provider's echo of it upserts onto the
// same natural key and writes nothing, so the evidence has to be stamped at
// send or it is never stamped at all.
//
// A single cold inbound is NOT correspondence — receiving mail is not intent.
// The first outbound message to an address counts immediately: writing to
// someone is affirmative intent toward them, and it is the message being
// captured right now that supplies it (the activity commits before this runs).
func (s *Sink) correspondencePositiveTx(ctx context.Context, tx pgx.Tx, email string) (bool, error) {
	normalized := normalizeEmail(email)
	if normalized == "" {
		return false, nil
	}
	var corresponded bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM activity
		  WHERE counterparty_email = $1 AND counterparty_outbound_attested
		)`, normalized).Scan(&corresponded)
	if err != nil {
		return false, fmt.Errorf("capture: correspondence-positive gate: %w", err)
	}
	return corresponded, nil
}

// registrySuppresses runs T2 against the transactional/ESP registry
// (CAP-PARAM-6): a DocuSign envelope or a SendGrid relay is not a
// counterparty's company, so person AND org derivation are suppressed while the
// activity stands — a signed envelope is a real timeline item — and the reason
// lands on the ledger so a wrong registry entry is queryable, not only logged.
//
// T1 OUTRANKS it, and the precedence is load-bearing: a known contact whose
// newsletter footer carries a List-Unsubscribe header is not infrastructure. A
// spare is recorded on its own, because it is the one path that lets an address
// the registry calls infrastructure become a record.
func (s *Sink) registrySuppresses(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord, row dispositionRow, corresponded bool) (bool, error) {
	if s.transactional == nil {
		return false, nil
	}
	suppress, reason := s.transactional.Suppress(transactionalInput(rec.Counterparty))
	if !suppress {
		return false, nil
	}
	if corresponded {
		return false, s.logBreadcrumbTx(ctx, tx, "capture_correspondence_spared", rec, reason)
	}
	row.Status, row.Reason = PendingStatusSuppressed, reason
	// A suppression asks nothing and so is never capped; the flag is only
	// meaningful for the deferring tier.
	if _, err := recordDisposition(ctx, tx, row); err != nil {
		return true, err
	}
	return true, s.logBreadcrumbTx(ctx, tx, "capture_transactional_suppressed", rec, reason)
}

// transactionalInput builds the transactional-gate input from a captured
// counterparty: the domain, the address local part (machine-sender
// corroboration), and the List-Unsubscribe signal the connector parsed.
func transactionalInput(cp connector.Counterparty) TransactionalInput {
	local, _, _ := strings.Cut(cp.Email, "@")
	return TransactionalInput{
		Domain:          cp.Domain,
		Localpart:       local,
		ListUnsubscribe: cp.ListUnsubscribe,
	}
}
