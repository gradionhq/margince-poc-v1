// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// ResetData implements (POST /admin/reset-data). The contract wiring
// (crm.yaml op + generated types) lands ahead of the wipe-and-reseed
// logic; until that logic is wired here the operation is honestly
// absent — the same "declared or absent, never a silent default"
// posture used elsewhere in this module (see RequestPasswordReset in
// reset.go) — rather than silently succeeding or 404ing.

import (
	"net/http"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// ResetData implements (POST /admin/reset-data). See the file comment: the
// wipe-and-reseed logic is not yet wired, so this answers 501 rather than
// silently succeeding or 404ing.
func (h Handlers) ResetData(w http.ResponseWriter, r *http.Request) {
	httperr.NotImplemented(w, r, "ResetData")
}
