// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The workspace's own consumer-mail list (CAP-PARAM-5): what the shipped
// baseline missed, and what it got wrong.
//
// The baseline is a third-party dataset of some 8 700 domains. A list that size
// is right far more often than a hand-typed one and still wrong sometimes, in
// both directions — it misses a regional provider, or it claims a domain an
// operator's real customers mail from. Neither error can wait for a release, and
// both are answerable by the people reading the mail.
//
// Workspace-shared and admin-curated: whether a domain can name a company is a
// statement about the DOMAIN, not about whoever happens to be reading its mail.
// Writes are audit-only (EVT-NOEVT-3, the same ruling the capture-settings
// write holds): the closed event catalog defines no verb for a list entry.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/freemail"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The audit images' field names, spelled once.
const (
	auditKeyDomain = "domain"
	auditKeyKind   = "kind"
)

// The two things a workspace can say about a domain.
const (
	// FreemailKindExtra adds a consumer domain the baseline missed.
	FreemailKindExtra = "extra"
	// FreemailKindNever takes one back out. It wins over everything, because an
	// operator locked out by the baseline has no other way in.
	FreemailKindNever = "never"
)

// freemailDomainObject is the RBAC object gating the list. It rides the
// capture-settings grant: both are workspace-shared capture posture, and an
// admin who may switch auto-enrich on may certainly say that a domain is a
// mailbox provider.
const freemailDomainObject = captureSettingsObject

// ValidFreemailEntry vets one entry and returns the domain in the form the
// matcher keys on — its registrable eTLD+1, derived by the same gate the
// capture path uses. An operator typing "mail.gmx.net"
// means gmx.net, and storing the subdomain would leave an entry that never
// matches anything.
//
// Exported because the handler validates before it writes, so a bad request
// answers 422 with the reason rather than 500 with a constraint violation.
func ValidFreemailEntry(domain, kind string) (string, error) {
	if kind != FreemailKindExtra && kind != FreemailKindNever {
		return "", fmt.Errorf("capture: %q is not a consumer-mail entry kind (extra|never)", kind)
	}
	// The SAME floor the mail path uses. An admin typing "co.uk" as a carve-out
	// would otherwise take every UK domain out of the consumer-mail baseline,
	// and two paths judging one thing must not judge it differently.
	base, ok := freemail.Hostname(domain)
	if !ok {
		return "", fmt.Errorf("capture: %q is not a mail domain", domain)
	}
	return base, nil
}

// FreemailDomain is one list entry.
type FreemailDomain struct {
	ID        ids.UUID
	Domain    string
	Kind      string
	CreatedAt time.Time
}

// FreemailDomainStore owns the workspace's consumer-mail list.
type FreemailDomainStore struct {
	pool *pgxpool.Pool
}

// NewFreemailDomains builds the store over the pool.
func NewFreemailDomains(pool *pgxpool.Pool) *FreemailDomainStore {
	return &FreemailDomainStore{pool: pool}
}

