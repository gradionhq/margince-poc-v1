// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The ADR-0063 counterparty auto-create follow-up: after a captured mail
// activity commits, the Sink ensures the human behind it exists — person
// always, company unless suppressed — through the resolver seam compose
// injects. Capture itself never touches person/organization SQL.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// CounterpartyEnsurer is the auto-create seam (ADR-0063): after a captured
// mail activity commits, the pipeline ensures the human behind it exists —
// person always, company unless suppressed — through the ONE dedupe
// chokepoint. Compose injects the people module's implementation; capture
// itself never touches person/organization SQL.
type CounterpartyEnsurer interface {
	EnsureCounterparty(ctx context.Context, in EnsureRequest) error
}

// EnsureRequest names one captured message's counterparty for the resolver.
type EnsureRequest struct {
	Email       string
	DisplayName string // untrusted header text
	Domain      string
	OwnerID     ids.UUID // the granting human — owner of anything created
	ActivityID  ids.UUID
	Source      string
	CapturedBy  string
	SuppressOrg bool // free-mail domain: person yes, company no
}

// WithEnsurer returns a copy wired to the counterparty auto-create path:
// freemail decides which domains never derive a company (CAP-PARAM-5), and
// transactional decides which senders are mail infrastructure that derive no
// counterparty at all while the activity stands (CAP-PARAM-6, ADR-0072). A nil
// ensurer keeps capture activity-only (a role that wired no resolver); a nil
// transactional list simply runs no T2 suppression.
func (s *Sink) WithEnsurer(ensurer CounterpartyEnsurer, freemail *FreemailList, transactional *TransactionalList) *Sink {
	c := *s
	c.ensurer = ensurer
	c.freemail = freemail
	c.transactional = transactional
	return &c
}

// ensureCounterparty is the auto-create follow-up for one freshly captured
// mail activity: the deterministic gates first (internal domain → skip
// everything; free-mail → person only), then the resolver seam. Runs after
// the capture transaction committed, and NEVER fails the capture — a fault
// lands in system_log for the nightly reconcile (the link-less connector
// activity is the retry marker).
func (s *Sink) ensureCounterparty(ctx context.Context, rec connector.NormalizedRecord, ref datasource.EntityRef, decision counterpartyDecision) {
	if !decision.create {
		return
	}
	cp := rec.Counterparty
	err := s.ensurer.EnsureCounterparty(ctx, EnsureRequest{
		Email:       cp.Email,
		DisplayName: cp.DisplayName,
		Domain:      cp.Domain,
		OwnerID:     decision.owner,
		ActivityID:  ref.ID,
		Source:      captureSource(rec),
		CapturedBy:  decision.capturedBy,
		SuppressOrg: decision.suppressOrg,
	})
	if err != nil {
		s.logEnsureFault(ctx, rec, err)
	}
}

// counterpartyDecision is what the tiered gate concluded inside the capture
// transaction, for the post-commit step to act on. Creation is deliberately
// NOT done in that transaction: the timeline row must never be lost to a
// resolver fault, and the 60 s capture budget must not wait on record creation.
type counterpartyDecision struct {
	create      bool
	suppressOrg bool
	owner       ids.UUID
	capturedBy  string
}

