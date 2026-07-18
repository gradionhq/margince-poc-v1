// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The RC-2 personal-mail exclusion gate: the Sink's pre-write check that a
// captured record is not the capturing human's personal mail (CAP-DDL-3).
// An excluded message writes zero domain rows — one system_log line and one
// capture.skipped event are all it leaves behind.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/capture/exclusion"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// ExclusionRules is the RC-2 gate's seam: the caller's personal-mail rules,
// loaded per capturing user. Injected so the ONE Sink runs the exclusion
// gate for EVERY connector (imap one-shot, gmail sync) without any of them
// knowing about it. nil means a role that wired no gate — then it is a
// no-op and every record proceeds.
type ExclusionRules interface {
	RulesFor(ctx context.Context, userID ids.UUID) ([]exclusion.Rule, error)
}

// gateExclusion applies the RC-2 personal-mail gate: a record matching one
// of the capturing user's rules records the skip (audit + one
// capture.skipped event) and returns ErrSkip — so the connector counts it
// and writes nothing else; otherwise nil, and ingestion proceeds.
func (s *Sink) gateExclusion(ctx context.Context, rec connector.NormalizedRecord) error {
	rule, excluded, err := s.excluded(ctx, rec)
	if err != nil {
		return err
	}
	if !excluded {
		return nil
	}
	if err := s.emitSkip(ctx, rec, rule); err != nil {
		return err
	}
	return fmt.Errorf("capture: %s/%s excluded by a personal-mail rule (%s): %w",
		rec.NaturalKey.SourceSystem, rec.NaturalKey.SourceID, rule.Kind, connector.ErrSkip)
}

// excluded reports whether this record matches one of the capturing user's
// personal-mail rules (RC-2), and which rule. Only mail records carry match
// attributes, so a record with none (a lead, a non-mail activity) never
// loads the rule set — the gate is free for them. The rules are the
// on-behalf-of human's (the connector acts for them); the read is
// RLS-scoped to the workspace already on the context.
func (s *Sink) excluded(ctx context.Context, rec connector.NormalizedRecord) (exclusion.Rule, bool, error) {
	if s.exclusions == nil || !hasMatchAttrs(rec.Match) {
		return exclusion.Rule{}, false, nil
	}
	actor, _ := principal.Actor(ctx) // Upsert already validated a connector actor
	userID := actor.OnBehalfOf
	if userID.IsZero() {
		userID = actor.UserID
	}
	if userID.IsZero() {
		// Fail closed: a capture connector always acts for a granting human
		// (RC-8). With no effective user we cannot evaluate the personal-mail
		// gate — refuse rather than load rules for the nil user and let
		// personal mail through unexcluded.
		return exclusion.Rule{}, false, errors.New("capture: exclusion gate has no capturing user — refusing to ingest unexcluded")
	}
	rules, err := s.exclusions.RulesFor(ctx, userID)
	if err != nil {
		return exclusion.Rule{}, false, fmt.Errorf("capture: loading exclusion rules: %w", err)
	}
	rule, ok := exclusion.Match(rec.Match, rules)
	return rule, ok, nil
}

// hasMatchAttrs reports whether a record carries anything the gate can match
// — the cheap short-circuit that keeps non-mail writes off the rule query.
func hasMatchAttrs(a connector.ExclusionAttrs) bool {
	return a.SenderDomain != "" || len(a.RecipientDomains) > 0 || len(a.Labels) > 0
}

// emitSkip records an excluded message: one system_log 'capture_skip' row
// (the non-entity operational ledger — an excluded message mutates nothing,
// so it has no place in audit_log) paired with exactly one entity-less
// capture.skipped{personal_exclusion} bus event, in one transaction. No
// domain row, no raw original — the message leaves nothing else behind. The
// event is entity-less by nature (a pipeline event, events envelope class);
// its ledger trace link is the system_log row's id.
func (s *Sink) emitSkip(ctx context.Context, rec connector.NormalizedRecord, rule exclusion.Rule) error {
	detail := map[string]any{
		"reason":          "personal_exclusion",
		fieldSourceSystem: rec.NaturalKey.SourceSystem,
		"source_id":       rec.NaturalKey.SourceID,
		"rule_kind":       rule.Kind,
	}
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		ledgerID, err := storekit.LogSystem(ctx, tx, "capture_skip", detail)
		if err != nil {
			return fmt.Errorf("capture: logging exclusion skip: %w", err)
		}
		if err := storekit.EmitPipeline(ctx, tx, ledgerID, "capture.skipped", detail); err != nil {
			return fmt.Errorf("capture: emitting capture.skipped: %w", err)
		}
		return nil
	})
}
