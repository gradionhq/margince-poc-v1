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

	"github.com/gradionhq/margince/backend/internal/shared/kernel/textlang"
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
// A lead is not correspondence and is not judged. A channel record (Telegram,
// WhatsApp) does reach the ActivityFields arm, and is excluded by the address
// guard below rather than by the type: it names provider identities, never mail
// addresses, so there is nothing here to measure it against.
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
	own, err := trustedOwnDomainsTx(ctx, tx)
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
	own, err := ownDomainsTx(ctx, tx)
	if err != nil {
		return false, err
	}
	// Through the same matcher every other caller uses, so a colleague at
	// mail.acme.com is a colleague here too. Exact equality read the same table
	// and answered differently, which minted contacts for the workspace's own
	// employees whenever they wrote from a subdomain.
	return own.CoversDomain(domain), nil
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
//
// One shape is excluded, and it is narrow on purpose: a SINGLE outbound whose
// own text declines. A founder answering unsolicited mail with "not interested"
// produced exactly one attested outbound, and that was enough to admit the
// spammer ahead of every suppression rule — the record for "PE Insights" in a
// real import came from precisely that reply. Declining is the one thing a
// person writes that means the opposite of intent.
//
// Everything else still counts on sight. A reply that engages — a question, a
// price, a meeting — is intent no matter that it answered rather than opened,
// and so is any second outbound. The test is the WORDS, not the direction or
// the order: a rule that demoted every reply would have refused a prospect who
// wrote first and got answered, which is the most ordinary shape there is.
func (s *Sink) correspondencePositiveTx(ctx context.Context, tx pgx.Tx, email string) (bool, error) {
	normalized := normalizeEmail(email)
	if normalized == "" {
		return false, nil
	}
	// Body and subject travel SEPARATELY because only the body carries a quoted
	// thread. Concatenating them first would leave no way to strip the sender's
	// own words out of our reply.
	rows, err := tx.Query(ctx, `
		SELECT COALESCE(subject, ''), COALESCE(body, '')
		  FROM activity
		 WHERE counterparty_email = $1 AND counterparty_outbound_attested
		 LIMIT 2`, normalized)
	if err != nil {
		return false, fmt.Errorf("capture: correspondence-positive gate: %w", err)
	}
	defer rows.Close()
	var texts []string
	for rows.Next() {
		var subject, body string
		if err := rows.Scan(&subject, &body); err != nil {
			return false, fmt.Errorf("capture: correspondence-positive gate: %w", err)
		}
		// Only what the SENDER of this outbound message wrote. A stored body
		// keeps the quoted thread beneath the reply (mailmap caps it at 8000
		// runes, it does not strip it), so a spammer who writes "not interested"
		// in their own mail would otherwise put those words into our reply the
		// moment somebody hits Reply — and talk themselves out of the CRM by
		// feeding this gate a decline the mailbox owner never wrote.
		texts = append(texts, subject+" "+textlang.NewTextOnly(body))
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("capture: correspondence-positive gate: %w", err)
	}
	switch len(texts) {
	case 0:
		return false, nil
	case 1:
		return !isDecliningReply(texts[0]), nil
	default:
		// The LIMIT 2 above is why this is safe on a high-volume address: the
		// query stops at the only distinction that matters, one outbound versus
		// more than one, and never loads a whole correspondence to count it.
		// Two or more outbound messages are a correspondence whatever any one of
		// them says; nobody declines twice and keeps writing.
		return true, nil
	}
}

// declinePhrases are what a person writes to end a conversation they never
// wanted. Deliberately short and unambiguous: every entry here has to be a
// phrase whose presence means "stop", because a false positive sends a genuine
// prospect to the verdict engine (a delay, and recoverable) while a false
// negative admits a spammer permanently.
var declinePhrases = []string{
	"not interested", "no interest", "kein interesse", "nicht interessiert",
	"please remove me", "remove me from", "unsubscribe", "opt out", "opt-out",
	"austragen", "abmelden", "bitte löschen sie", "keine weiteren e-mails",
	"do not contact", "don't contact", "stop emailing", "stop contacting",
	"no thanks", "no thank you",
}

// isDecliningReply reports whether an outbound message is a refusal rather than
// engagement. It reads the message's own words; the LLM verdict that follows
// reads the whole thread and has the final say.
func isDecliningReply(text string) bool {
	// Whitespace is collapsed before matching: a mail client wraps lines where
	// it likes, so "not\ninterested" is the same sentence as "not interested"
	// and a matcher that missed it would be defeated by the window width.
	lowered := strings.Join(strings.Fields(strings.ToLower(text)), " ")
	for _, phrase := range declinePhrases {
		if strings.Contains(lowered, phrase) {
			return true
		}
	}
	return false
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
// subject is the party the ladder is about, which is not always the message's
// counterparty: on an introduction it is the outsider a colleague copied, and
// asking the registry about the colleague's own domain would test a domain that
// is never infrastructure instead of the one that might be.
func (s *Sink) registrySuppresses(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord, subject connector.Counterparty, row dispositionRow, corresponded bool) (bool, error) {
	if s.transactional == nil {
		return false, nil
	}
	suppress, reason := s.transactional.Suppress(transactionalInput(subject))
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
