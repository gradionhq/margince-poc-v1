// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package signals

// A derived signal summarises correspondence, so when a human LIMITS a message
// after the summary was written, the summary's audience has to follow: a
// workspace-visible signal over a limited email is that email's content, read
// by everyone. The activities module emits activity.updated with the audience
// in changed_fields; the compose consumer resolves the capture owner and calls
// here, inside one transaction, to narrow every derived signal whose evidence
// names the activity.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The audit-image vocabulary of the re-scope, named once for goconst and for
// the reader: which key says what changed, and why.
const (
	rescopeImageArchivedAt = "archived_at"
	rescopeImageReason     = "reason"
	rescopeImageCitedID    = "cited_activity_id"
)

// NarrowDerivedForActivity re-scopes the live, workspace-visible extraction
// signals whose evidence cites the given activity: to owner-private when the
// limited message's capture owner is known, to archived when nobody could
// answer for it (owner-private with no owner is a lost signal, not a stricter
// one — the CHECK refuses it, and archiving frees the fingerprint for a clean
// re-derivation under the new audience). A signal ALREADY narrowed to a
// different owner is archived too: its summary now mixes correspondence two
// different people limited, and no one reader admits both.
//
// The rows are selected by source = 'signal-scan' — the extraction producer's
// own stamp — never by source_channel, which the contract DEFAULTS to
// 'derived' for a human's own POST /signals (migration 0208 records the same
// trap); a person's own filing is theirs, not this corrector's.
func NarrowDerivedForActivity(ctx context.Context, tx pgx.Tx, activityID ids.UUID, owner *ids.UUID) (int, error) {
	cites := `evidence @> jsonb_build_array(jsonb_build_object('source_type', 'activity', 'source_id', $1::text))`
	moved := 0
	apply := func(action, sql string, before, after map[string]any, args ...any) error {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return fmt.Errorf("signals: %s for a limited activity: %w", action, err)
		}
		defer rows.Close()
		var ids_ []ids.UUID
		for rows.Next() {
			var id ids.UUID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids_ = append(ids_, id)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		// The write shape per row, ratified audit-only (writeshape_test.go):
		// the closed catalog carries no verb for a narrowing, and announcing
		// one would broadcast the existence of what was just limited.
		for _, id := range ids_ {
			if _, err := storekit.Audit(ctx, tx, "update", "signal", id, before, after); err != nil {
				return fmt.Errorf("signals: auditing the re-scope of %s: %w", id, err)
			}
		}
		moved += len(ids_)
		return nil
	}

	if owner != nil {
		if err := apply("narrowing extraction signals", `
			UPDATE signal
			   SET visibility = 'owner', owner_id = $2, updated_at = now(), version = version + 1
			 WHERE source = 'signal-scan' AND visibility = 'workspace' AND archived_at IS NULL
			   AND `+cites+`
			RETURNING id`,
			map[string]any{"visibility": "workspace", "owner_id": nil},
			map[string]any{"visibility": "owner", "owner_id": *owner, rescopeImageCitedID: activityID},
			activityID.String(), *owner); err != nil {
			return 0, err
		}
		// Already owner-private to SOMEBODY ELSE and citing this newly limited
		// message: the summary now mixes two people's limited correspondence.
		if err := apply("archiving cross-owner extraction signals", `
			UPDATE signal
			   SET archived_at = now(), updated_at = now(), version = version + 1
			 WHERE source = 'signal-scan' AND visibility = 'owner' AND owner_id <> $2 AND archived_at IS NULL
			   AND `+cites+`
			RETURNING id`,
			map[string]any{rescopeImageArchivedAt: nil},
			map[string]any{"archived": true, rescopeImageCitedID: activityID, rescopeImageReason: "evidence limited to a different owner"},
			activityID.String(), *owner); err != nil {
			return 0, err
		}
		return moved, nil
	}
	if err := apply("archiving ownerless extraction signals", `
		UPDATE signal
		   SET archived_at = now(), updated_at = now(), version = version + 1
		 WHERE source = 'signal-scan' AND archived_at IS NULL
		   AND `+cites+`
		RETURNING id`,
		map[string]any{rescopeImageArchivedAt: nil},
		map[string]any{"archived": true, rescopeImageCitedID: activityID, rescopeImageReason: "no capture owner could answer for the limited evidence"},
		activityID.String()); err != nil {
		return 0, err
	}
	return moved, nil
}
