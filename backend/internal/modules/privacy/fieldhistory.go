// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
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

// FieldHistoryFilter carries the validated query surface of
// (GET /field-history). EntityType and EntityID are required; the rest
// narrow the projection.
type FieldHistoryFilter struct {
	EntityType string
	EntityID   ids.UUID
	Field      *string
	ActorType  *string
	Cursor     *string
	Limit      *int
}

// FieldHistoryEntry is one per-field change projected from a single
// audit_log row's before/after diff — not a stored history row. ID is
// the source audit row's id, so entries from one mutation share it.
type FieldHistoryEntry struct {
	ID         ids.UUID
	EntityType string
	EntityID   ids.UUID
	Field      string
	OldValue   *string
	NewValue   *string
	ChangedAt  time.Time
	ActorType  string
	ActorID    string
	PassportID *ids.UUID
	Evidence   map[string]any
}

// FieldHistoryPage is one keyset window of the timeline, newest first.
type FieldHistoryPage struct {
	Entries    []FieldHistoryEntry
	NextCursor string
	HasMore    bool
}

// entityTypeActivity names the one record kind whose row-scope check
// dispatches differently below (activity carries no owner_id, so its
// visibility rides the link-walk) — named once so the map literal and
// the dispatch both read from the same word.
const entityTypeActivity = "activity"

// The record kinds whose field history is readable — the audit spine's
// entity_type is free text, so the surface pins the vocabulary.
var fieldHistoryEntityTypes = map[string]bool{
	"person": true, "organization": true, "deal": true, "lead": true, entityTypeActivity: true,
}

var fieldHistoryActorTypes = map[string]bool{
	"human": true, "agent": true, "system": true, "connector": true,
}

// entityFieldMask names fields whose history is withheld for an entity
// type, exactly as the live value would be withheld — hiding history and
// value is one motion, never two mechanisms. Empty until field-level
// masking ships; the transform applies it to both sides before diffing
// so a masked field can never leak through an old_value.
type entityFieldMask map[string]struct{}

var defaultFieldMasks = map[string]entityFieldMask{}

// auditDiffRow carries the columns of one audit_log row the diff needs.
type auditDiffRow struct {
	id         ids.UUID
	entityType string
	entityID   ids.UUID
	actorType  string
	actorID    string
	passportID *ids.UUID
	evidence   map[string]any
	occurredAt time.Time
	before     map[string]any
	after      map[string]any
}

