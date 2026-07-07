// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The audit-log read surface (GET /audit-log): the Settings governance
// view over the append-only audit_log table. Reading the workspace's
// full attributable history deliberately crosses row scope — like SAR
// assembly, it is admitted only to an unbounded (admin/compliance)
// principal; a bounded caller gets 403, never a narrowed page that
// would misread as "nothing happened".

import (
	"context"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// AuditFilter narrows the audit page. The actor filter matches the
// stored typed principal id (`human:<uuid>`, `agent:<passport>`,
// `system:*`) verbatim — the column already carries the typed spelling.
type AuditFilter struct {
	Actor      *string
	EntityType *string
	// EntityID stays ids.UUID: it filters the audit envelope's polymorphic
	// (entity_type, entity_id) pair, which addresses any entity kind.
	EntityID *ids.UUID
	Action   *string
	From     *time.Time
	To       *time.Time
	Cursor   *string
	Limit    *int
}

// AuditEntry mirrors one audit_log row (contract AuditLogEntry). ID
// stays ids.UUID — the audit row is a ledger line, not a first-class
// entity — and EntityID stays untyped as the envelope's polymorphic
// target; the concrete workspace/passport/on-behalf ids type cleanly.
type AuditEntry struct {
	ID                ids.UUID
	WorkspaceID       ids.WorkspaceID
	ActorType         string
	ActorID           string
	PassportID        *ids.PassportID
	OnBehalfOf        *ids.UserID
	Action            string
	EntityType        string
	EntityID          *ids.UUID
	Before            []byte
	After             []byte
	AuthorizationRule *string
	Evidence          []byte
	OccurredAt        time.Time
}

// AuditPage is one newest-first keyset page.
type AuditPage struct {
	Entries    []AuditEntry
	NextCursor string
	HasMore    bool
}

// ListAuditLog reads the workspace's audit history, newest first. The
// surface is x-agent-access: human-only AND unbounded: the agent gate
// only fronts mutating routes, so the human check lives here — an agent
// reading the log that records its own governance would let it observe
// exactly the oversight trail that bounds it.
func ListAuditLog(ctx context.Context, pool *pgxpool.Pool, f AuditFilter) (AuditPage, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.Type != principal.PrincipalHuman || !auth.Unbounded(actor) {
		return AuditPage{}, apperrors.ErrPermissionDenied
	}

	limit := storekit.ClampLimit(f.Limit)
	where := "TRUE"
	args := []any{}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	if f.Actor != nil {
		where += " AND actor_id = " + arg(*f.Actor)
	}
	if f.EntityType != nil {
		where += " AND entity_type = " + arg(*f.EntityType)
	}
	if f.EntityID != nil {
		where += " AND entity_id = " + arg(*f.EntityID)
	}
	if f.Action != nil {
		where += " AND action = " + arg(*f.Action)
	}
	if f.From != nil {
		where += " AND occurred_at >= " + arg(*f.From)
	}
	if f.To != nil {
		where += " AND occurred_at <= " + arg(*f.To)
	}
	if f.Cursor != nil && *f.Cursor != "" {
		c, err := storekit.DecodeCursor(*f.Cursor)
		if err != nil {
			return AuditPage{}, err
		}
		where += " AND (occurred_at, id) < (" + arg(c.CreatedAt) + ", " + arg(c.ID) + ")"
	}

	var page AuditPage
	err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id, workspace_id, actor_type, actor_id, passport_id, on_behalf_of,
			        action, entity_type, entity_id, before, after, authorization_rule,
			        evidence, occurred_at
			 FROM audit_log WHERE `+where+`
			 ORDER BY occurred_at DESC, id DESC
			 LIMIT `+arg(limit+1), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e AuditEntry
			// The nullable envelope ids scan through untyped locals, then
			// widen to their kind — a NULL column stays a nil typed pointer.
			var passportID, onBehalfOf *ids.UUID
			if err := rows.Scan(&e.ID, &e.WorkspaceID, &e.ActorType, &e.ActorID,
				&passportID, &onBehalfOf, &e.Action, &e.EntityType, &e.EntityID,
				&e.Before, &e.After, &e.AuthorizationRule, &e.Evidence, &e.OccurredAt); err != nil {
				return err
			}
			if passportID != nil {
				v := ids.From[ids.PassportKind](*passportID)
				e.PassportID = &v
			}
			if onBehalfOf != nil {
				v := ids.From[ids.UserKind](*onBehalfOf)
				e.OnBehalfOf = &v
			}
			page.Entries = append(page.Entries, e)
		}
		return rows.Err()
	})
	if err != nil {
		return AuditPage{}, err
	}

	if len(page.Entries) > limit {
		page.Entries = page.Entries[:limit]
		last := page.Entries[len(page.Entries)-1]
		page.NextCursor = storekit.EncodeCursor(last.OccurredAt, last.ID)
		page.HasMore = true
	}
	return page, nil
}
