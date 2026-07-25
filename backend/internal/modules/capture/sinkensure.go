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
	row, decision, ok, err := s.derivationStart(ctx, tx, rec, activityID)
	if err != nil || !ok {
		return counterpartyDecision{}, err
	}

	// T0 internal own-domain: colleagues, not customers. Creates nothing, and
	// records nothing — an internal address is not a disposition anyone reviews.
	internal, err := s.internalDomainTx(ctx, tx, cp.Domain)
	if err != nil {
		return counterpartyDecision{}, err
	}
	if internal {
		return counterpartyDecision{}, nil
	}
	// T1 correspondence-positive: the workspace has provably written to this
	// address, so it is a counterparty by demonstrated intent. Asked of EVERY
	// sender, not only those a suppression rule matched — since T4 defers the
	// ambiguous class, the answer now decides create-versus-defer for ordinary
	// senders too, which is worth a query per captured message.
	corresponded, err := s.correspondencePositiveTx(ctx, tx, cp.Email)
	if err != nil {
		return counterpartyDecision{}, err
	}
	decision.create = corresponded

	// T2 transactional / ESP infrastructure, which T1 outranks.
	suppressed, err := s.registrySuppresses(ctx, tx, rec, row, corresponded)
	if err != nil {
		return counterpartyDecision{}, err
	}
	if suppressed {
		return counterpartyDecision{}, nil
	}

	// An address this workspace has ALREADY decided about is not the ambiguous
	// class, whatever its domain — so this runs BEFORE the free-mail tier, which
	// would otherwise set create=true and skip the check entirely, minting the
	// person a prior `noise` verdict refused every time that sender wrote again.
	alreadyKnown, settled, err := s.alreadyDecided(ctx, tx, rec, cp.Email)
	if err != nil {
		return counterpartyDecision{}, err
	}
	// T1 OUTRANKS a stale terminal answer, exactly as it outranks the T2
	// registry, and for the same reason: the workspace writing to an address is
	// the strongest evidence it owns that the address is a counterparty. Without
	// this, a `noise` or `suppressed` row would bar that sender from ever
	// becoming a record again no matter how much the owner corresponded with
	// them — and since nothing clears those statuses, "reply to recover" would
	// only half work: the hide would stop, the record would still be refused.
	if settled && !corresponded {
		return counterpartyDecision{}, nil
	}
	decision.create = decision.create || alreadyKnown

	// T3 free-mail (CAP-PARAM-5): a personal mailbox is a person, never a
	// company — gmail.com is not an organization whatever else is true of it.
	// Its domain already says what it is, so it is not the ambiguous class.
	if s.freemail != nil && s.freemail.IsFreemail(cp.Domain) {
		decision.create, decision.suppressOrg = true, true
		// Recorded on the ledger too, not only on this decision: if anything ever
		// lets a free-mail sender reach T4, whoever creates the records days
		// later must still know that gmail.com names a person and not a company.
		row.SuppressOrg = true
	}

	if !decision.create {
		return counterpartyDecision{}, s.deferAmbiguous(ctx, tx, rec, row)
	}
	return decision, nil
}

// deferAmbiguous is T4: a first-time sender nothing about this address yet calls
// stranger or customer. ADR-0063's create-on-sight is what manufactured junk
// from exactly this class, so the message is captured, no record is minted, and
// the verdict engine answers the question the ledger now holds.
func (s *Sink) deferAmbiguous(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord, row dispositionRow) error {
	row.Status = PendingStatusPending
	capped, err := recordDisposition(ctx, tx, row)
	if err != nil {
		return err
	}
	if !capped {
		return nil
	}
	// The workspace is holding its ceiling of open questions, which an outsider
	// can drive by mailing from fresh addresses. Say so where an operator will
	// see it: silence here would read as a sender that was judged and dismissed,
	// when in fact nothing was ever asked.
	return s.logBreadcrumbTx(ctx, tx, "capture_deferral_capped", rec,
		"the workspace is at its open-disposition ceiling; the message stands unjudged")
}

// derivationStart settles whether a derivation is possible at all and builds the
// two values the ladder works on. It reports ok=false when nothing can be
// derived — no resolver wired, no counterparty address, or no granting human.
//
// The last of those is the one with teeth (RC-8): a capture connector always
// acts for a human, and with no owner nothing can honestly own the created rows.
// The ACTIVITY still stands — refusing the derivation is the honest answer,
// where failing the capture would throw away a message we successfully read — so
// the fault is recorded for the nightly reconcile and creation is skipped.
func (s *Sink) derivationStart(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord, activityID ids.UUID) (dispositionRow, counterpartyDecision, bool, error) {
	cp := rec.Counterparty
	if s.ensurer == nil || cp.Email == "" {
		return dispositionRow{}, counterpartyDecision{}, false, nil
	}
	actor, owner := capturePrincipal(ctx)
	if owner.IsZero() {
		return dispositionRow{}, counterpartyDecision{}, false,
			s.logBreadcrumbTx(ctx, tx, "capture_ensure_fault", rec, "no granting human on the connector principal")
	}
	row := dispositionRow{
		Email: cp.Email, Domain: cp.Domain, DisplayName: cp.DisplayName,
		ActivityID: activityID, OwnerID: owner,
	}
	return row, counterpartyDecision{owner: owner, capturedBy: actor.ID}, true, nil
}