// diffAuditRowFields projects one audit row into per-field entries:
// changed or added keys emit old->new, removed keys emit old->nil, and
// keys equal on both sides emit nothing — an empty history is honest,
// never fabricated. Keys emit alphabetically so a row's entries are
// deterministic. passport/evidence surface only for agent actors.
func diffAuditRowFields(row auditDiffRow, mask entityFieldMask, fieldFilter *string) []FieldHistoryEntry {
	before := applyFieldMask(row.before, mask)
	after := applyFieldMask(row.after, mask)

	keyset := make(map[string]struct{}, len(before)+len(after))
	for k := range before {
		keyset[k] = struct{}{}
	}
	for k := range after {
		keyset[k] = struct{}{}
	}
	keys := make([]string, 0, len(keyset))
	for k := range keyset {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var entries []FieldHistoryEntry
	for _, key := range keys {
		if fieldFilter != nil && key != *fieldFilter {
			continue
		}
		beforeVal, inBefore := before[key]
		afterVal, inAfter := after[key]
		switch {
		case inAfter && (!inBefore || !reflect.DeepEqual(beforeVal, afterVal)):
			entries = append(entries, makeFieldHistoryEntry(row, key, stringifyFieldValue(beforeVal), stringifyFieldValue(afterVal)))
		case inBefore && !inAfter:
			entries = append(entries, makeFieldHistoryEntry(row, key, stringifyFieldValue(beforeVal), nil))
		}
	}
	return entries
}

func makeFieldHistoryEntry(row auditDiffRow, field string, oldValue, newValue *string) FieldHistoryEntry {
	var passportID *ids.UUID
	var evidence map[string]any
	if row.actorType == "agent" {
		passportID = row.passportID
		evidence = row.evidence
	}
	return FieldHistoryEntry{
		ID:         row.id,
		EntityType: row.entityType,
		EntityID:   row.entityID,
		Field:      field,
		OldValue:   oldValue,
		NewValue:   newValue,
		ChangedAt:  row.occurredAt,
		ActorType:  row.actorType,
		ActorID:    row.actorID,
		PassportID: passportID,
		Evidence:   evidence,
	}
}

// stringifyFieldValue renders a diff side for display. A nil (JSON null
// or absent) value stays a nil pointer — the client renders the
// empty/created origin label, never a literal "nil".
func stringifyFieldValue(v any) *string {
	if v == nil {
		return nil
	}
	s := fmt.Sprintf("%v", v)
	return &s
}

func applyFieldMask(data map[string]any, mask entityFieldMask) map[string]any {
	if data == nil || len(mask) == 0 {
		return data
	}
	out := make(map[string]any, len(data))
	for k, v := range data {
		if _, hidden := mask[k]; !hidden {
			out[k] = v
		}
	}
	return out
}

const (
	fieldHistoryScanBatch   = 100
	fieldHistoryMaxScanRows = 2000
)

// ListFieldHistory reads one record's per-field change timeline,
// projected inside a single workspace tx from the audit spine. The gate
// is threefold: a human session (the agent gate only fronts mutating
// routes, so the human-only rule binds here), object-level read on the
// entity type, and the row-scope visibility check — out of scope reads
// as not-found, indistinguishable from not-there.
//
// The page limit counts ENTRIES, but one audit row can yield several;
// a row's entries never split across pages, so a page may overflow the
// limit by the tail row's width. When a page fills exactly on a row
// boundary, a cheap existence probe decides has_more — the row that
// filled the page may have been the true last one.
func ListFieldHistory(ctx context.Context, pool *pgxpool.Pool, f FieldHistoryFilter) (FieldHistoryPage, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.Type != principal.PrincipalHuman {
		return FieldHistoryPage{}, apperrors.ErrPermissionDenied
	}
	if !fieldHistoryEntityTypes[f.EntityType] {
		return FieldHistoryPage{}, fmt.Errorf("field-history entity %q: %w", f.EntityType, apperrors.ErrNotFound)
	}
	if err := auth.Require(ctx, f.EntityType, principal.ActionRead); err != nil {
		return FieldHistoryPage{}, err
	}

	limit := storekit.ClampLimit(f.Limit)
	var cursorTime time.Time
	var cursorID ids.UUID
	useCursor := false
	if f.Cursor != nil && *f.Cursor != "" {
		c, err := storekit.DecodeCursor(*f.Cursor)
		if err != nil {
			return FieldHistoryPage{}, err
		}
		cursorTime, cursorID, useCursor = c.CreatedAt, c.ID, true
	}
	mask := defaultFieldMasks[f.EntityType]

	var page FieldHistoryPage
	err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		// activity carries no owner_id — it row-scopes through its
		// links (the entities it is attached to), so its visibility
		// check dispatches to EnsureActivityVisible; every other entity
		// type in fieldHistoryEntityTypes is owner-scoped and goes
		// through the generic EnsureVisible.
		var visErr error
		if f.EntityType == entityTypeActivity {
			visErr = auth.EnsureActivityVisible(ctx, tx, f.EntityID)
		} else {
			visErr = auth.EnsureVisible(ctx, tx, f.EntityType, f.EntityID)
		}
		if visErr != nil {
			return visErr
		}
		var scanErr error
		page, scanErr = scanFieldHistorySpine(ctx, tx, f, mask, limit, cursorTime, cursorID, useCursor)
		return scanErr
	})
	if err != nil {
		return FieldHistoryPage{}, err
	}
	if page.Entries == nil {
		page.Entries = []FieldHistoryEntry{}
	}
	return page, nil
}

// scanFieldHistorySpine walks the audit spine in batches from the given
// cursor position, newest-first, accumulating diffed entries up to limit.
// It runs entirely inside the caller's workspace tx (the visibility check
// already passed), and is the one place that decides has_more: the scan
// cap, the limit, and true spine exhaustion are three distinct reasons to
// stop, and only the first two ever set a next cursor.
func scanFieldHistorySpine(ctx context.Context, tx pgx.Tx, f FieldHistoryFilter, mask entityFieldMask,
	limit int, cursorTime time.Time, cursorID ids.UUID, useCursor bool,
) (FieldHistoryPage, error) {
	var page FieldHistoryPage
	scanned := 0
	for {
		rows, batch, err := queryFieldHistoryBatch(ctx, tx, f, cursorTime, cursorID, useCursor)
		if err != nil {
			return FieldHistoryPage{}, err
		}
		var batchScanned int
		page.Entries, cursorTime, cursorID, batchScanned = scanFieldHistoryBatch(page.Entries, rows, f, mask, limit, cursorTime, cursorID)
		if batchScanned > 0 {
			useCursor = true
		}
		scanned += batchScanned
		switch {
		case scanned >= fieldHistoryMaxScanRows:
			// The scan cap keeps a filter that skips most rows from
			// walking the whole spine in one call; more MAY match, and
			// claiming so is the honest side to err on.
			page.NextCursor = storekit.EncodeCursor(cursorTime, cursorID)
			page.HasMore = true
			return page, nil
		case len(page.Entries) >= limit:
			more, err := hasFollowingAuditRow(ctx, tx, f, cursorTime, cursorID)
			if err != nil {
				return FieldHistoryPage{}, err
			}
			if more {
				page.NextCursor = storekit.EncodeCursor(cursorTime, cursorID)
				page.HasMore = true
			}
			return page, nil
		case batch < fieldHistoryScanBatch:
			return page, nil // spine exhausted
		}
	}
}

