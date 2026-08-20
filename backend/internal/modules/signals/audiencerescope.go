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

// NarrowDerivedForActivity re-scopes the live, workspace-visible derived
// signals whose evidence cites the given activity: to owner-private when the
// limited message's capture owner is known, to archived when nobody could
// answer for it (owner-private with no owner is a lost signal, not a stricter
// one — the CHECK refuses it, and archiving frees the fingerprint for a clean
// re-derivation under the new audience). Answers how many rows moved.
func NarrowDerivedForActivity(ctx context.Context, tx pgx.Tx, activityID ids.UUID, owner *ids.UUID) (int, error) {
	set, verb := `visibility = 'owner', owner_id = $2, updated_at = now(), version = version + 1`, "narrowed_to_owner"
	args := []any{activityID.String(), owner}
	if owner == nil {
		set, verb = `archived_at = now(), updated_at = now(), version = version + 1`, "archived_no_owner"
		args = []any{activityID.String()}
	}
	rows, err := tx.Query(ctx, `
		UPDATE signal
		   SET `+set+`
		 WHERE source_channel = 'derived' AND visibility = 'workspace' AND archived_at IS NULL
		   AND evidence @> jsonb_build_array(jsonb_build_object('source_type', 'activity', 'source_id', $1::text))
		RETURNING id`, args...)
	if err != nil {
		return 0, fmt.Errorf("signals: re-scoping derived signals for a limited activity: %w", err)
	}
	defer rows.Close()
	var moved []ids.UUID
	for rows.Next() {
		var id ids.UUID
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		moved = append(moved, id)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	// The write shape per row. Ratified audit-only (writeshape_test.go): the
	// closed catalog carries signal.detected and signal.resolved, neither of
	// which says "the audience of this summary narrowed", and announcing a
	// narrowing would broadcast the existence of what was just limited.
	for _, id := range moved {
		image := map[string]any{"activity_id": activityID, "rescope": verb}
		if _, err := storekit.Audit(ctx, tx, "update", "signal", id,
			map[string]any{"visibility": "workspace"}, image); err != nil {
			return 0, fmt.Errorf("signals: auditing the re-scope of %s: %w", id, err)
		}
	}
	return len(moved), nil
}