// alreadyDecided applies the tier for an address this workspace has ALREADY
// concluded about, which is not the ambiguous class however new the message is.
// It reports whether the sender is a known counterparty, and whether a prior
// answer settles the matter (in which case the caller stops: no record, no new
// question, and no model call — the hide sweep folds this message in with
// the rest of that sender's mail on its next pass).
func (s *Sink) alreadyDecided(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord, email string) (known, settled bool, err error) {
	prior, err := s.priorDispositionTx(ctx, tx, email)
	if err != nil {
		return false, false, err
	}
	switch prior {
	case PendingStatusReal:
		return true, false, nil
	case PendingStatusNoise:
		return false, true, s.logBreadcrumbTx(ctx, tx, "capture_noise_sender", rec,
			"a prior verdict already judged this sender noise")
	case PendingStatusRejected, PendingStatusSuppressed:
		// A human's decline and a registry suppression are answers too. Without
		// this the next message re-raises the same question, buys another model
		// call, and offers the human the decision they already made.
		return false, true, s.logBreadcrumbTx(ctx, tx, "capture_decided_sender", rec,
			"this sender was already decided: "+prior)
	default:
		return false, false, nil
	}
}

// priorDispositionTx reports what this workspace already concluded about an
// address, or "" if it has never decided. A person that exists by any route —
// an earlier verdict, a human typing them in, an import — counts as `real`:
// what matters is that the address is already a known counterparty, not which
// path made it one.
func (s *Sink) priorDispositionTx(ctx context.Context, tx pgx.Tx, email string) (string, error) {
	normalized := normalizeEmail(email)
	if normalized == "" {
		return "", nil
	}
	var status string
	err := tx.QueryRow(ctx, `
		SELECT CASE
		         WHEN EXISTS (
		           SELECT 1 FROM person_email pe JOIN person p ON p.id = pe.person_id
		            WHERE pe.email = $1 AND p.archived_at IS NULL) THEN 'real'
		         ELSE coalesce((
		           SELECT status FROM capture_pending_counterparty
		            WHERE email = $1
		              AND status IN ('real', 'noise', 'rejected', 'suppressed')
		            ORDER BY resolved_at DESC NULLS LAST
		            LIMIT 1), '')
		       END`, normalized).Scan(&status)
	if err != nil {
		return "", fmt.Errorf("capture: reading the prior disposition: %w", err)
	}
	return status, nil
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

// capturePrincipal resolves the acting connector and the human it acts for.
// The granting human is who anything created would belong to, so a connector
// acting for nobody can own nothing.
func capturePrincipal(ctx context.Context) (principal.Principal, ids.UUID) {
	actor, _ := principal.Actor(ctx) // Upsert already validated a connector actor
	owner := actor.OnBehalfOf
	if owner.IsZero() {
		owner = actor.UserID
	}
	return actor, owner
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

// decideCounterpartyGuarded runs the tier ladder inside a SAVEPOINT so a fault
// costs the derivation and nothing else.
//
// The ladder decides whether a RECORD is created; it must never decide whether
// a MESSAGE is kept. But it cannot simply swallow its own errors: the first
// failed statement poisons the surrounding transaction, so every later
// statement — including the breadcrumb explaining the failure, and the COMMIT
// itself — fails too, and the activity, the raw evidence, the audit row and the
// outbox event all roll back. A Sink error then stops the connector's pull, so
// one deterministic fault would cost the whole mailbox rather than one
// derivation.
//
// Rolling back to the savepoint returns the transaction to a usable state, so
// the fault is recorded and the capture commits without its derivation.
func (s *Sink) decideCounterpartyGuarded(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord, activityID ids.UUID) (counterpartyDecision, error) {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return counterpartyDecision{}, fmt.Errorf("capture: opening the counterparty-gate savepoint: %w", err)
	}
	decision, gateErr := s.decideCounterparty(ctx, sp, rec, activityID)
	if gateErr != nil {
		if rbErr := sp.Rollback(ctx); rbErr != nil {
			// Without a clean rollback the outer transaction stays poisoned,
			// so there is no committing anything — report the original fault.
			return counterpartyDecision{}, errors.Join(gateErr, rbErr)
		}
		return counterpartyDecision{}, s.logBreadcrumbTx(ctx, tx, "capture_ensure_fault", rec, gateErr.Error())
	}
	if err := sp.Commit(ctx); err != nil {
		return counterpartyDecision{}, fmt.Errorf("capture: committing the counterparty gate: %w", err)
	}
	return decision, nil
}

// logBreadcrumbTx records one capture-gate decision on the caller's capture
// transaction. Every tier outcome a human might have to explain — a suppression,
// a T1 spare that overrode one — commits with the activity it is about, so a
// rolled-back capture never leaves a breadcrumb for a message that does not
// exist, and no gate has to borrow a second pool connection while holding one.
func (s *Sink) logBreadcrumbTx(ctx context.Context, tx pgx.Tx, action string, rec connector.NormalizedRecord, reason string) error {
	_, err := storekit.LogSystem(ctx, tx, action, map[string]any{
		fieldReason:       reason,
		fieldSourceSystem: rec.NaturalKey.SourceSystem,
		fieldSourceID:     rec.NaturalKey.SourceID,
	})
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
