// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

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
