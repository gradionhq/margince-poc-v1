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
func (s *Sink) ensureCounterparty(ctx context.Context, rec connector.NormalizedRecord, ref datasource.EntityRef) {
	cp := rec.Counterparty
	if s.ensurer == nil || cp.Email == "" || ref.Type != datasource.EntityActivity {
		return
	}
	actor, _ := principal.Actor(ctx) // Upsert already validated a connector actor
	owner := actor.OnBehalfOf
	if owner.IsZero() {
		owner = actor.UserID
	}
	if owner.IsZero() {
		// RC-8: a capture connector always acts for a granting human; with
		// no owner nothing can honestly own the created rows.
		s.logEnsureFault(ctx, rec, errors.New("no granting human on the connector principal"))
		return
	}
	internal, err := s.internalDomain(ctx, cp.Domain)
	if err != nil {
		s.logEnsureFault(ctx, rec, err)
		return
	}
	if internal {
		// Colleagues, not customers: mail on the workspace's own domain
		// creates nothing.
		return
	}
	// T1 correspondence-positive (CAP-DDL-7, ADR-0072): a human the owner has
	// ever written to is a real contact, never suppressed as infrastructure —
	// even when their mail carries a List-Unsubscribe footer. This runs BEFORE
	// the T2 transactional gate, so a known contact's newsletter is spared. The
	// predicate is an index-backed EXISTS over the owner's outbound activities
	// to this address (idx_activity_counterparty_email); a fault here is logged
	// and treated as not-corresponded (fail safe toward the suppression check,
	// never toward a false suppression — the gate itself still requires
	// corroboration on the prefix rules).
	corresponded, err := s.correspondencePositive(ctx, cp.Email)
	if err != nil {
		s.logEnsureFault(ctx, rec, err)
	}
	// T2 transactional / ESP infrastructure (CAP-PARAM-6, ADR-0072): a
	// DocuSign envelope or a SendGrid relay is not a counterparty's company.
	// Suppress person AND org derivation — the activity already committed and
	// stands (a DocuSign envelope is a real timeline item) — and record the
	// reason durably for observability.
	if !corresponded && s.transactional != nil {
		if suppress, reason := s.transactional.Suppress(transactionalInput(cp)); suppress {
			s.logSuppression(ctx, rec, reason)
			return
		}
	}
	suppressOrg := s.freemail != nil && s.freemail.IsFreemail(cp.Domain)
	err = s.ensurer.EnsureCounterparty(ctx, EnsureRequest{
		Email:       cp.Email,
		DisplayName: cp.DisplayName,
		Domain:      cp.Domain,
		OwnerID:     owner,
		ActivityID:  ref.ID,
		Source:      captureSource(rec),
		CapturedBy:  actor.ID,
		SuppressOrg: suppressOrg,
	})
	if err != nil {
		s.logEnsureFault(ctx, rec, err)
	}
}

// internalDomain reports whether domain is one of the workspace's own mail
// domains (the colleagues gate).
func (s *Sink) internalDomain(ctx context.Context, domain string) (bool, error) {
	if domain == "" {
		return false, nil
	}
	var internal bool
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM workspace_email_domain WHERE domain = lower($1))`,
			domain).Scan(&internal)
	})
	if err != nil {
		return false, fmt.Errorf("capture: internal-domain gate: %w", err)
	}
	return internal, nil
}

// correspondencePositive reports whether the owner has ever written to this
// address — an outbound activity whose counterparty is email exists (CAP-DDL-7).
// Sending to someone is affirmative intent; the first outbound mail TO an
// address makes it correspondence-positive immediately (the outbound activity
// carries counterparty_email). A cold inbound alone does not. The lookup is
// index-backed (idx_activity_counterparty_email) and RLS-scoped to the
// workspace.
func (s *Sink) correspondencePositive(ctx context.Context, email string) (bool, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return false, nil
	}
	var corresponded bool
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM activity WHERE counterparty_email = $1 AND direction = 'outbound')`,
			email).Scan(&corresponded)
	})
	if err != nil {
		return false, fmt.Errorf("capture: correspondence-positive check: %w", err)
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
