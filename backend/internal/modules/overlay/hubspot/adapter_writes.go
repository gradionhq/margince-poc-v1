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
func (a *Adapter) Create(ctx context.Context, canonicalClass string, fields map[string]any) (overlay.Record, error) {
	mw, err := mapWrite(canonicalClass, fields)
	if err != nil {
		return overlay.Record{}, err
	}
	if len(mw.Props) == 0 {
		return overlay.Record{}, fmt.Errorf("overlay: cannot create a %s in HubSpot — every supplied field is read-only or derived (nothing to write)", canonicalClass)
	}
	created, err := a.client.CreateObject(ctx, mw.ObjectClass, mw.Props)
	if err != nil {
		return overlay.Record{}, err
	}
	return a.mapWriteResult(ctx, mw.ObjectClass, created)
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
func (a *Adapter) Update(ctx context.Context, canonicalClass, externalID string, fields map[string]any, baseline time.Time) (overlay.Record, error) {
	mw, err := mapWrite(canonicalClass, fields)
	if err != nil {
		return overlay.Record{}, err
	}
	// The current incumbent state is both the drift anchor and the record a
	// read-only-only patch returns unchanged.
	current, err := a.Get(ctx, mw.ObjectClass, externalID)
	if err != nil {
		return overlay.Record{}, err
	}
	if len(mw.Props) == 0 {
		return current, nil
	}
	if current.ModifiedAt.After(baseline) {
		return overlay.Record{}, apperrors.ErrVersionSkew
	}
	updated, err := a.client.UpdateObject(ctx, mw.ObjectClass, incumbentActivityID(mw.ObjectClass, externalID), mw.Props)
	if err != nil {
		return overlay.Record{}, err
	}
	return a.mapWriteResult(ctx, mw.ObjectClass, updated)
}

// Archive removes a record from HubSpot via its own archive/delete. For an
// activity the engagement class is recovered from the mirror id's "<class>:"
// prefix (OVA-MAP-7); for the other object types it is the single incumbent
// class the canonical type maps to.
func (a *Adapter) Archive(ctx context.Context, canonicalClass, externalID string) error {
	hsClass, err := incumbentClassFor(canonicalClass, externalID)
	if err != nil {
		return err
	}
	return a.client.ArchiveObject(ctx, hsClass, incumbentActivityID(hsClass, externalID))
}

// mapWriteResult maps a create/update response object back to a canonical
// overlay.Record and enriches a lead's association-derived fields, so a
// write's result is the SAME shape a sync read produces — what the mirror
// ingests as the record's post-write state.
func (a *Adapter) mapWriteResult(ctx context.Context, hsClass string, obj ObjectRecord) (overlay.Record, error) {
	m, err := mappingFor(hsClass)
	if err != nil {
		return overlay.Record{}, err
	}
	rec, err := mapRecord(m, hsClass, obj)
	if err != nil {
		return overlay.Record{}, err
	}
	enriched, err := a.enrichLeads(ctx, hsClass, []overlay.Record{rec})
	if err != nil {
		return overlay.Record{}, err
	}
	return enriched[0], nil
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
