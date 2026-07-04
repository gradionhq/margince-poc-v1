// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package storekit is the shared store mechanics under every module's
// persistence layer (ADR-0054 §6): the one non-negotiable write shape
// (data-model §11, events.md §4.2 — domain row + audit_log row +
// event_outbox row commit in ONE transaction), keyset pagination,
// optimistic-version patches, and the SQLSTATE branch helpers. Modules
// own their tables and SQL; the invariants live here, spelled once.
package storekit

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// Actor resolves the audit identity of the current call. A missing actor
// is a programming error (the middleware always binds one).
func Actor(ctx context.Context) (principal.Principal, error) {
	p, ok := principal.Actor(ctx)
	if !ok {
		return principal.Principal{}, errors.New("store: no actor bound to context")
	}
	return p, nil
}

// CapturedBy is the server-derived provenance stamp: always the
// authenticated principal, never a client-supplied string (a client that
// could write captured_by could forge the P5 provenance signal).
func CapturedBy(ctx context.Context) (string, error) {
	p, err := Actor(ctx)
	if err != nil {
		return "", err
	}
	return p.ID, nil
}

// Audit writes the append-only audit_log row inside the mutation's
// transaction — atomic with the domain write by construction — and
// returns the row's id so the paired event can carry it as
// trace.audit_log_id (events.md §2).
func Audit(ctx context.Context, tx pgx.Tx, action, entityType string, entityID ids.UUID, before, after any) (ids.UUID, error) {
	p, err := Actor(ctx)
	if err != nil {
		return ids.Nil, err
	}
	wsID, _ := principal.WorkspaceID(ctx)

	beforeJSON, err := marshalOrNil(before)
	if err != nil {
		return ids.Nil, err
	}
	afterJSON, err := marshalOrNil(after)
	if err != nil {
		return ids.Nil, err
	}

	id := ids.NewV7()
	_, err = tx.Exec(ctx,
		`INSERT INTO audit_log (id, workspace_id, actor_type, actor_id, passport_id, on_behalf_of, action, entity_type, entity_id, before, after, authorization_rule)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		id, wsID, string(p.Type), p.ID, UUIDOrNil(p.PassportID), UUIDOrNil(p.OnBehalfOf),
		action, entityType, entityID, beforeJSON, afterJSON,
		auth.AuthzRule(p, entityType, action))
	return id, err
}

// Emit stages a domain event in the transactional outbox (events.md
// §4.2). The envelope is complete at staging time — event_id (UUIDv7),
// actor incl. passport/on-behalf-of, and the trace linking this event to
// its audit row, its request's correlation scope, and (for bus-derived
// writes) the causing event — so the relay ships it verbatim.
func Emit(ctx context.Context, tx pgx.Tx, auditID ids.UUID, eventType, entityType string, entityID ids.UUID, payload any) error {
	p, err := Actor(ctx)
	if err != nil {
		return err
	}
	wsID, _ := principal.WorkspaceID(ctx)
	correlationID, ok := principal.CorrelationID(ctx)
	if !ok {
		// Every write path opens an operation scope (the HTTP middleware,
		// a consumer re-binding its trigger); a missing one is a
		// programming error, caught before the row hits the events.
		return errors.New("store: no correlation id bound to context")
	}

	env := events.Envelope{
		EventID:     ids.NewV7(),
		Type:        eventType,
		Version:     events.VersionOf(eventType),
		WorkspaceID: wsID,
		OccurredAt:  time.Now().UTC(),
		Actor: events.Actor{
			Type:       string(p.Type),
			ID:         p.ID,
			PassportID: UUIDOrNil(p.PassportID),
			OnBehalfOf: UUIDOrNil(p.OnBehalfOf),
		},
		Entity: events.EntityRef{Type: entityType, ID: entityID},
		Trace: events.Trace{
			CorrelationID: correlationID,
			AuditLogID:    auditID,
		},
	}
	if causeID, ok := principal.CausationEvent(ctx); ok {
		env.Trace.CausationID = &causeID
	}
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		env.Payload = raw
	}

	stream, err := events.StreamFor(eventType)
	if err != nil {
		return err
	}
	if err := env.Validate(); err != nil {
		return err
	}

	body, err := json.Marshal(env)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO event_outbox (stream, envelope) VALUES ($1, $2)`,
		stream, body)
	return err
}

// UUIDOrNil maps a zero UUID to SQL NULL / JSON null (the Principal uses
// the zero value for "not an agent action").
func UUIDOrNil(id ids.UUID) *ids.UUID {
	if id.IsZero() {
		return nil
	}
	return &id
}

// MustWorkspace is safe inside a workspace-bound transaction:
// WithWorkspaceTx already failed if no workspace was bound.
func MustWorkspace(ctx context.Context) ids.UUID {
	wsID, _ := principal.WorkspaceID(ctx)
	return wsID
}

// JSONArg marshals a map for a jsonb parameter, passing NULL for nil.
func JSONArg(m map[string]any) any {
	if m == nil {
		return nil
	}
	raw, _ := json.Marshal(m)
	return raw
}

