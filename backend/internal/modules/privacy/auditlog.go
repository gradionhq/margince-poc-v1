// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The audit-log read surface (GET /audit-log): the Settings governance
// view over the append-only audit_log table. Reading the workspace's
// full attributable history deliberately crosses row scope, and it is
// the admin's alone — distinct from the per-record history in
// recordhistory.go, which every member may read on records they can
// see. A caller without that authority gets 403, never a narrowed page
// that would misread as "nothing happened".

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
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
	ID          ids.UUID
	WorkspaceID ids.WorkspaceID
	ActorType   string
	ActorID     string
	// ActorName and OnBehalfOfName are resolved on the read path, not stored:
	// the ledger row keeps the identifier that was true when it was written,
	// and a display name is looked up when somebody reads it. Both are nil
	// when no app_user resolves — a machine actor, or a member whose account
	// is gone while their audit rows remain.
	ActorName         *string
	OnBehalfOfName    *string
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

// ListAuditLog reads the workspace's audit history, newest first.
//
// Human-only: an agent reading the log that records its own governance would
// observe exactly the oversight trail that bounds it. The agent gate refuses
// the route as well, and this is the arm that survives a routing change.
//
// Admin-only, and deliberately NOT "unbounded row scope". This is the
// unrestricted compliance read, distinct from the per-record history every
// member may read on records they can see, and the spec's governance matrix
// reserves it for the admin alone. Row scope is the wrong predicate for it:
// ops and read_only both seed with scope `all`, so an unbounded check handed
// the whole governance trail to two roles the matrix denies — the compliance
// read is oversight OF ops' machine-origin actions and cannot sit with the
// role it oversees.
// auditActivityAlias names the activity an audit row is ABOUT, joined so the
// audience the row's author set can be asked about it. It is deliberately not
// `a` or one of the app_user aliases already in the statement.
const auditActivityAlias = "aud_activity"

// auditActivityJoin reaches the activity an audit row describes, and only for
// rows that describe one. entity_id is a bare uuid across every entity type, so
// the entity_type test is what stops a person's id colliding with an activity's
// and withholding an image that has no audience to answer to.
const auditActivityJoin = `
		LEFT JOIN activity ` + auditActivityAlias + `
		  ON a.entity_type = 'activity' AND ` + auditActivityAlias + `.id = a.entity_id`

// withholdAuditImage blanks the record images on one entry, keeping the row.
//
// The images are REPLACED rather than nulled. A nil image is what a row that
// never carried one looks like, so nulling would give a reader two readings of
// one value — "nothing was recorded" and "you may not see what was recorded" —
// and the compliance question those readers ask is exactly which of the two it
// is. The marker uses the same word the activity read surface already answers
// with, so present-but-withheld has one spelling across the product.
//
// Only before/after are touched. Evidence is context ABOUT the mutation — which
// retention policy fired, which rule admitted it (storekit.go) — not the record
// content the audience governs, and withholding it would blank the governance
// trail the audience limit has no claim over.
func withholdAuditImage(e *AuditEntry) {
	if e.Before != nil {
		e.Before = auditWithheldImage
	}
	if e.After != nil {
		e.After = auditWithheldImage
	}
}

// auditWithheldImage is the stand-in an out-of-audience reader gets. Built once
// from the contract's own enum so a rename of the wire spelling reaches here.
var auditWithheldImage = []byte(`{"content_state":"` + string(crmcontracts.ActivityContentStateWithheld) + `"}`)

