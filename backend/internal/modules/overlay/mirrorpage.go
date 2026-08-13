// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// The mirror's paged read and the cursor it hands back — kept together, and
// apart from mirrorstore.go's ingest and single-row reads, because the page
// bounds, the keyset query and the token encoding are one contract: a change
// to the ordering is a change to what an outstanding cursor means.

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
)

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

// clampListLimit maps a caller's page size onto the bounds above: absent or
// nonsensical takes the default, oversized takes the ceiling. Every paged read
// in this package resolves it here, so a page's size cannot mean one thing on
// the mirror walk and another on the user map.
func clampListLimit(limit int) int {
	switch {
	case limit <= 0:
		return defaultListLimit
	case limit > maxListLimit:
		return maxListLimit
	default:
		return limit
	}
}

const selectVisibleMirrorPageSQL = `
SELECT m.object_class, m.external_id, m.fields, m.updated_at_baseline,
       coalesce(m.owner_external_id, ''), m.sync_state, m.last_synced_at,
       coalesce(m.projection_fingerprint, '')
FROM overlay_mirror m
%s
WHERE m.object_class = $2 AND m.external_id > $3
ORDER BY m.external_id
LIMIT $4`

// List pages mirror rows for one object class in external_id order, a
// stable (if not incumbent-numeric) keyset — the cursor only has to be a
// consistent Margince-side ordering, not replicate HubSpot's own paging
// scheme. Gated by the same mirror_visibility deny-join as Get (design.md
// §4.6); an unmapped ctx principal answers apperrors.ErrNotFound before
// the page query ever runs.
func (s *MirrorStore) List(ctx context.Context, objectClass, cursor string, limit int) ([]Row, string, error) {
	after, err := decodeMirrorCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	limit = clampListLimit(limit)

	var rows []Row
	err = s.db.Tx(ctx, func(tx pgx.Tx) error {
		mirrorUserID, err := resolveActingMirrorUserID(ctx, tx)
		if err != nil {
			return err
		}
		joinClause, args := visibilityJoin(mirrorUserID)
		args = append(args, objectClass, after, limit)
		query := fmt.Sprintf(selectVisibleMirrorPageSQL, joinClause)
		pgRows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("overlay: listing mirror rows for %s: %w", objectClass, err)
		}
		defer pgRows.Close()
		for pgRows.Next() {
			var row Row
			if err := pgRows.Scan(
				&row.ObjectClass, &row.ExternalID, &row.Fields, &row.UpdatedAtBaseline,
				&row.OwnerExternalID, &row.SyncState, &row.LastSyncedAt, &row.ProjectionFingerprint,
			); err != nil {
				return fmt.Errorf("overlay: scanning mirror row for %s: %w", objectClass, err)
			}
			rows = append(rows, row)
		}
		return pgRows.Err()
	})
	if err != nil {
		return nil, "", err
	}

	next := ""
	if len(rows) == limit {
		next = encodeMirrorCursor(rows[len(rows)-1].ExternalID)
	}
	return rows, next, nil
}

// encodeMirrorCursor/decodeMirrorCursor keep the List cursor opaque to
// callers (a client must never construct or edit one by hand) while
// staying a plain external_id underneath — there is no sort/direction
// variance to encode, unlike storekit's general keyset cursor.
func encodeMirrorCursor(externalID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(externalID))
}

// decodeMirrorCursor answers storekit.MalformedCursorError, not a bare wrap:
// the cursor is client-supplied input on every surface that pages this store,
// so a token that does not decode is the caller's mistake and httperr must be
// able to say so (422 malformed_cursor). An opaque wrap falls through to a 500
// and sends an admin looking for an outage that is not there. The base64
// cause is deliberately dropped — it tells the client nothing it can act on
// beyond "the token is not one we minted".
func decodeMirrorCursor(cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", &storekit.MalformedCursorError{}
	}
	return string(raw), nil
}
