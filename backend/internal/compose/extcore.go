// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The core's half of extension.Core: a unit's governed write onto the
// product's own records, made on the transaction the unit already holds.
//
// Everything here is a translation between two vocabularies and a set of
// refusals. The WRITE is the product's own — activities.LogActivityTx, the same
// entry point the HTTP handler reaches — so the RBAC gate, the row-scope check
// on every link, the audit row, the outbox event and the captured_by stamp are
// inherited rather than re-implemented. A port that re-implemented any of them
// would be a second write shape, which is the thing the tier exists to avoid.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/pkg/extension"
	"github.com/gradionhq/margince/backend/pkg/extension/crm"
)

// extensionCore is one transaction's Core. It holds the transaction rather than
// reaching for a connection, which is the property the whole seam exists for:
// the unit's own row and the core record it writes are in the same transaction,
// so they commit together or not at all.
type extensionCore struct {
	tx pgx.Tx
	// tick marks an invocation with no human behind it. Held rather than
	// re-derived because the reason a tick is refused is about the INVOCATION,
	// not about the context the handler passes in.
	tick bool
	deps extensionRuntimeBinding
}

//nolint:ireturn // returning the published repo IS the seam: a unit holds extension.ActivityRepo, never a core type.
func (c extensionCore) Activities() extension.ActivityRepo {
	return extensionActivities{core: c}
}

type extensionActivities struct{ core extensionCore }

// Create files one activity through the product's own write path.
func (a extensionActivities) Create(ctx context.Context, in crm.CreateActivityRequest) (crm.Activity, error) {
	if err := a.core.admit(ctx); err != nil {
		return crm.Activity{}, err
	}
	request, err := transcode[crmcontracts.CreateActivityRequest](in)
	if err != nil {
		return crm.Activity{}, fmt.Errorf("%w: %s", extension.ErrInvalid, err)
	}
	mapped, err := activities.LogActivityInputFrom(request)
	if err != nil {
		return crm.Activity{}, portRefusal(err)
	}
	// The store is built here rather than bound at boot, from the same pool the
	// transaction came off: it is a value over a handle, activities.NewStore is
	// what every other composition site does with it, and deriving it means no
	// role can wire the port half-way.
	store := activities.NewStore(InstallationDB(a.core.deps.pool))
	logged, _, err := store.LogActivityTx(ctx, a.core.tx, mapped)
	if err != nil {
		return crm.Activity{}, portRefusal(err)
	}
	published, err := transcode[crm.Activity](logged)
	if err != nil {
		return crm.Activity{}, fmt.Errorf("compose: an activity the store wrote does not fit the published shape: %w", err)
	}
	return published, nil
}

// admit is the check every Core verb makes before it does anything: the two
// invocations that may not reach core records at all.
func (c extensionCore) admit(ctx context.Context) error {
	if c.tick {
		// A tick runs as the unit, with nobody behind it. Core writes are
		// checked against the CALLER's live RBAC, and there is no caller to
		// check — the alternatives are a system principal, which passes every
		// check ever written, or resolving the workspace's agent seat, which is
		// a governance surface of its own. Both are features; this is a
		// refusal. A tick's own tables stay writable, which is what a tick is
		// for.
		return fmt.Errorf("%w: a scheduled job runs with no caller, and a core write is checked against the caller's own permissions", extension.ErrForbidden)
	}
	workspace, bound := principal.WorkspaceID(ctx)
	if !bound {
		return database.ErrNoWorkspace
	}
	// FRESH, never cached, and for the reason the dispatcher's own uncached read
	// carries: a write routed on a stale mode is silent divergence rather than a
	// stale screen. An overlay workspace's native tables are not the live ones,
	// so this write would land where nothing reads it.
	//
	// Read on the CALLER'S transaction, which is both safer and stronger than a
	// connection of its own. Safer: a second acquire inside a borrowed
	// transaction is the deadlock shape this programme removed from the store
	// seams. Stronger: the mode and the write it guards are then the same
	// transaction, so the answer cannot go stale between them — the dispatcher's
	// own read narrows that window and cannot close it.
	overlaid, err := overlayModeOf(ctx, c.tx, workspace)
	if err != nil {
		return fmt.Errorf("compose: resolving the workspace's record mode for an extension core write: %w", err)
	}
	if overlaid {
		return extension.ErrOverlayUnsupported
	}
	return nil
}

// portRefusal maps a core error onto the published refusal classes.
//
// It maps rather than wraps, and that is the point: a unit is other people's
// code, so the core's own error text — a table name, a constraint, a SQL state,
// the shape of an internal type — must not reach it. What survives is the
// class, which is the only part a unit can act on.
func portRefusal(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, apperrors.ErrPermissionDenied):
		return extension.ErrForbidden
	case errors.Is(err, apperrors.ErrNotFound):
		return extension.ErrNotFound
	case errors.Is(err, apperrors.ErrVersionSkew), errors.Is(err, apperrors.ErrConflict):
		return extension.ErrConflict
	}
	// A field refusal is the contract's own "this request is malformed" — the
	// same interface httperr turns into a 422 — so it maps to the same class
	// here. Its MESSAGE does not travel: the three strings it carries are
	// written for the product's own clients, and a unit is not one.
	var fault apperrors.FieldFault
	if errors.As(err, &fault) {
		return extension.ErrInvalid
	}
	// A fault with no class is the core's own — a broken connection, a
	// constraint nobody mapped. The unit is told the write failed and nothing
	// about how; the detail belongs in the core's logs, where it is already.
	return fmt.Errorf("extension: the core refused this write")
}

// transcode carries a value between the internal contract types and the
// published ones through their shared JSON shape.
//
// The two are generated from the SAME schema in backend/api/crm.yaml — the
// internal set from the whole contract, the published set from the subset in
// pkg/extension/crm — so their JSON is identical by construction and a field
// added to the contract appears on both sides at once. That is what makes this
// safer than a hand-written mapper here, which would compile perfectly while
// silently dropping the new field.
//
//craft:ignore naked-any the source IS a generated contract value of either set; naming one would defeat the point of a bridge between them
func transcode[T any](src any) (T, error) {
	var out T
	encoded, err := json.Marshal(src)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(encoded, &out); err != nil {
		return out, err
	}
	return out, nil
}
