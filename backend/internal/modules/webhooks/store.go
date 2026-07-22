// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package webhooks is the governed outbound integration surface
// (B-E10.13, S-E10.6/A51): tenant-configured webhook_subscription rows,
// and a delivery worker that fans matching domain events over the event
// bus (events.md §4) to their target URLs as HMAC-SHA256-signed HTTP
// POSTs, retried with exponential backoff and parked in a dead-letter
// store with inspectable replay. It is first-party (subscriptions live in
// the workspace), not a third-party app marketplace and not an inbound
// receiver (features/04 §3). A subscription's signing secret is sealed at
// rest (cipher.go); it is returned exactly once at create/rotate.
package webhooks

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// rbacObject is the RBAC object type governing the fan-out config surface
// (identity policy coreObjects). Managing subscriptions is admin/ops
// integration config; the store gates every entry point on it.
const rbacObject = "webhook_subscription"

// fieldEventTypes is the wire/column name of the subscribed event-type set —
// named once so the validation errors, the audit before/after image, and the
// update patch all spell it identically.
const fieldEventTypes = "event_types"

// Store owns the webhook tables and their write shape. cipher may be nil
// when the deployment configured no signing key: read paths still work
// (metadata never includes the secret), but any path that must seal or
// open a secret returns ErrNotConfigured rather than shipping an
// unsigned or guessable delivery.
type Store struct {
	pool   *pgxpool.Pool
	cipher *Cipher
}

// NewStore wires the webhook tables over a pool; cipher may be nil when no
// deployment signing key is configured (read paths work, secret paths 503).
func NewStore(pool *pgxpool.Pool, cipher *Cipher) *Store {
	return &Store{pool: pool, cipher: cipher}
}

// ErrNotConfigured is returned by paths that need the deployment signing
// key when none was configured — an honest 503, never a silent no-op.
var ErrNotConfigured = errors.New("webhooks: signing key not configured")

// BadInputError maps to a 422 with the offending field.
type BadInputError struct {
	Field  string
	Reason string
}

func (e *BadInputError) Error() string { return e.Field + ": " + e.Reason }

const subscriptionColumns = `id, workspace_id, owner_id, target_url, event_types, state, version, created_at, updated_at, archived_at`

// Subscription is the metadata view of a subscription — the signing
// secret is deliberately absent: it exists on the wire exactly once.
type Subscription struct {
	ID          ids.UUID
	WorkspaceID ids.UUID
	OwnerID     ids.UUID
	TargetURL   string
	EventTypes  []string
	State       string
	Version     int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ArchivedAt  *time.Time
}

func scanSubscription(r pgx.Row) (Subscription, error) {
	var s Subscription
	err := r.Scan(&s.ID, &s.WorkspaceID, &s.OwnerID, &s.TargetURL, &s.EventTypes,
		&s.State, &s.Version, &s.CreatedAt, &s.UpdatedAt, &s.ArchivedAt)
	return s, err
}

// owner resolves the human a subscription is attributed to: the acting
// user, or the human an agent acts on behalf of. A principal with no
// human identity cannot own integration config.
func owner(ctx context.Context) (ids.UUID, error) {
	p, err := storekit.Actor(ctx)
	if err != nil {
		return ids.Nil, err
	}
	switch {
	case !p.UserID.IsZero():
		return p.UserID, nil
	case !p.OnBehalfOf.IsZero():
		return p.OnBehalfOf, nil
	default:
		return ids.Nil, fmt.Errorf("a webhook subscription needs a human owner: %w", apperrors.ErrPermissionDenied)
	}
}

// validateEventTypes rejects a request naming an event type outside the
// published catalog (events.md §5) — the catalog IS the contract, so an
// unknown type is a client error, not a silently-never-delivered rule.
func validateEventTypes(types []string) error {
	if len(types) == 0 {
		return &BadInputError{Field: fieldEventTypes, Reason: "must name at least one event type"}
	}
	catalog := kevents.Types()
	for _, t := range types {
		if !slices.Contains(catalog, t) {
			return &BadInputError{Field: fieldEventTypes, Reason: fmt.Sprintf("%q is not a published event type", t)}
		}
		// A pipeline event (capture.received and its siblings) carries no
		// subject entity, so the owner-scoped fan-out (BYO-EVT-4) has
		// nothing to bound delivery by — these are internal pipeline proofs,
		// not integrator-facing domain facts, and are not subscribable.
		if kevents.IsPipelineEvent(t) {
			return &BadInputError{Field: fieldEventTypes, Reason: fmt.Sprintf("%q is an internal pipeline event and cannot be subscribed to", t)}
		}
	}
	return nil
}

