// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

import (
	"context"
	"fmt"
	"strings"
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

// Actor-type and action literals that recur across this file (map key,
// switch case, occurrence count 3+ once this file joins fieldhistory.go/
// retention.go) — named once so goconst has one extraction target instead
// of flagging each new occurrence.
const (
	actorTypeAgent     = "agent"
	actorTypeHuman     = "human"
	actorTypeSystem    = "system"
	actorTypeConnector = "connector"
	actionArchive      = "archive"
)

// recordHistoryVerbs renders each audit_log action as a past-tense phrase
// for composeRecordSummary. The set here is reconciled to the CHECK
// constraint's admitted vocabulary (migrations/core/0053_audit_verb_
// vocabulary.up.sql, 25 verbs); TestRecordHistoryVerbsCoverTheAuditCheckVocabulary
// parses that file directly and fails if a future widening lands without a
// matching entry here. An action absent from the map (defensive only — the
// CHECK already closes the set at the DB level) falls back to the raw
// string, never an error: an unrenderable phrase is still honest history.
var recordHistoryVerbs = map[string]string{
	"create":           "created",
	"update":           "updated",
	actionArchive:      "archived",
	"merge":            "merged",
	"promote":          "promoted",
	"restore":          "restored",
	"export":           "exported",
	"erase":            "erased",
	"login":            "logged in",
	"assign":           "assigned",
	"advance_stage":    "advanced the stage of",
	"approve":          "approved",
	"reject":           "rejected",
	"consent_grant":    "granted consent for",
	"consent_withdraw": "withdrew consent for",
	"activity_relink":  "relinked",
	"record_share":     "shared",
	"record_unshare":   "unshared",
	"resolve":          "resolved",
	"demote":           "demoted",
	"import":           "imported",
	"import_undo":      "undid the import of",
	"disqualify":       "disqualified",
	"anonymize":        "anonymized",
	"send_email":       "sent an email for",
}

// RecordHistoryFilter carries the validated query surface of
// (GET /records/{entity_type}/{id}/history).
type RecordHistoryFilter struct {
	EntityType string
	EntityID   ids.UUID
	Cursor     *string
	Limit      *int
}

// RecordHistoryEntry is one audit_log row rendered as a history line —
// the whole-mutation view, one entry per row (field-history's per-field
// projection is the sibling read). Before/After are the row's own field
// images with the entity's mask applied by omission; operational meta
// rides audit_log.evidence, which this read never selects.
type RecordHistoryEntry struct {
	ID                ids.UUID
	ActorType         string
	ActorID           string
	OnBehalfOf        *ids.UUID
	OnBehalfOfName    *string
	Action            string
	OccurredAt        time.Time
	AuthorizationRule *string
	Before            map[string]any
	After             map[string]any
	Summary           string
}

// RecordHistoryPage is one keyset window of the timeline, chronological
// (oldest first — the reading order of a story, unlike field-history's
// newest-first diff feed).
type RecordHistoryPage struct {
	Entries    []RecordHistoryEntry
	NextCursor string
	HasMore    bool
}

// recordAuditRow carries one scanned audit_log row plus its resolved
// display names, ready for the pure entry transform.
type recordAuditRow struct {
	id                ids.UUID
	actorType         string
	actorID           string
	onBehalfOf        *ids.UUID
	action            string
	occurredAt        time.Time
	authorizationRule *string
	before            map[string]any
	after             map[string]any
	actorDisplayName  *string
	onBehalfOfName    *string
}

// recordHistoryEntry renders one audit row as a history entry: mask both
// payload sides by omission (hiding history and live value is one motion),
// then compose the plain-language line. The actor display falls back to
// the raw prefixed actor_id when no app_user resolves — an honest
// identifier, never an invented name; agent/connector/system ids never
// resolve, and their lines render from their own branch (an agent's human
// context is the on-behalf-of authority, nothing else).
func recordHistoryEntry(row recordAuditRow, mask entityFieldMask) RecordHistoryEntry {
	display := row.actorID
	if row.actorDisplayName != nil && *row.actorDisplayName != "" {
		display = *row.actorDisplayName
	}
	return RecordHistoryEntry{
		ID:                row.id,
		ActorType:         row.actorType,
		ActorID:           row.actorID,
		OnBehalfOf:        row.onBehalfOf,
		OnBehalfOfName:    row.onBehalfOfName,
		Action:            row.action,
		OccurredAt:        row.occurredAt,
		AuthorizationRule: row.authorizationRule,
		Before:            applyFieldMask(row.before, mask),
		After:             applyFieldMask(row.after, mask),
		Summary:           composeRecordSummary(row.actorType, display, row.onBehalfOfName, row.action),
	}
}

// ListRecordHistory reads one record's whole-mutation timeline — every
// audit verb, one line per row — inside a single workspace tx. The gate
// stack is ListFieldHistory's, verbatim: a human session, object-level
// read on the entity type, and the row-scope visibility check (out of
// scope reads as not-found, indistinguishable from not-there).
//
// Unlike field-history there is no action allowlist: a merge or export
// line IS the point of this view. Payload honesty is inherited instead —
// meta writers put operation metadata in audit_log.evidence, and this
// read never selects that column.
func ListRecordHistory(ctx context.Context, pool *pgxpool.Pool, f RecordHistoryFilter) (RecordHistoryPage, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.Type != principal.PrincipalHuman {
		return RecordHistoryPage{}, apperrors.ErrPermissionDenied
	}
	if !fieldHistoryEntityTypes[f.EntityType] {
		return RecordHistoryPage{}, fmt.Errorf("record-history entity %q: %w", f.EntityType, apperrors.ErrNotFound)
	}
	if err := auth.Require(ctx, f.EntityType, principal.ActionRead); err != nil {
		return RecordHistoryPage{}, err
	}

	limit := storekit.ClampLimit(f.Limit)
	var cursor storekit.Cursor
	useCursor := false
	if f.Cursor != nil && *f.Cursor != "" {
		c, err := storekit.DecodeCursor(*f.Cursor)
		if err != nil {
			return RecordHistoryPage{}, err
		}
		cursor, useCursor = c, true
	}
	mask := defaultFieldMasks[f.EntityType]

	page := RecordHistoryPage{Entries: []RecordHistoryEntry{}}
	err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		// activity carries no owner_id — it row-scopes through its links,
		// so its visibility check dispatches to EnsureActivityVisible.
		var visErr error
		if f.EntityType == entityTypeActivity {
			visErr = auth.EnsureActivityVisible(ctx, tx, f.EntityID)
		} else {
			visErr = auth.EnsureVisible(ctx, tx, f.EntityType, f.EntityID)
		}
		if visErr != nil {
			return visErr
		}
		boundary, err := latestScrubTombstone(ctx, tx, f.EntityType, f.EntityID)
		if err != nil {
			return err
		}
		rows, err := queryRecordHistoryWindow(ctx, tx, f, boundary, cursor, useCursor, limit+1)
		if err != nil {
			return err
		}
		if len(rows) > limit {
			rows = rows[:limit]
			page.HasMore = true
			page.NextCursor = storekit.EncodeCursor(rows[limit-1].occurredAt, rows[limit-1].id)
		}
		for _, row := range rows {
			page.Entries = append(page.Entries, recordHistoryEntry(row, mask))
		}
		return nil
	})
	if err != nil {
		return RecordHistoryPage{}, err
	}
	return page, nil
}

