// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The workspace capture settings surface (CAP-PARAM-7, ADR-0072/A118). The
// value lives in the `setting` table under capture's own key (ADR-0090/A135);
// this file is the module-facing shape over it. RBAC, the audit-only posture
// and the idempotent-PATCH semantics are unchanged from the column form — the
// store below no longer owns any of them, because the settings mechanism
// carries them from the entry declaration in settingsentry.go.

import (
	"context"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/settings"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// Settings is the workspace-shared capture posture (the wire shape).
type Settings struct {
	AutoEnrich bool
}

// SettingsStore is the store over the workspace capture posture.
type SettingsStore struct {
	settings *settings.Store
}

// NewSettings builds the capture-settings store over the settings mechanism.
func NewSettings(s *settings.Store) *SettingsStore { return &SettingsStore{settings: s} }

// Get reads the workspace's capture settings. The read gate lives on the
// entry (`capture_settings`, read granted to every role), so there is no
// second gate to keep in step here.
func (s *SettingsStore) Get(ctx context.Context) (Settings, error) {
	on, err := settings.Get(ctx, s.settings, AutoEnrich)
	if err != nil {
		return Settings{}, fmt.Errorf("capture: reading settings: %w", err)
	}
	return Settings{AutoEnrich: on}, nil
}

// Update applies a sparse capture-settings patch (admin/ops). A nil field is
// left unchanged; an unchanged value writes nothing and audits nothing.
// Returns the settings after the write.
//
// The update gate is taken HERE, before the empty-patch branch, not left to
// the write that may never happen. An empty PATCH is still an attempt to
// change settings, and answering it from the read gate alone would let a
// read-only role probe the surface with a 200 where the column form gave a
// 403.
//
// The mirror case is real but not reachable: a caller holding update WITHOUT
// read gets a 403 on an empty patch, because the response comes from Get. No
// seeded role has that combination — `capture_settings` grants read to every
// role — so this stays a note rather than a branch.
func (s *SettingsStore) Update(ctx context.Context, autoEnrich *bool) (Settings, error) {
	if err := auth.Require(ctx, captureSettingsObject, principal.ActionUpdate); err != nil {
		return Settings{}, err
	}
	if autoEnrich == nil {
		return s.Get(ctx)
	}
	if err := settings.Set(ctx, s.settings, AutoEnrich, *autoEnrich); err != nil {
		return Settings{}, err
	}
	return Settings{AutoEnrich: *autoEnrich}, nil
}
