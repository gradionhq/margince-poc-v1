// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/pipelinetrace"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// reasonExcluded is the breadcrumb and trace reason for a message an
// exclusion rule kept out. It names the KIND of rule and nothing else: the
// address or domain that matched is exactly what the rule exists to keep out
// of the CRM, and a trace that repeated it would store it after all.
const (
	reasonExcludedAddress = "excluded_address"
	reasonExcludedDomain  = "excluded_domain"
	actionCaptureExcluded = "capture_excluded"
)

// dropBeforeStoreTx runs the gates that keep a message out of the CRM
// entirely — the colleagues-only gate and the exclusion lists — and answers
// the sentence the connector's skip carries, or "" when the message may be
// stored. Each drop leaves its breadcrumb (the operator's record) and its
// trace (the member's answer to "why did this never appear"), and neither
// names an address.
func (s *Sink) dropBeforeStoreTx(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord) (string, error) {
	// Colleague correspondence is not evidence of a customer relationship — it
	// is two employees talking, and the CRM was never asked to hold it.
	internal, err := s.internalOnlyTx(ctx, tx, rec)
	if err != nil {
		return "", err
	}
	if internal {
		if err := s.logBreadcrumbTx(ctx, tx, actionCaptureInternalDropped, rec, reasonInternalOnly); err != nil {
			return "", err
		}
		if err := s.traceTx(ctx, tx, rec, pipelinetrace.StageInternalDrop, TraceInternal, reasonInternalOnly); err != nil {
			return "", err
		}
		return "all participants are on the workspace's own domains", nil
	}
	// A message the workspace or this mailbox's owner ruled out is not the
	// CRM's to hold either.
	excluded, err := excludedTx(ctx, tx, rec)
	if err != nil {
		return "", err
	}
	if excluded == "" {
		return "", nil
	}
	if err := s.logBreadcrumbTx(ctx, tx, actionCaptureExcluded, rec, excluded); err != nil {
		return "", err
	}
	if err := s.traceTx(ctx, tx, rec, pipelinetrace.StageInternalDrop, TraceSuppressed, excluded); err != nil {
		return "", err
	}
	return "a capture exclusion rule keeps this message out (" + excluded + ")", nil
}

// excludedTx reports whether any address this message names — sender,
// recipients, copies — is kept out by a workspace exclusion, or by one the
// mailbox owner behind this connection set for themselves. Runs on the
// capture transaction before the raw store, like the internal gate: a message
// that reaches the raw store has been kept whatever happens next. The answer
// is the reason to trace, or "" when nothing matched.
func excludedTx(ctx context.Context, tx pgx.Tx, rec connector.NormalizedRecord) (string, error) {
	if len(rec.Addresses) == 0 {
		return "", nil
	}
	addresses := make([]string, 0, len(rec.Addresses))
	domains := make([]string, 0, len(rec.Addresses))
	for _, a := range rec.Addresses {
		addresses = append(addresses, strings.ToLower(strings.TrimSpace(a)))
		if d := domainOfAddress(a); d != "" {
			domains = append(domains, d)
		}
	}
	// A domain rule covers its subdomains: an exclusion of acme.com keeps out
	// mail.acme.com, the way the own-domain matcher reads a domain.
	var kind string
	err := tx.QueryRow(ctx, `
		SELECT kind FROM capture_exclusion e
		 WHERE (e.scope = 'workspace' OR e.user_id = $3)
		   AND ((e.kind = 'address' AND e.value = ANY($1::text[]))
		     OR (e.kind = 'domain' AND EXISTS (
		           SELECT 1 FROM unnest($2::text[]) d
		            WHERE d = e.value OR d LIKE '%.' || e.value)))
		 ORDER BY e.kind LIMIT 1`,
		addresses, domains, nilToNull(actorUserID(ctx))).Scan(&kind)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	if kind == ExclusionKindAddress {
		return reasonExcludedAddress, nil
	}
	return reasonExcludedDomain, nil
}

// nilToNull hands a zero user id to SQL as NULL, so a connection with no
// human behind it matches no personal rule rather than a rule keyed on the
// zero uuid.
func nilToNull(id ids.UUID) *ids.UUID {
	if id == ids.Nil {
		return nil
	}
	return &id
}