// queryRecordHistoryWindow fetches one chronological keyset window of the
// record's audit spine, with both display-name joins resolved in SQL. The
// actor join builds the prefixed key FROM app_user ('human:' || id), so a
// non-UUID actor_id (agent:*, connector:*, system) simply resolves to no
// name — never a cast error. RLS carries the workspace scope on both
// tables, exactly like every sibling read.
func queryRecordHistoryWindow(ctx context.Context, tx pgx.Tx, f RecordHistoryFilter,
	boundary scrubBoundary, cursor storekit.Cursor, useCursor bool, fetch int,
) ([]recordAuditRow, error) {
	conds := []string{"a.entity_type = $1", "a.entity_id = $2"}
	args := []any{f.EntityType, f.EntityID}
	if boundary.exists() {
		// Tombstone-INCLUSIVE (>=), where field-history cuts strictly
		// after (>): projecting a tombstone's payload as field diffs would
		// fabricate changes, so that read withholds the row — but here the
		// tombstone renders as its own honest line ("… erased the
		// record"), and its images are empty since the scrub meta rides
		// evidence. Everything strictly older is still the PII the scrub
		// certified gone, and stays withheld.
		conds = append(conds, fmt.Sprintf("(a.occurred_at, a.id) >= ($%d, $%d)", len(args)+1, len(args)+2))
		args = append(args, boundary.occurredAt, boundary.id)
	}
	if useCursor {
		conds = append(conds, fmt.Sprintf("(a.occurred_at, a.id) > ($%d, $%d)", len(args)+1, len(args)+2))
		args = append(args, cursor.CreatedAt, cursor.ID)
	}
	args = append(args, fetch)
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT a.id, a.actor_type, a.actor_id, a.on_behalf_of, a.action, a.occurred_at,
		       a.authorization_rule, a.before, a.after,
		       actor_user.display_name, obo.display_name
		FROM audit_log a
		LEFT JOIN app_user actor_user
		  ON a.actor_type = 'human' AND a.actor_id = 'human:' || actor_user.id::text
		LEFT JOIN app_user obo ON obo.id = a.on_behalf_of
		WHERE %s
		ORDER BY a.occurred_at ASC, a.id ASC
		LIMIT $%d`, strings.Join(conds, " AND "), len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []recordAuditRow
	for rows.Next() {
		var r recordAuditRow
		var beforeJSON, afterJSON []byte
		if err := rows.Scan(&r.id, &r.actorType, &r.actorID, &r.onBehalfOf, &r.action, &r.occurredAt,
			&r.authorizationRule, &beforeJSON, &afterJSON, &r.actorDisplayName, &r.onBehalfOfName); err != nil {
			return nil, err
		}
		if err := unmarshalJSONBMap(beforeJSON, &r.before); err != nil {
			return nil, fmt.Errorf("audit row %s before: %w", r.id, err)
		}
		if err := unmarshalJSONBMap(afterJSON, &r.after); err != nil {
			return nil, fmt.Errorf("audit row %s after: %w", r.id, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// composeRecordSummary renders one audit row as a plain-language sentence,
// the record-history read's `summary` field. It is pure: callers resolve
// actorDisplayName/onBehalfOfName (app_user lookups) before calling in, so
// this stays testable without a database. onBehalfOfName is set only for
// an agent acting under a human's delegated authority (D2's authority
// weaving); an empty string is treated the same as nil — a resolved-but-
// blank name is not authority to report.
func composeRecordSummary(actorType, actorDisplayName string, onBehalfOfName *string, action string) string {
	verb := recordHistoryVerbs[action]
	if verb == "" {
		verb = action
	}
	switch actorType {
	case actorTypeAgent:
		if onBehalfOfName != nil && *onBehalfOfName != "" {
			return fmt.Sprintf("Agent acting for %s %s the record", *onBehalfOfName, verb)
		}
		return fmt.Sprintf("Agent %s the record", verb)
	case actorTypeHuman:
		return fmt.Sprintf("%s %s the record", actorDisplayName, verb)
	case actorTypeSystem:
		return fmt.Sprintf("System %s the record", verb)
	case actorTypeConnector:
		return fmt.Sprintf("Connector %s the record", verb)
	default:
		return fmt.Sprintf("%s %s the record", actorType, verb)
	}
}
