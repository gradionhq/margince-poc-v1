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

// WithEnsurer returns a copy wired to the counterparty auto-create path;
// freemail decides which domains never derive a company. nil ensurer keeps
// capture activity-only (a role that wired no resolver).
func (s *Sink) WithEnsurer(ensurer CounterpartyEnsurer, freemail *FreemailList) *Sink {
	c := *s
	c.ensurer = ensurer
	c.freemail = freemail
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

// logEnsureFault records an auto-create failure in system_log — the
// activity is already committed and stays; the nightly reconcile re-runs
// the resolver over link-less connector activities.
func (s *Sink) logEnsureFault(ctx context.Context, rec connector.NormalizedRecord, cause error) {
	detail := map[string]any{
		"reason":          "counterparty_ensure_failed",
		fieldSourceSystem: rec.NaturalKey.SourceSystem,
		"source_id":       rec.NaturalKey.SourceID,
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