// List returns the workspace's entries, additions before carve-outs and
// alphabetical within each — the order the surface renders and the one a human
// scanning for a domain expects.
func (s *FreemailDomainStore) List(ctx context.Context) ([]FreemailDomain, error) {
	if err := auth.Require(ctx, freemailDomainObject, principal.ActionRead); err != nil {
		return nil, err
	}
	var out []FreemailDomain
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, domain, kind, created_at FROM capture_freemail_domain
			ORDER BY kind, domain`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d FreemailDomain
			if err := rows.Scan(&d.ID, &d.Domain, &d.Kind, &d.CreatedAt); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("capture: reading the consumer-mail list: %w", err)
	}
	return out, nil
}

// Add records one domain as consumer mail, or carves it back out. Idempotent on
// the domain: re-adding returns the existing entry, and switching an entry's
// kind is an update rather than a second row — a domain cannot be both added
// and carved out.
//
// The domain is normalized to its registrable form, which is what the matcher
// keys on: an operator typing "mail.gmx.net" means gmx.net, and storing the
// subdomain would leave an entry that never matches anything.
func (s *FreemailDomainStore) Add(ctx context.Context, domain, kind string) (FreemailDomain, error) {
	if err := auth.Require(ctx, freemailDomainObject, principal.ActionUpdate); err != nil {
		return FreemailDomain{}, err
	}
	// The handler validates first and answers 422; these are the store's own
	// floor, so a non-HTTP caller cannot write a row the matcher will never read.
	base, err := ValidFreemailEntry(domain, kind)
	if err != nil {
		return FreemailDomain{}, err
	}

	var out FreemailDomain
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var before *string
		if err := tx.QueryRow(ctx,
			`SELECT kind FROM capture_freemail_domain WHERE domain = $1`, base).Scan(&before); err != nil &&
			!errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO capture_freemail_domain (workspace_id, domain, kind, created_by)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (workspace_id, domain) DO UPDATE SET kind = EXCLUDED.kind
			RETURNING id, domain, kind, created_at`,
			storekit.MustWorkspace(ctx), base, kind, freemailEntryAuthor(ctx)).
			Scan(&out.ID, &out.Domain, &out.Kind, &out.CreatedAt); err != nil {
			return err
		}
		if before != nil && *before == kind {
			// Nothing changed — no audit row for a re-add that said the same
			// thing, so the trail records decisions rather than clicks.
			return nil
		}
		var beforeImage map[string]any
		if before != nil {
			beforeImage = map[string]any{auditKeyDomain: base, auditKeyKind: *before}
		}
		_, auditErr := storekit.Audit(ctx, tx, "update", freemailDomainObject, out.ID,
			beforeImage, map[string]any{auditKeyDomain: base, auditKeyKind: kind})
		return auditErr
	})
	if err != nil {
		return FreemailDomain{}, fmt.Errorf("capture: recording %s as %s: %w", base, kind, err)
	}
	return out, nil
}

// Remove withdraws one entry, returning the workspace to the shipped baseline's
// answer for that domain. Idempotent: removing what is not there is a no-op,
// not a 404 — the caller's intent is already satisfied.
func (s *FreemailDomainStore) Remove(ctx context.Context, id ids.UUID) error {
	if err := auth.Require(ctx, freemailDomainObject, principal.ActionUpdate); err != nil {
		return err
	}
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var domain, kind string
		err := tx.QueryRow(ctx,
			`DELETE FROM capture_freemail_domain WHERE id = $1 RETURNING domain, kind`, id).Scan(&domain, &kind)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		// 'archive', not 'delete': the audit vocabulary is closed (0018) and
		// carries no delete verb, and withdrawing a list entry IS retiring it.
		_, auditErr := storekit.Audit(ctx, tx, "archive", freemailDomainObject, id,
			map[string]any{auditKeyDomain: domain, auditKeyKind: kind}, nil)
		return auditErr
	})
	if err != nil {
		return fmt.Errorf("capture: withdrawing a consumer-mail entry: %w", err)
	}
	return nil
}

// freemailEntryAuthor is the human an entry is attributed to, NULL for a system
// actor. The list is workspace-shared, so this is provenance for the audit
// trail and never a scope.
func freemailEntryAuthor(ctx context.Context) *ids.UUID {
	if author := actorUserID(ctx); !author.IsZero() {
		return &author
	}
	return nil
}

// MatcherTx builds the consumer-mail matcher this workspace's mail is judged
// by: the shipped baseline plus its own additions, minus its own carve-outs.
// Every path that asks "can this domain name a company?" takes its answer from
// here, so the list an admin edits is the list the capture pipeline obeys with
// no delay and no cache to go stale.
//
// It reads on the CALLER's transaction rather than opening its own. Both askers
// — the capture sink's tier ladder and the counterparty ensure — are already
// inside one, and borrowing a second pool connection while holding one is how a
// pool deadlocks under load. The table holds a handful of rows behind a unique
// index, so the read costs less than the correspondence check already run for
// every captured message.
func MatcherTx(ctx context.Context, tx pgx.Tx) (*freemail.Matcher, error) {
	rows, err := tx.Query(ctx, `SELECT domain, kind FROM capture_freemail_domain`)
	if err != nil {
		return nil, fmt.Errorf("capture: reading the workspace consumer-mail list: %w", err)
	}
	defer rows.Close()
	var extra, never []string
	for rows.Next() {
		var domain, kind string
		if err := rows.Scan(&domain, &kind); err != nil {
			return nil, fmt.Errorf("capture: scanning a consumer-mail entry: %w", err)
		}
		if kind == FreemailKindNever {
			never = append(never, domain)
			continue
		}
		extra = append(extra, domain)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("capture: reading the workspace consumer-mail list: %w", err)
	}
	return freemail.New(extra, never), nil
}
