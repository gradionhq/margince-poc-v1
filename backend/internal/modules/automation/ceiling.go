// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

import (
	"context"
	"fmt"
	"strings"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// requireAuthorCeiling fast-fails authoring when the author lacks the
// permission the automation's action needs. It is a UX convenience, not
// the security boundary: a firing runs long after authoring, and the
// author's authority can be revoked in between — gate.go's match-time
// gate is the boundary that re-checks the automation's owner_id's live
// RBAC against the entity the trigger actually fired on, immediately
// before every firing applies (catalog_actions.go's Permission doc
// carries the full rationale). This check only stops a user from
// authoring an automation whose effect they plainly cannot perform by
// hand today.
func requireAuthorCeiling(ctx context.Context, entry CatalogEntry) error {
	def, ok := ActionDefFor(ActionType(entry.Action))
	if !ok {
		return fmt.Errorf("automation catalog entry %q names action %q, which the action registry does not define", entry.Key, entry.Action)
	}
	object := def.RequiredPermission.Object
	if def.RequiredPermission.Shape == PermissionTargetScoped {
		entity, resolvable := entityFromTrigger(entry.Trigger)
		if !resolvable {
			// No entity to check against: blocking authoring on a guessed
			// object would be worse than deferring here, since this is
			// fast-fail UX rather than the enforcement point — the
			// match-time gate resolves the real target and fails closed
			// there.
			return nil
		}
		object = entity
	}
	return auth.Require(ctx, object, principal.Action(def.RequiredPermission.Action))
}

// entityFromTrigger reads the entity name a trigger's event type fires
// on: the substring before the first '.' (e.g. "lead.created" → "lead").
// Event prefixes and RBAC object names share the same spelling
// (identity/internal/policy's coreObjects), so no translation table is
// needed; ok is false when the trigger names no dot-qualified entity — a
// clock or cross-entity trigger with nothing to resolve.
func entityFromTrigger(trigger string) (string, bool) {
	entity, _, found := strings.Cut(trigger, ".")
	if !found || entity == "" {
		return "", false
	}
	return entity, true
}
