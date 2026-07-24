// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package hubspot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// This file is the adapter's write half (the Incumbent write seam, design.md
// §4.5): canonical→HubSpot projection (mapWrite) + the transport in writes.go,
// with the write-back drift check that makes an overlay write incumbent-first
// (AC-OV-4). It is the inverse of the read methods in adapter.go and returns
// canonical overlay.Records, mapped back through the same mapRecord the read
// path uses — so the mirror ingests a write's result exactly as it ingests a
// sync read.

// Create writes a new record of canonicalClass to HubSpot from the canonical
// field bag and returns the created record mapped back to canonical. A write
// whose fields are all read-only/derived (empty projection) cannot create an
// incumbent object — that is an honest error, never a POST of a blank record.
//
//craft:ignore naked-any fields is the JSON-decoded canonical bag from the datasource seam; the any is inherent to the decoded shape
func (a *Adapter) Create(ctx context.Context, canonicalClass string, fields map[string]any) (overlay.WriteResult, error) {
	mw, err := mapWrite(canonicalClass, fields, false)
	if err != nil {
		return overlay.WriteResult{}, err
	}
	if len(mw.Props) == 0 {
		return overlay.WriteResult{}, fmt.Errorf("overlay: cannot create a %s in HubSpot — every supplied field is read-only or derived (nothing to write)", canonicalClass)
	}
	created, err := a.client.CreateObject(ctx, mw.ObjectClass, mw.Props)
	if err != nil {
		return overlay.WriteResult{}, err
	}
	// Re-read the created record through the SAME path a sync read uses
	// (a.Get requests the baseline watermark property), so the mirrored
	// record's ModifiedAt is the object-specific baseline (lastmodifieddate /
	// hs_lastmodifieddate / hs_timestamp) the drift check compares against —
	// never HubSpot's top-level updatedAt, which can diverge from it.
	rec, err := a.Get(ctx, mw.ObjectClass, mirrorActivityExternalID(mw.ObjectClass, created.ID))
	if err != nil {
		return overlay.WriteResult{}, err
	}
	// WrittenProps carries the exact HubSpot properties+values written so the
	// echo-suppression ledger (OVA-DDL-6) recognizes this write's echo webhook.
	return overlay.WriteResult{Record: rec, IncumbentClass: mw.ObjectClass, WrittenProps: mw.Props}, nil
}

// Update applies a canonical patch to an existing record after the
// stored-baseline drift check: if HubSpot's CURRENT record is newer than the
// baseline captured at mirror-read, a third party (or the HubSpot UI) changed
// it since we mirrored, so the write is refused with ErrVersionSkew and
// NOTHING is PATCHed (AC-OV-4, incumbent-wins) — never a blind overwrite. A
// patch of only read-only fields writes nothing and returns the current
// record unchanged. externalID is the mirror id (namespaced for an activity,
// OVA-MAP-7).
//
//craft:ignore naked-any fields is the JSON-decoded canonical patch from the datasource seam; the any is inherent to the decoded shape
func (a *Adapter) Update(ctx context.Context, canonicalClass, externalID string, fields map[string]any, baseline time.Time) (overlay.WriteResult, error) {
	mw, err := mapWrite(canonicalClass, fields, true)
	if err != nil {
		return overlay.WriteResult{}, err
	}
	// An activity's engagement class is fixed by its mirror id's "<class>:"
	// prefix (OVA-MAP-7); a patch that carried a changed/inconsistent kind
	// would make mapWrite target a different class than the record's identity.
	// Refuse that mismatch rather than route a write to meetings/calls:123.
	if canonicalClass == activityTarget {
		want, err := incumbentClassFor(canonicalClass, externalID)
		if err != nil {
			return overlay.WriteResult{}, err
		}
		if mw.ObjectClass != want {
			return overlay.WriteResult{}, fmt.Errorf("overlay: activity %s is a %s; its kind cannot be changed to a %s on update", externalID, want, mw.ObjectClass)
		}
	}
	if len(mw.Props) == 0 {
		// A read-only-fields patch writes nothing; return the current record with
		// no written properties. No drift check — a no-op cannot lose a
		// concurrent edit, and no ledger entry is opened (nothing was written).
		rec, err := a.Get(ctx, mw.ObjectClass, externalID)
		if err != nil {
			return overlay.WriteResult{}, err
		}
		return overlay.WriteResult{Record: rec, IncumbentClass: mw.ObjectClass}, nil
	}
	if err := a.driftCheck(ctx, mw.ObjectClass, externalID, baseline); err != nil {
		return overlay.WriteResult{}, err
	}
	if _, err := a.client.UpdateObject(ctx, mw.ObjectClass, incumbentActivityID(mw.ObjectClass, externalID), mw.Props); err != nil {
		return overlay.WriteResult{}, err
	}
	// Re-read through the sync-read path so the mirrored ModifiedAt is the
	// baseline watermark property, consistent with reads and the drift check
	// (see Create's note).
	rec, err := a.Get(ctx, mw.ObjectClass, externalID)
	if err != nil {
		return overlay.WriteResult{}, err
	}
	return overlay.WriteResult{Record: rec, IncumbentClass: mw.ObjectClass, WrittenProps: mw.Props}, nil
}