// Page is a keyset-paginated result window.
type Page struct {
	NextCursor string
	HasMore    bool
}

// Cursor is the opaque keyset token: the last row's (created_at, id)
// under the default -created_at,id sort. Keyset, never offset (CAP-PAGE).
type Cursor struct {
	CreatedAt time.Time `json:"t"`
	ID        ids.UUID  `json:"id"`
}

func EncodeCursor(createdAt time.Time, id ids.UUID) string {
	raw, _ := json.Marshal(Cursor{CreatedAt: createdAt, ID: id})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func DecodeCursor(token string) (Cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return Cursor{}, errors.New("store: malformed cursor")
	}
	var c Cursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return Cursor{}, errors.New("store: malformed cursor")
	}
	return c, nil
}

// SQLf keeps store-side SQL assembly lines readable; arguments are
// always positional parameters or fixed identifiers, never user input.
func SQLf(format string, a ...any) string { return fmt.Sprintf(format, a...) }

// ClampLimit applies the contract's CAP-PAGE bounds (default 50, max 200).
func ClampLimit(limit *int) int {
	switch {
	case limit == nil:
		return 50
	case *limit < 1:
		return 1
	case *limit > 200:
		return 200
	default:
		return *limit
	}
}

// Patch accumulates a partial UPDATE: only fields the client sent, plus
// the before/after diff the audit row records.
type Patch struct {
	sets   []string
	args   []any
	before map[string]any
	after  map[string]any
}

func NewPatch() *Patch {
	return &Patch{before: map[string]any{}, after: map[string]any{}}
}

// Set records one changed column. oldVal comes from the row read inside
// the same transaction, so the audit diff is exact.
func (p *Patch) Set(column string, oldVal, newVal any) {
	p.args = append(p.args, newVal)
	p.sets = append(p.sets, fmt.Sprintf("%s = $%d", column, len(p.args)))
	p.before[column] = oldVal
	p.after[column] = newVal
}

func (p *Patch) Empty() bool { return len(p.sets) == 0 }

// Before and After expose the audit diff the accumulated Set calls built.
func (p *Patch) Before() map[string]any { return p.before }
func (p *Patch) After() map[string]any  { return p.after }

// Apply runs the UPDATE with optimistic concurrency: the WHERE clause
// carries the If-Match version when given; zero rows affected on a live
// row means version skew (data-model §1.3a).
func (p *Patch) Apply(ctx context.Context, tx pgx.Tx, table string, id ids.UUID, ifVersion *int64) error {
	p.args = append(p.args, id)
	where := fmt.Sprintf("id = $%d AND archived_at IS NULL", len(p.args))
	if ifVersion != nil {
		p.args = append(p.args, *ifVersion)
		where += fmt.Sprintf(" AND version = $%d", len(p.args))
	}

	tag, err := tx.Exec(ctx,
		fmt.Sprintf(`UPDATE %s SET %s WHERE %s`, table, strings.Join(p.sets, ", "), where),
		p.args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		return nil
	}

	// Distinguish "gone" from "stale": a live row that didn't match can
	// only mean the version clause failed.
	var exists bool
	if err := tx.QueryRow(ctx,
		fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE id = $1 AND archived_at IS NULL)`, table),
		id).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return apperrors.ErrVersionSkew
	}
	return apperrors.ErrNotFound
}

func marshalOrNil(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}

// The SQLSTATEs the stores branch on, named once.
const (
	pgUniqueViolation     = "23505"
	pgForeignKeyViolation = "23503"
	pgCheckViolation      = "23514"
	pgExclusionViolation  = "23P01"
)

// pgViolation names the violated constraint when err is the given
// SQLSTATE class — the single spelling of "which constraint fired".
func pgViolation(err error, code string) (constraint string, ok bool) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == code {
		return pgErr.ConstraintName, true
	}
	return "", false
}

// IsUniqueViolation detects the 23505 dedupe path (409 + existing id).
func IsUniqueViolation(err error) bool {
	_, ok := UniqueViolation(err)
	return ok
}

// UniqueViolation names the violated constraint of a 23505, so callers
// can tell an email/domain dedupe hit from an unrelated uniqueness rule
// (e.g. the one-primary-email index) instead of mislabeling both as
// duplicates.
func UniqueViolation(err error) (constraint string, ok bool) {
	return pgViolation(err, pgUniqueViolation)
}

func IsForeignKeyViolation(err error) bool {
	_, ok := pgViolation(err, pgForeignKeyViolation)
	return ok
}

// ExclusionViolation names a fired EXCLUDE constraint — the overlap
// guards (double-booking) map it to their domain conflict.
func ExclusionViolation(err error) (constraint string, ok bool) {
	return pgViolation(err, pgExclusionViolation)
}

// CheckViolation exposes a fired CHECK constraint's name so the transport
// can answer a typed 422 instead of an opaque 500 — the defense-in-depth
// net under the per-path validations: a CHECK is a business rule, and a
// business-rule breach is never a server fault.
func CheckViolation(err error) (constraint string, ok bool) {
	return pgViolation(err, pgCheckViolation)
}
