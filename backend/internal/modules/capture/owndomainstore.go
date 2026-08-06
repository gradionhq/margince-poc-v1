// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The administrator's view of the own-domain set (CAP-WIRE-2a).
//
// The set decides whether correspondence is STORED, so the surface over it is
// admin-gated and human-only: an agent must not widen or narrow what the CRM
// may hold. What a connected mailbox contributes is a candidate; confirming one
// is a human act, and this is where that act happens.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// OwnDomain is one registered domain and how it got there.
type OwnDomain struct {
	Domain    string
	Source    string
	Verified  bool
	CreatedAt time.Time
}

// OwnDomainStore reads and writes the workspace's own-domain registry.
type OwnDomainStore struct {
	pool *pgxpool.Pool
}

// NewOwnDomainStore builds the store over the app pool.
func NewOwnDomainStore(pool *pgxpool.Pool) *OwnDomainStore {
	return &OwnDomainStore{pool: pool}
}

// OwnDomainList is the registry plus what the installation's own company
// claims. The two are reported separately because only one of them is editable
// here: a company's own domains come from its profile and are changed there, so
// showing them as removable rows would offer an action this surface cannot
// perform.
type OwnDomainList struct {
	Domains       []OwnDomain
	AnchorDomains []string
}

// List returns the registry and the company's claimed domains.
func (s *OwnDomainStore) List(ctx context.Context) (OwnDomainList, error) {
	var out OwnDomainList
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := auth.Require(ctx, captureSettingsObject, principal.ActionRead); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT domain, source, verified, created_at
			  FROM workspace_email_domain ORDER BY domain`)
		if err != nil {
			return fmt.Errorf("capture: listing own domains: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var d OwnDomain
			if err := rows.Scan(&d.Domain, &d.Source, &d.Verified, &d.CreatedAt); err != nil {
				return fmt.Errorf("capture: listing own domains: %w", err)
			}
			out.Domains = append(out.Domains, d)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("capture: listing own domains: %w", err)
		}

		claimed, err := queryDomains(ctx, tx, anchorDomains)
		if err != nil {
			return err
		}
		out.AnchorDomains = claimed.Domains()
		return nil
	})
	return out, err
}

// Add registers a domain as the workspace's own, verified.
//
// An administrator entering a domain IS the human vouching for it — there is no
// second confirmation step, because there is no one else to ask. Idempotent on
// the folded domain: adding one a mailbox already contributed confirms it
// rather than failing.
func (s *OwnDomainStore) Add(ctx context.Context, raw string) (OwnDomain, error) {
	domain := normalizeDomain(strings.TrimPrefix(strings.TrimSpace(raw), "@"))
	if err := validOwnDomain(domain); err != nil {
		return OwnDomain{}, err
	}
	var out OwnDomain
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := auth.Require(ctx, captureSettingsObject, principal.ActionUpdate); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO workspace_email_domain (workspace_id, domain, source, verified)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, 'admin', true)
			ON CONFLICT (workspace_id, domain)
			  DO UPDATE SET source = 'admin', verified = true
			RETURNING domain, source, verified, created_at`, domain).
			Scan(&out.Domain, &out.Source, &out.Verified, &out.CreatedAt); err != nil {
			return fmt.Errorf("capture: registering own domain: %w", err)
		}
		// Audit-only, like the capture-settings write beside it: this is
		// workspace configuration, not a domain record, and the closed event
		// catalog carries no type for it. The audit row is the durable answer to
		// "who put this domain in", which is the question that will be asked.
		wsID, _ := principal.WorkspaceID(ctx)
		_, err := storekit.Audit(ctx, tx, "update", captureSettingsObject, wsID,
			nil, map[string]any{"own_email_domain": domain, "verified": true})
		return err
	})
	return out, err
}

// Remove stops treating a domain as the workspace's own.
//
// Removing one the company itself claims does nothing on its own: that claim
// lives on the company profile and is changed there. Saying so is better than
// silently accepting a delete that changes no behaviour.
func (s *OwnDomainStore) Remove(ctx context.Context, raw string) error {
	domain := normalizeDomain(strings.TrimPrefix(strings.TrimSpace(raw), "@"))
	if domain == "" {
		return nil
	}
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := auth.Require(ctx, captureSettingsObject, principal.ActionUpdate); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx,
			`DELETE FROM workspace_email_domain WHERE domain = $1`, domain)
		if err != nil {
			return fmt.Errorf("capture: removing own domain: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		wsID, _ := principal.WorkspaceID(ctx)
		_, err = storekit.Audit(ctx, tx, "update", captureSettingsObject, wsID,
			map[string]any{"own_email_domain": domain}, nil)
		return err
	})
}

// InvalidOwnDomainError names a value that is not a bare domain. The value
// decides whether mail is stored, so a mistyped one is refused with what to do
// about it rather than folded into something that silently matches nothing.
type InvalidOwnDomainError struct{ Reason string }

func (e *InvalidOwnDomainError) Error() string { return "invalid own domain: " + e.Reason }

// Status maps it onto the wire as a 422 (httperr's status-carrying contract).
func (e *InvalidOwnDomainError) Status() int { return http.StatusUnprocessableEntity }

// Code is the stable problem type a client branches on.
func (e *InvalidOwnDomainError) Code() string { return "own_domain_invalid" }

func validOwnDomain(domain string) error {
	switch {
	case domain == "":
		return &InvalidOwnDomainError{Reason: "give a domain, for example acme.com"}
	case strings.ContainsAny(domain, "@/ "):
		return &InvalidOwnDomainError{Reason: "give a bare domain — no address, scheme or path"}
	case !strings.Contains(domain, "."):
		return &InvalidOwnDomainError{Reason: domain + " is not a domain"}
	case len(domain) > 253:
		return &InvalidOwnDomainError{Reason: "that domain is too long"}
	}
	return nil
}
