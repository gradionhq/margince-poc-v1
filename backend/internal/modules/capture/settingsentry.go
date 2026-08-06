// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The capture module's own settings declarations (ADR-0090/A135). Capture
// owns these: it supplies the type, default, validator, RBAC object and audit
// verb, and platform/settings runs them without knowing what enrichment is.
//
// AutoEnrich moved here from a `capture_auto_enrich` column on the workspace
// row (0121). The governance is deliberately unchanged — same
// `capture_settings` object, same audit-only posture (EVT-NOEVT-3: the closed
// event catalog defines no capture-settings verb) — because this is a change
// of where the value lives, not of who may change it.

import (
	"github.com/gradionhq/margince/backend/internal/platform/settings"
)

// captureSettingsObject is the RBAC object gating the capture-settings
// surface: every role reads it (a rep needs to see whether auto-enrich is
// live), only admin/ops writes.
const captureSettingsObject = "capture_settings"

// AutoEnrich is the captured-organization auto-enrich posture (CAP-PARAM-7,
// ADR-0072/A118).
//
// Default true preserves 0121's column default, so an installation that never
// touched the toggle sees no behaviour change across the move.
//
// One thing the move DID change: the audit row's before/after field is now
// keyed `capture.auto_enrich` where the column form wrote `auto_enrich`. The
// verb, object and actor are unchanged, but an operator reading the ledger
// across the migration boundary sees both spellings.
var AutoEnrich = settings.Define[bool](
	"capture.auto_enrich",
	captureSettingsObject,
	"update",
	true,
	nil, // every bool is a valid posture; a validator here would be ceremony
)

// Definitions is capture's contribution to the settings registry. Compose
// concatenates each module's list; a module that declares no settings has no
// such function, so this is opt-in rather than an interface every module must
// satisfy.
func Definitions() []settings.Definition {
	return []settings.Definition{AutoEnrich}
}