// CreateSubscriptionInput is the create payload; the owner and secret are
// server-derived, never client-supplied.
type CreateSubscriptionInput struct {
	TargetURL  string
	EventTypes []string
}

// CreateSubscription registers a subscription and returns it together
// with the freshly minted signing secret — the ONLY time the plaintext
// leaves the system. The secret is sealed for storage.
func (s *Store) CreateSubscription(ctx context.Context, in CreateSubscriptionInput) (Subscription, string, error) {
	if err := auth.Require(ctx, rbacObject, principal.ActionCreate); err != nil {
		return Subscription{}, "", err
	}
	if s.cipher == nil {
		return Subscription{}, "", ErrNotConfigured
	}
	if !strings.HasPrefix(in.TargetURL, "https://") {
		return Subscription{}, "", &BadInputError{Field: "target_url", Reason: "must be an https:// URL"}
	}
	if err := validateEventTypes(in.EventTypes); err != nil {
		return Subscription{}, "", err
	}
	ownerID, err := owner(ctx)
	if err != nil {
		return Subscription{}, "", err
	}
	secret, err := generateSecret()
	if err != nil {
		return Subscription{}, "", err
	}
	sealed, err := s.cipher.seal(secret)
	if err != nil {
		return Subscription{}, "", err
	}

	var out Subscription
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			INSERT INTO webhook_subscription
			  (workspace_id, owner_id, target_url, event_types, signing_secret_ref, state)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, $4, 'active')
			RETURNING `+subscriptionColumns,
			ownerID, in.TargetURL, in.EventTypes, sealed)
		var err error
		if out, err = scanSubscription(row); err != nil {
			return err
		}
		_, err = storekit.Audit(ctx, tx, "create", rbacObject, out.ID, nil, map[string]any{
			"target_url": out.TargetURL, fieldEventTypes: out.EventTypes,
		})
		return err
	})
	if err != nil {
		return Subscription{}, "", err
	}
	return out, secret, nil
}

// ListSubscriptions returns the workspace's subscriptions (RBAC-read-gated),
// newest first, optionally including archived rows.
func (s *Store) ListSubscriptions(ctx context.Context, archived storekit.ArchivedFilter) ([]Subscription, error) {
	if err := auth.Require(ctx, rbacObject, principal.ActionRead); err != nil {
		return nil, err
	}
	var out []Subscription
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		where := ""
		if archived != storekit.IncludeArchived {
			where = " WHERE archived_at IS NULL"
		}
		rows, err := tx.Query(ctx, "SELECT "+subscriptionColumns+" FROM webhook_subscription"+where+
			" ORDER BY created_at DESC, id DESC")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			sub, err := scanSubscription(rows)
			if err != nil {
				return err
			}
			out = append(out, sub)
		}
		return rows.Err()
	})
	return out, err
}

// GetSubscription returns one live subscription by id; an archived, absent,
// or out-of-workspace row reads as ErrNotFound (existence-hiding).
func (s *Store) GetSubscription(ctx context.Context, id ids.UUID) (Subscription, error) {
	if err := auth.Require(ctx, rbacObject, principal.ActionRead); err != nil {
		return Subscription{}, err
	}
	var out Subscription
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanSubscription(tx.QueryRow(ctx,
			"SELECT "+subscriptionColumns+" FROM webhook_subscription WHERE id = $1 AND archived_at IS NULL", id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// An archived (or absent, or another workspace's) subscription reads
		// as absent — existence-hiding, and delivery stops at archive.
		return Subscription{}, apperrors.ErrNotFound
	}
	return out, err
}

// UpdateSubscriptionInput carries the mutable fields. State toggles the
// pause/resume lifecycle; event_types re-targets the fan-out.
type UpdateSubscriptionInput struct {
	State      *string
	EventTypes *[]string
	IfVersion  *int64
}

// UpdateSubscription pauses/resumes or re-targets a subscription under an
// optimistic-concurrency guard, auditing the before/after image.
func (s *Store) UpdateSubscription(ctx context.Context, id ids.UUID, in UpdateSubscriptionInput) (Subscription, error) {
	if err := auth.Require(ctx, rbacObject, principal.ActionUpdate); err != nil {
		return Subscription{}, err
	}
	if in.State != nil && *in.State != "active" && *in.State != "paused" {
		return Subscription{}, &BadInputError{Field: "state", Reason: "must be active or paused"}
	}
	if in.EventTypes != nil {
		if err := validateEventTypes(*in.EventTypes); err != nil {
			return Subscription{}, err
		}
	}
	var out Subscription
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := scanSubscription(tx.QueryRow(ctx,
			"SELECT "+subscriptionColumns+" FROM webhook_subscription WHERE id = $1 AND archived_at IS NULL", id))
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}
		p := storekit.NewPatch()
		if in.State != nil {
			p.Set("state", current.State, *in.State)
		}
		if in.EventTypes != nil {
			p.Set(fieldEventTypes, current.EventTypes, *in.EventTypes)
		}
		if p.Empty() {
			out = current
			return nil
		}
		if err := p.ApplyGuarded(ctx, tx, "webhook_subscription", id, in.IfVersion); err != nil {
			return err
		}
		if _, err := storekit.Audit(ctx, tx, "update", rbacObject, id, p.Before(), p.After()); err != nil {
			return err
		}
		out, err = scanSubscription(tx.QueryRow(ctx,
			"SELECT "+subscriptionColumns+" FROM webhook_subscription WHERE id = $1", id))
		return err
	})
	return out, err
}

// RotateSecret mints and seals a new signing secret, returning the
// plaintext once. The prior secret stops verifying immediately — a
// receiver must adopt the new one.
func (s *Store) RotateSecret(ctx context.Context, id ids.UUID) (Subscription, string, error) {
	if err := auth.Require(ctx, rbacObject, principal.ActionUpdate); err != nil {
		return Subscription{}, "", err
	}
	if s.cipher == nil {
		return Subscription{}, "", ErrNotConfigured
	}
	secret, err := generateSecret()
	if err != nil {
		return Subscription{}, "", err
	}
	sealed, err := s.cipher.seal(secret)
	if err != nil {
		return Subscription{}, "", err
	}
	var out Subscription
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			"UPDATE webhook_subscription SET signing_secret_ref = $2 WHERE id = $1 AND archived_at IS NULL",
			id, sealed)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return apperrors.ErrNotFound
		}
		// The rotation is audited without recording either secret value.
		if _, err := storekit.Audit(ctx, tx, "update", rbacObject, id, nil,
			map[string]any{"signing_secret_rotated": true}); err != nil {
			return err
		}
		out, err = scanSubscription(tx.QueryRow(ctx,
			"SELECT "+subscriptionColumns+" FROM webhook_subscription WHERE id = $1", id))
		return err
	})
	if err != nil {
		return Subscription{}, "", err
	}
	return out, secret, nil
}

// ArchiveSubscription soft-archives a subscription (delivery stops at
// archive); an already-archived or absent row reads as ErrNotFound.
func (s *Store) ArchiveSubscription(ctx context.Context, id ids.UUID) (Subscription, error) {
	if err := auth.Require(ctx, rbacObject, principal.ActionDelete); err != nil {
		return Subscription{}, err
	}
	var out Subscription
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			"UPDATE webhook_subscription SET archived_at = now() WHERE id = $1 AND archived_at IS NULL RETURNING "+subscriptionColumns,
			id)
		var err error
		if out, err = scanSubscription(row); errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		} else if err != nil {
			return err
		}
		_, err = storekit.Audit(ctx, tx, "archive", rbacObject, id, nil, nil)
		return err
	})
	return out, err
}
