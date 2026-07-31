// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Who a flipped record belongs to. The incumbent's owner ids mean
// nothing natively, and the answer decides VISIBILITY — an ownerless
// native row is workspace-shared at every tier, while the mirror row it
// came from was hidden from every seat — so this is a security decision
// before it is a mapping one, and it lives on its own.

import (
	"context"
	"fmt"
	"strings"

	"github.com/gradionhq/margince/backend/internal/modules/migration"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// resolveOwner maps the row's incumbent owner id onto the mapped
// app_user. An owner that does not map —
// or a row that names none at all — imports under the flip OPERATOR,
// disclosed: an ownerless native row is workspace-shared at every tier,
// while the mirror row it came from was hidden from every seat.
func (w *flipWriters) resolveOwner(ctx context.Context, row migration.Row, object string) (*ids.UserID, string, error) {
	raw := strings.TrimSpace(row.OwnerExternalID)
	if raw == "" {
		// No incumbent owner at all: the mirror row was hidden from every
		// seat (the fail-closed NULL-owner rule), so it inherits the
		// operator rather than becoming a workspace-shared native row.
		return w.operator, fmt.Sprintf("%s %s: the incumbent record names no owner; imported under the flip operator rather than left workspace-visible", object, row.ExternalID), nil
	}
	id, found, err := w.mappedOwner(ctx, raw)
	if err != nil {
		return nil, "", err
	}
	if !found {
		// Inherited by the operator, not left ownerless: an ownerless
		// native row is visible to every seat, while the mirror row it
		// came from was hidden from all of them.
		return w.operator, fmt.Sprintf("%s %s: incumbent owner %s has no user mapping; imported under the flip operator rather than left workspace-visible", object, row.ExternalID, raw), nil
	}
	owner := ids.From[ids.UserKind](id)
	return &owner, "", nil
}

// mappedOwner turns an incumbent owner id into an app_user through
// whichever map this run has: the bundle's own on the reconstruction
// path, the live mirror_user_map otherwise. Live lookups are memoized
// because a flip resolves the owner of EVERY row it imports, and an
// estate's owners number in the dozens where its records number in the
// hundreds of thousands — without the memo the import spends one query
// per record re-answering a question it has already answered. The
// mirror is frozen for the duration, so the answer cannot go stale.
func (w *flipWriters) mappedOwner(ctx context.Context, incumbentUser string) (ids.UUID, bool, error) {
	if w.ownerOverride != nil {
		id, found := w.ownerOverride[incumbentUser]
		return id, found, nil
	}
	if id, found := w.ownerCache[incumbentUser]; found {
		return id, true, nil
	}
	id, found, err := w.ms.ResolveMirrorOwner(ctx, incumbentUser)
	if err != nil || !found {
		return ids.UUID{}, false, err
	}
	if w.ownerCache == nil {
		w.ownerCache = map[string]ids.UUID{}
	}
	w.ownerCache[incumbentUser] = id
	return id, true, nil
}