func ListAuditLog(ctx context.Context, db *database.DB, f AuditFilter) (AuditPage, error) {
	// Human is spelled out rather than delegated to auth.RequireHuman, which
	// only turns agents away: nothing internal reads this trail, so connector
	// and system principals are refused here too — which is also why
	// RequireAdmin's system bypass below is unreachable.
	actor, ok := principal.Actor(ctx)
	if !ok || actor.Type != principal.PrincipalHuman {
		return AuditPage{}, fmt.Errorf("human-only compliance read: %w", apperrors.ErrPermissionDenied)
	}
	if err := auth.RequireAdmin(ctx); err != nil {
		return AuditPage{}, err
	}

	limit := storekit.ClampLimit(f.Limit)
	where, args, err := buildAuditWhere(f)
	if err != nil {
		return AuditPage{}, err
	}
	// argN is the platform/auth spelling — a clause helper registers a value and
	// gets its ORDINAL back — and arg is the local "$N" spelling this statement
	// already interpolates. One list, two renderings, so a value registered by
	// the audience arm and one registered here cannot land at different offsets.
	argN := func(v any) int {
		args = append(args, v)
		return len(args)
	}
	arg := func(v any) string {
		return "$" + strconv.Itoa(argN(v))
	}

	// The audience an activity's author set is a property of the ROW, and it
	// does NOT yield to row_scope=all — that is the whole point of the limit,
	// and RequireAdmin above is therefore not an answer to it. The audit image
	// carries the subject verbatim (activities.LogActivity writes
	// {kind, subject}; the update delta writes subject in full while body is
	// reduced to presence), so an admin outside the audience reads through this
	// endpoint exactly what the limit exists to withhold.
	//
	// Projected per row rather than filtered, because a compliance trail with
	// holes in it is its own defect: the row, its actor, its action and its
	// timestamp are all still answered, and only the IMAGE is withheld.
	//
	// The arm is rendered against the joined activity, so a non-activity row —
	// and an activity row whose record is gone — has nothing to withhold and
	// reads as before.
	audience, err := auth.ActivityAudienceArm(ctx, auditActivityAlias, argN)
	if err != nil {
		return AuditPage{}, err
	}

	var page AuditPage
	err = db.Tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT a.id, a.workspace_id, a.actor_type, a.actor_id, a.passport_id, a.on_behalf_of,
			        a.action, a.entity_type, a.entity_id, a.before, a.after, a.authorization_rule,
			        a.evidence, a.occurred_at,
			        actor_user.display_name, obo.display_name,
			        (`+auditActivityAlias+`.id IS NULL OR (`+audience+`)) AS content_readable
			 FROM audit_log a`+auditActorNameJoins+auditActivityJoin+`
			 WHERE `+where+`
			 ORDER BY a.occurred_at DESC, a.id DESC
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
			var contentReadable bool
			if err := rows.Scan(&e.ID, &e.WorkspaceID, &e.ActorType, &e.ActorID,
				&passportID, &onBehalfOf, &e.Action, &e.EntityType, &e.EntityID,
				&e.Before, &e.After, &e.AuthorizationRule, &e.Evidence, &e.OccurredAt,
				&e.ActorName, &e.OnBehalfOfName, &contentReadable); err != nil {
				return err
			}
			if !contentReadable {
				withholdAuditImage(&e)
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

// buildAuditWhere renders the filter into a parameterized WHERE clause and
// its ordered args. The keyset predicate matches the newest-first ORDER BY
// the caller appends. It leaves the trailing LIMIT arg to the caller so the
// same $-numbering stays contiguous.
//
// Every column is qualified with the audit row's `a` alias, which is required
// rather than tidy: the read joins app_user twice for the display names, and
// `id` alone would be ambiguous across those relations.
func buildAuditWhere(f AuditFilter) (string, []any, error) {
	where := "TRUE"
	args := []any{}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	if f.Actor != nil {
		where += " AND a.actor_id = " + arg(*f.Actor)
	}
	if f.EntityType != nil {
		where += " AND a.entity_type = " + arg(*f.EntityType)
	}
	if f.EntityID != nil {
		where += " AND a.entity_id = " + arg(*f.EntityID)
	}
	if f.Action != nil {
		where += " AND a.action = " + arg(*f.Action)
	}
	if f.From != nil {
		where += " AND a.occurred_at >= " + arg(*f.From)
	}
	if f.To != nil {
		where += " AND a.occurred_at <= " + arg(*f.To)
	}
	if f.Cursor != nil && *f.Cursor != "" {
		c, err := storekit.DecodeCursor(*f.Cursor)
		if err != nil {
			return "", nil, err
		}
		where += " AND (a.occurred_at, a.id) < (" + arg(c.CreatedAt) + ", " + arg(c.ID) + ")"
	}
	return where, args, nil
}