// scanFieldHistoryBatch diffs and appends one fetched batch's rows into the
// accumulating entry list, newest first, advancing the cursor to the last
// row scanned so a following batch (or a next-page cursor) resumes exactly
// there. It stops early once the entry limit is hit — a row's own diff
// entries never split across the return and a following page — and leaves
// cursorTime/cursorID untouched when the batch is empty, so an exhausted
// scan can never clobber a caller's valid cursor.
func scanFieldHistoryBatch(entries []FieldHistoryEntry, rows []auditDiffRow, f FieldHistoryFilter,
	mask entityFieldMask, limit int, cursorTime time.Time, cursorID ids.UUID,
) ([]FieldHistoryEntry, time.Time, ids.UUID, int) {
	scanned := 0
	for _, row := range rows {
		scanned++
		cursorTime, cursorID = row.occurredAt, row.id
		if f.ActorType != nil && row.actorType != *f.ActorType {
			continue
		}
		entries = append(entries, diffAuditRowFields(row, mask, f.Field)...)
		if len(entries) >= limit {
			break
		}
	}
	return entries, cursorTime, cursorID, scanned
}

// queryFieldHistoryBatch fetches the next window of audit rows for the
// record, newest first, decoding the jsonb sides eagerly so a corrupt
// payload surfaces as an error, never as a silently empty diff.
func queryFieldHistoryBatch(ctx context.Context, tx pgx.Tx, f FieldHistoryFilter,
	cursorTime time.Time, cursorID ids.UUID, useCursor bool,
) ([]auditDiffRow, int, error) {
	var (
		rows pgx.Rows
		err  error
	)
	if useCursor {
		rows, err = tx.Query(ctx, `
			SELECT id, actor_type, actor_id, passport_id, evidence, occurred_at, before, after
			FROM audit_log
			WHERE entity_type = $1 AND entity_id = $2
			  AND (occurred_at, id) < ($3, $4)
			ORDER BY occurred_at DESC, id DESC
			LIMIT $5`,
			f.EntityType, f.EntityID, cursorTime, cursorID, fieldHistoryScanBatch)
	} else {
		rows, err = tx.Query(ctx, `
			SELECT id, actor_type, actor_id, passport_id, evidence, occurred_at, before, after
			FROM audit_log
			WHERE entity_type = $1 AND entity_id = $2
			ORDER BY occurred_at DESC, id DESC
			LIMIT $3`,
			f.EntityType, f.EntityID, fieldHistoryScanBatch)
	}
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []auditDiffRow
	for rows.Next() {
		var r auditDiffRow
		var evidenceJSON, beforeJSON, afterJSON []byte
		if err := rows.Scan(&r.id, &r.actorType, &r.actorID, &r.passportID,
			&evidenceJSON, &r.occurredAt, &beforeJSON, &afterJSON); err != nil {
			return nil, 0, err
		}
		r.entityType, r.entityID = f.EntityType, f.EntityID
		if err := unmarshalJSONBMap(evidenceJSON, &r.evidence); err != nil {
			return nil, 0, fmt.Errorf("audit row %s evidence: %w", r.id, err)
		}
		if err := unmarshalJSONBMap(beforeJSON, &r.before); err != nil {
			return nil, 0, fmt.Errorf("audit row %s before: %w", r.id, err)
		}
		if err := unmarshalJSONBMap(afterJSON, &r.after); err != nil {
			return nil, 0, fmt.Errorf("audit row %s after: %w", r.id, err)
		}
		out = append(out, r)
	}
	return out, len(out), rows.Err()
}

func unmarshalJSONBMap(raw []byte, dst *map[string]any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dst)
}

// hasFollowingAuditRow answers whether any audit row for the record
// precedes the cursor position. It deliberately ignores the actor
// filter: the rare cost is one extra empty page, never a false "done".
func hasFollowingAuditRow(ctx context.Context, tx pgx.Tx, f FieldHistoryFilter,
	cursorTime time.Time, cursorID ids.UUID,
) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM audit_log
			WHERE entity_type = $1 AND entity_id = $2
			  AND (occurred_at, id) < ($3, $4))`,
		f.EntityType, f.EntityID, cursorTime, cursorID).Scan(&exists)
	return exists, err
}