// decideCounterparty runs the tiered creation gate (ADR-0072 §1) and records
// what it decided, both INSIDE the capture transaction — so there is no window
// in which an activity exists with no disposition, and a T2 suppression or a T4
// deferral is durable the moment the mail lands.
//
// It decides only. The classes that create do so after the commit; the classes
// that do not create are finished here.
func (s *Sink) decideCounterparty(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord, activityID ids.UUID) (counterpartyDecision, error) {
	cp := rec.Counterparty
	if s.ensurer == nil || cp.Email == "" {
		return counterpartyDecision{}, nil
	}
	actor, _ := principal.Actor(ctx) // Upsert already validated a connector actor
	owner := actor.OnBehalfOf
	if owner.IsZero() {
		owner = actor.UserID
	}
	if owner.IsZero() {
		// RC-8: a capture connector always acts for a granting human; with no
		// owner nothing can honestly own the created rows. The ACTIVITY still
		// stands — refusing the derivation is the honest answer, failing the
		// capture would throw away a message we successfully read — so the
		// fault is recorded for the nightly reconcile and creation is skipped.
		if err := s.logFaultTx(ctx, tx, rec, errors.New("no granting human on the connector principal")); err != nil {
			return counterpartyDecision{}, err
		}
		return counterpartyDecision{}, nil
	}
	row := dispositionRow{
		Email: cp.Email, Domain: cp.Domain, DisplayName: cp.DisplayName,
		ActivityID: activityID, OwnerID: owner,
	}
	decision := counterpartyDecision{owner: owner, capturedBy: actor.ID}

	// T0 internal own-domain: colleagues, not customers. Creates nothing, and
	// records nothing — an internal address is not a disposition anyone reviews.
	internal, err := s.internalDomainTx(ctx, tx, cp.Domain)
	if err != nil {
		return counterpartyDecision{}, err
	}
	if internal {
		return counterpartyDecision{}, nil
	}
	// T2 transactional / ESP infrastructure (CAP-PARAM-6, ADR-0072): a
	// DocuSign envelope or a SendGrid relay is not a counterparty's company.
	// Suppress person AND org derivation — the activity already committed and
	// stands (a DocuSign envelope is a real timeline item) — and record the
	// reason durably for observability.
	if s.transactional != nil {
		if suppress, reason := s.transactional.Suppress(transactionalInput(cp)); suppress {
			// T1 correspondence-positive OUTRANKS T2 (ADR-0072 §1), and the
			// precedence is load-bearing: a known contact whose newsletter
			// footer carries a List-Unsubscribe header, or who mails from
			// `news.acme.com`, must not be suppressed as bulk infrastructure.
			// Someone the workspace has written to is a counterparty by
			// demonstrated intent, whatever their mail plumbing looks like.
			// Asked only here — the tier below is the only place the answer
			// changes an outcome, and asking costs a query per captured message.
			corresponded, err := s.correspondencePositiveTx(ctx, tx, cp.Email)
			if err != nil {
				return counterpartyDecision{}, err
			}
			if !corresponded {
				// T2 stands: person and org are suppressed, the activity keeps
				// its place on the timeline, and the reason lands on the ledger
				// so a wrong registry entry is queryable, not only logged.
				row.Status, row.Reason = PendingStatusSuppressed, reason
				if err := recordDisposition(ctx, tx, row); err != nil {
					return counterpartyDecision{}, err
				}
				s.logSuppression(ctx, rec, reason)
				return counterpartyDecision{}, nil
			}
			// The rule matched and T1 overrode it. Recorded on its own so a
			// spare is as diagnosable as a suppression — this is the one path
			// that lets an address the registry calls infrastructure become a
			// record, and ops must be able to see it happen.
			s.logCorrespondenceSpare(ctx, rec, reason)
			decision.create = true
		}
	}

	// T3 free-mail (CAP-PARAM-5): a personal mailbox is a person, never a
	// company — gmail.com is not an organization whatever else is true of it.
	decision.suppressOrg = s.freemail != nil && s.freemail.IsFreemail(cp.Domain)

	if !decision.create {
		// T4 ambiguous first-time sender (ADR-0072 §1). Nothing about this
		// address yet says stranger or customer, and ADR-0063's create-on-sight
		// is what manufactured junk from exactly this class. Defer instead: the
		// activity stands, no record is minted, and the verdict engine answers
		// the question the ledger now holds.
		row.Status = PendingStatusPending
		if err := recordDisposition(ctx, tx, row); err != nil {
			return counterpartyDecision{}, err
		}
		return counterpartyDecision{}, nil
	}
	return decision, nil
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

// correspondencePositive reports whether the workspace has ever sent mail to
// email — the T1 evidence (ADR-0072 §1). It reads only
// `counterparty_outbound_attested`, the provider's own attestation that the
// mailbox owner sent the message, and never `direction`: direction is derived
// by comparing the forgeable From header against the owner, so honoring it here
// would let a spoofed From:owner message delivered to the inbox whitelist any
// address it names past the T2 suppression gate.
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

// logSuppression records a T2 transactional suppression in system_log — the
// activity stands, no counterparty was derived, and the reason is durable for
// ops (until CAP-DDL-8's disposition row carries it, ADR-0072 phase 2a). Never
// fails capture: a failed breadcrumb only loses observability, not correctness.
func (s *Sink) logSuppression(ctx context.Context, rec connector.NormalizedRecord, reason string) {
	detail := map[string]any{
		fieldReason:       reason,
		fieldSourceSystem: rec.NaturalKey.SourceSystem,
		fieldSourceID:     rec.NaturalKey.SourceID,
	}
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, logErr := storekit.LogSystem(ctx, tx, "capture_transactional_suppressed", detail)
		return logErr
	})
	if err != nil {
		slog.ErrorContext(ctx, "capture: recording transactional suppression", "err", err, "reason", reason)
	}
}

// logCorrespondenceSpare records that T1 overrode a matched T2 suppression
// rule: the workspace has provably written to this address, so the counterparty
// is derived despite the registry calling its domain infrastructure. Carries
// the rule that would have fired, so a wrong registry entry stays diagnosable.
// Never fails capture: a failed breadcrumb only loses observability.
func (s *Sink) logCorrespondenceSpare(ctx context.Context, rec connector.NormalizedRecord, reason string) {
	detail := map[string]any{
		fieldReason:       reason,
		fieldSourceSystem: rec.NaturalKey.SourceSystem,
		fieldSourceID:     rec.NaturalKey.SourceID,
	}
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, logErr := storekit.LogSystem(ctx, tx, "capture_correspondence_spared", detail)
		return logErr
	})
	if err != nil {
		slog.ErrorContext(ctx, "capture: recording correspondence spare", "err", err, "reason", reason)
	}
}

// logFaultTx records an ensure fault on the caller's capture transaction. Same
// breadcrumb as logEnsureFault, written where the tier ladder runs: it must
// commit with the activity it is about, not in a transaction that could fail
// separately and leave the fault invisible.
func (s *Sink) logFaultTx(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord, cause error) error {
	_, err := storekit.LogSystem(ctx, tx, "capture_ensure_fault", map[string]any{
		fieldReason:       "counterparty_ensure_failed",
		fieldSourceSystem: rec.NaturalKey.SourceSystem,
		fieldSourceID:     rec.NaturalKey.SourceID,
		"error":           cause.Error(),
	})
	if err != nil {
		return fmt.Errorf("capture: recording ensure fault: %w", err)
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
		fieldSourceID:     rec.NaturalKey.SourceID,
		"error":           cause.Error(),
	}
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, logErr := storekit.LogSystem(ctx, tx, "capture_ensure_fault", detail)
		return logErr
	})
	if err != nil {
		// The ledger itself failed — nothing left but the process log; the
		// nightly reconcile still finds the link-less activity.
		slog.ErrorContext(ctx, "capture: recording ensure fault", "err", err, "cause", cause)
	}
}
