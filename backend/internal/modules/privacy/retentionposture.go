// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The retain-only posture's own read/write surface (GCS-WIRE-5, GCS-PARAM-6),
// shaped exactly like capture's settings store: the module owns the sparse-patch
// semantics and the RBAC gate on the write, platform/settings owns the row, the
// validation and the audit entry.
//
// The evaluator and the policy store do NOT come through here — they read the
// posture with settings.GetTx inside a transaction they already hold, because a
// pass that read it in a second transaction could act on some records under one
// posture and the rest under another.

import (
	"context"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/settings"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// PostureStore reads and writes the installation's retain-only posture.
type PostureStore struct {
	settings *settings.Store
}

// NewPostureStore wires the posture over the assembled settings catalog.
func NewPostureStore(store *settings.Store) *PostureStore {
	return &PostureStore{settings: store}
}

// Posture reads whether the installation destroys nothing.
func (s *PostureStore) Posture(ctx context.Context) (bool, error) {
	retainOnly, err := settings.Get(ctx, s.settings, RetainOnly)
	if err != nil {
		return false, fmt.Errorf("privacy: reading the retention posture: %w", err)
	}
	return retainOnly, nil
}

// SetPosture applies a sparse posture patch. A nil value is a patch that names
// no field: it changes nothing and answers with the current posture, so an
// idempotent retry is not an error. Returns the posture after the write.
func (s *PostureStore) SetPosture(ctx context.Context, retainOnly *bool) (bool, error) {
	// Gated here as well as inside settings.Set, and deliberately: the nil
	// branch below never reaches that write, so without this an update-shaped
	// request would pass on the READ grant alone.
	if err := auth.Require(ctx, retentionPolicyObject, principal.ActionUpdate); err != nil {
		return false, err
	}
	if retainOnly == nil {
		return s.Posture(ctx)
	}
	if err := settings.Set(ctx, s.settings, RetainOnly, *retainOnly); err != nil {
		return false, err
	}
	return *retainOnly, nil
}