// Archive removes a record from HubSpot via its own archive/delete, after the
// SAME stored-baseline drift check Update applies: a stale mirror must not
// delete a record a third party changed since it was mirrored (AC-OV-4). For
// an activity the engagement class is recovered from the mirror id's
// "<class>:" prefix (OVA-MAP-7); for the other object types it is the single
// incumbent class the canonical type maps to.
func (a *Adapter) Archive(ctx context.Context, canonicalClass, externalID string, baseline time.Time) error {
	hsClass, err := incumbentClassFor(canonicalClass, externalID)
	if err != nil {
		return err
	}
	if err := a.driftCheck(ctx, hsClass, externalID, baseline); err != nil {
		return err
	}
	return a.client.ArchiveObject(ctx, hsClass, incumbentActivityID(hsClass, externalID))
}

// driftCheck reads the incumbent's current record and refuses (ErrVersionSkew)
// if it is newer than baseline — the stored-baseline lost-update guard shared
// by Update and Archive. It is best-effort (non-atomic) per the Incumbent
// contract: HubSpot v3 has no If-Match primitive.
func (a *Adapter) driftCheck(ctx context.Context, hsClass, externalID string, baseline time.Time) error {
	current, err := a.Get(ctx, hsClass, externalID)
	if err != nil {
		return err
	}
	if current.ModifiedAt.After(baseline) {
		return apperrors.ErrVersionSkew
	}
	return nil
}

// incumbentClassFor resolves the HubSpot object class a canonical write
// targets. An activity's mirror id namespaces its source engagement class
// ("calls:123"); every other canonical type maps to exactly one incumbent
// class (IncumbentClassesFor). A canonical type with no mapping, or an
// activity id with no valid class prefix, is an honest error rather than a
// guessed endpoint.
func incumbentClassFor(canonicalClass, externalID string) (string, error) {
	if canonicalClass == activityTarget {
		class, _, found := strings.Cut(externalID, ":")
		if !found || !isEngagementClass(class) {
			return "", fmt.Errorf("overlay: activity mirror id %q carries no valid engagement class prefix (OVA-MAP-7)", externalID)
		}
		return class, nil
	}
	classes, ok := IncumbentClassesFor(canonicalClass)
	if !ok || len(classes) != 1 {
		return "", fmt.Errorf("overlay: canonical class %q does not map to exactly one HubSpot write class", canonicalClass)
	}
	return classes[0], nil
}

// AccountID returns the HubSpot portal id for this connection's token — the
// incumbent account identity recorded at connect (OVA-DDL-3) for the
// webhook-as-signal tenant binding.
func (a *Adapter) AccountID(ctx context.Context) (string, error) {
	return a.client.AccountID(ctx)
}
