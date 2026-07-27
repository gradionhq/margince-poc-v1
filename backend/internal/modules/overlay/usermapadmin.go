// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

import (
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// auditEntityUserMap is the audit_log.entity_type every mirror_user_map
// mutation is recorded under. mirror_user_map has no id column, so the audit
// keys on the mapping's subject — the app_user it governs.
const auditEntityUserMap = "mirror_user_map"

// userMapImage is the before/after field image an audited mapping change
// records. It carries the row's OWN fields only: operation metadata folded in
// here would make downstream field-history projections read it as field
// changes that never happened on the record.
type userMapImage struct {
	IncumbentUserID string `json:"incumbent_user_id"`
	MatchSource     string `json:"match_source"`
}

// revokedMapping is one mirror_user_map row an automated revoke deleted: the
// app_user the audit keys on, plus the image that vanished with it. Both
// revoke paths (a stale email in emailrevalidate.go, an email that turned
// ambiguous in usermapseed.go) delete a SET of rows, so each needs the
// per-row identity a bare RowsAffected count cannot give — an admin asking
// why a user lost access needs to know which mapping went, not how many did.
type revokedMapping struct {
	appUser ids.UserID
	image   userMapImage
}

// collectRevokedMapping reads one `DELETE … RETURNING app_user_id,
// incumbent_user_id, match_source` row, for pgx.CollectRows.
func collectRevokedMapping(row pgx.CollectableRow) (revokedMapping, error) {
	var r revokedMapping
	if err := row.Scan(&r.appUser, &r.image.IncumbentUserID, &r.image.MatchSource); err != nil {
		return revokedMapping{}, fmt.Errorf("overlay: scanning a revoked mirror_user_map row: %w", err)
	}
	return r, nil
}
