// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// This file is the adapter binding the HubSpot REST Client
// (client.go/search.go/records.go) to the overlay.Incumbent seam every
// other incumbent-agnostic overlay component (mirror sync, budget,
// conflict logic) reaches HubSpot through. Every method delegates to a
// Client call and maps the raw HubSpot properties into an overlay.Record
// via overlay.Apply(Mapping(objectClass), raw) — the mapping IR
// (mapping_hs.go) stays the single place field-level projection logic
// lives; this file never hand-extracts a HubSpot property.

package hubspot

import (
	"context"
	"fmt"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// pageSize is the fixed page size Backfill/Modified request per call.
const pageSize = 100

// directionForward labels every Assoc this adapter returns: HubSpot's v4
// associations endpoint is inherently one-directional (it resolves edges
// FROM the queried record TO toClass), so every edge this method call can
// see is the same direction. A caller that also needs the reverse
// (toClass → fromClass) edges issues a separate Associations call from
// the other side — that result is a distinct query, not this one
// relabeled, so this adapter never guesses at a "reverse" value.
const directionForward = "forward"

// Adapter is the overlay.Incumbent implementation over the HubSpot REST
// Client: it carries no state of its own beyond the Client connection —
// mirror sync, budget, and conflict-detection state all live above this
// seam.
type Adapter struct {
	client *Client
}

var _ overlay.Incumbent = (*Adapter)(nil)

// NewAdapter wraps client as an overlay.Incumbent.
func NewAdapter(client *Client) *Adapter {
	return &Adapter{client: client}
}

// Name identifies this incumbent implementation.
func (a *Adapter) Name() string { return "hubspot" }

// watermarkProperty returns the per-object modified-timestamp property
// the incremental sweep sorts/filters by (design.md §7, spike-confirmed):
// contacts use lastmodifieddate; every other object uses
// hs_lastmodifieddate.
func watermarkProperty(objectClass string) string {
	if objectClass == objectClassContacts {
		return "lastmodifieddate"
	}
	return baselineHSLastModifiedDate
}

// Backfill lists objectClass records id-cursor style via the Client's
// List endpoint (the uncapped backfill cursor, design.md §11) and maps
// each into an overlay.Record.
func (a *Adapter) Backfill(ctx context.Context, objectClass, cursor string) (overlay.Page, error) {
	m, err := mappingFor(objectClass)
	if err != nil {
		return overlay.Page{}, err
	}
	page, err := a.client.List(ctx, objectClass, propertyNames(m), cursor, pageSize)
	if err != nil {
		return overlay.Page{}, err
	}
	records, err := mapRecords(m, objectClass, page.Results)
	if err != nil {
		return overlay.Page{}, err
	}
	return overlay.Page{Records: records, NextCursor: page.NextAfter}, nil
}

// Modified searches objectClass records modified at or after since via
// the Client's watermark-sorted Search sweep (design.md §4.4/§7), mapping
// each result into an overlay.Record in the ascending order HubSpot
// returns them. cursor is SearchModified's own offset-capped after token
// (design.md §11); paging past the 10k-per-timestamp ceiling into a
// tied-timestamp numeric-id keyset is an open spike (design.md §7),
// deliberately not built at this seam yet.
func (a *Adapter) Modified(ctx context.Context, objectClass string, since time.Time, cursor string) (overlay.Page, error) {
	m, err := mappingFor(objectClass)
	if err != nil {
		return overlay.Page{}, err
	}
	page, err := a.client.SearchModified(ctx, objectClass, watermarkProperty(objectClass), since, cursor, pageSize, propertyNames(m))
	if err != nil {
		return overlay.Page{}, err
	}
	records, err := mapRecords(m, objectClass, page.Results)
	if err != nil {
		return overlay.Page{}, err
	}
	return overlay.Page{Records: records, NextCursor: page.NextAfter}, nil
}

// Deletions pages objectClass's archived (deleted) records via the
// Client's archived-object list feed and maps each into an
// overlay.Deletion keyed by the CANONICAL object class (m.Target) — the
// same incumbent-naming-never-leaks rule mapRecord obeys. archivedAt
// becomes DeletedAt; a record archived strictly before since is dropped
// (the caller's watermark lower-bound), the rest are returned in the feed
// order. Unlike Modified, this feed is not watermark-ordered on the wire
// (HubSpot's list endpoint cannot sort by archivedAt), so the deletion
// sweep pages it fully each pass — see Client.ListArchived for why that
// stays correct.
func (a *Adapter) Deletions(ctx context.Context, objectClass string, since time.Time, cursor string) (overlay.DeletionPage, error) {
	m, err := mappingFor(objectClass)
	if err != nil {
		return overlay.DeletionPage{}, err
	}
	page, err := a.client.ListArchived(ctx, objectClass, cursor, pageSize)
	if err != nil {
		return overlay.DeletionPage{}, err
	}
	deletions := make([]overlay.Deletion, 0, len(page.Results))
	for _, r := range page.Results {
		deletedAt, err := parseWatermark(r.ArchivedAt)
		if err != nil {
			return overlay.DeletionPage{}, fmt.Errorf("hubspot: archived %s record %s: %w", objectClass, r.ID, err)
		}
		if deletedAt.Before(since) {
			continue
		}
		deletions = append(deletions, overlay.Deletion{
			// Namespace the id so the purge matches the namespaced mirror row
			// (OVA-MAP-7); a bare id would miss an engagement activity entirely.
			ExternalID:  mirrorActivityExternalID(objectClass, r.ID),
			ObjectClass: m.Target,
			DeletedAt:   deletedAt,
		})
	}
	return overlay.DeletionPage{Deletions: deletions, NextCursor: page.NextAfter}, nil
}

// Get fetches one record's current HubSpot-side state via BatchRead (the
// record-clock fetch, design.md §4.4 — the read a force-fresh
// read-through lands on).
func (a *Adapter) Get(ctx context.Context, objectClass, externalID string) (overlay.Record, error) {
	m, err := mappingFor(objectClass)
	if err != nil {
		return overlay.Record{}, err
	}
	// externalID arrives as the MIRROR id (namespaced for an engagement
	// class, OVA-MAP-7); the HubSpot API needs the raw object id.
	recs, err := a.client.BatchRead(ctx, objectClass, []string{incumbentActivityID(objectClass, externalID)}, propertyNames(m))
	if err != nil {
		return overlay.Record{}, err
	}
	if len(recs) == 0 {
		return overlay.Record{}, fmt.Errorf("hubspot: no %s record with external id %s", objectClass, externalID)
	}
	return mapRecord(m, objectClass, recs[0])
}

// Associations lists the v4 association edges from fromID (of fromClass)
// to every linked record of toClass, tagging each edge with the forward
// direction this query resolves.
func (a *Adapter) Associations(ctx context.Context, fromClass, fromID, toClass string) ([]overlay.Assoc, error) {
	// fromID is the MIRROR id (namespaced for an engagement class,
	// OVA-MAP-7). The v4 API needs the raw object id, but the stored edge
	// must reference the activity by its namespaced mirror id so it joins the
	// mirror row — so query with the raw id and keep the namespaced FromID.
	assocs, err := a.client.Associations(ctx, fromClass, incumbentActivityID(fromClass, fromID), toClass)
	if err != nil {
		return nil, err
	}
	out := make([]overlay.Assoc, 0, len(assocs))
	for _, assoc := range assocs {
		for _, t := range assoc.Types {
			out = append(out, overlay.Assoc{
				FromType:  fromClass,
				FromID:    fromID,
				ToType:    toClass,
				ToID:      assoc.ToObjectID,
				TypeID:    t.TypeID,
				Category:  t.Category,
				Label:     t.Label,
				Direction: directionForward,
			})
		}
	}
	return out, nil
}

// OwnerEmail resolves a HubSpot owner id to its email via the Owners API
// (design.md §4.3's mirror_user_map resolution).
func (a *Adapter) OwnerEmail(ctx context.Context, ownerExternalID string) (string, error) {
	owner, err := a.client.Owner(ctx, ownerExternalID)
	if err != nil {
		return "", err
	}
	return owner.Email, nil
}

// Owners lists the HubSpot owners directory as canonical OwnerRefs
// (design.md §4.3) — the population mirror_user_map seeding matches
// against the workspace's app_user rows.
func (a *Adapter) Owners(ctx context.Context) ([]overlay.OwnerRef, error) {
	owners, err := a.client.Owners(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]overlay.OwnerRef, 0, len(owners))
	for _, o := range owners {
		out = append(out, overlay.OwnerRef{ExternalID: o.ID, Email: o.Email})
	}
	return out, nil
}

// mappingFor resolves objectClass's mapping with a direct membership
// check against objectMappings (mapping_hs.go) — the same slice
// IncumbentClassesFor derives its reverse lookup from, so this function
// never drifts against what Mapping's switch actually declares as new
// object classes are added. A recover()-based guard around Mapping's
// panic-on-unknown-source was rejected: it would swallow ANY future
// panic from that function (a nil-map bug, an index error in a mapping
// literal) as a benign "unsupported object class", turning a real
// defect into a silent, hard-to-notice production failure mode.
func mappingFor(objectClass string) (overlay.ObjectMapping, error) {
	for _, m := range objectMappings {
		if m.Source == objectClass {
			return m, nil
		}
	}
	return overlay.ObjectMapping{}, fmt.Errorf("hubspot: object class %q has no declared mapping: %w", objectClass, apperrors.ErrUnsupportedBySoR)
}

// propertyNames collects the HubSpot property names m's projection reads
// — ExternalKey, Baseline, and every FieldMapping's From entries — so the
// Client only ever requests the properties the mapping actually consumes.
func propertyNames(m overlay.ObjectMapping) []string {
	seen := make(map[string]bool)
	var names []string
	add := func(k string) {
		if k == "" || seen[k] {
			return
		}
		seen[k] = true
		names = append(names, k)
	}
	add(m.ExternalKey)
	add(m.Baseline)
	for _, f := range m.Fields {
		for _, k := range f.From {
			add(k)
		}
	}
	return names
}

// mapRecords maps every raw ObjectRecord through mapRecord, in the order
// the Client returned them (Backfill/Modified both rely on this order —
// id-keyset ascending and watermark ascending respectively).
func mapRecords(m overlay.ObjectMapping, objectClass string, raws []ObjectRecord) ([]overlay.Record, error) {
	records := make([]overlay.Record, 0, len(raws))
	for _, raw := range raws {
		rec, err := mapRecord(m, objectClass, raw)
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, nil
}

// mapRecord projects one raw HubSpot ObjectRecord into an overlay.Record
// via overlay.Apply, then lifts the mapping's structural external_id/
// last_synced_at/owner_id targets into Record's own ExternalID/
// ModifiedAt/OwnerExternalID fields — the mirror sync loop and the
// conflict/owner-resolution logic above this seam read those directly
// rather than re-deriving them from Fields.
func mapRecord(m overlay.ObjectMapping, objectClass string, raw ObjectRecord) (overlay.Record, error) {
	props := make(map[string]any, len(raw.Properties))
	for k, v := range raw.Properties {
		props[k] = v
	}

	out, _, err := overlay.Apply(m, props)
	if err != nil {
		return overlay.Record{}, fmt.Errorf("hubspot: mapping %s record %s: %w", objectClass, raw.ID, err)
	}

	externalID, _ := out["external_id"].(string)
	if externalID == "" {
		externalID = raw.ID
	}
	// Namespace an engagement id by its source class for the mirror key
	// (OVA-MAP-7): the five engagement classes share the canonical "activity"
	// type, and HubSpot ids are unique per-type only, so a bare id would let a
	// call and a meeting collide.
	externalID = mirrorActivityExternalID(objectClass, externalID)

	modifiedAt, err := parseWatermark(out["last_synced_at"])
	if err != nil {
		return overlay.Record{}, fmt.Errorf("hubspot: %s record %s: %w", objectClass, raw.ID, err)
	}

	ownerID, _ := out["owner_id"].(string)

	return overlay.Record{
		ExternalID: externalID,
		// The mirror is keyed by canonical entity type, not the incumbent
		// source name: the datasource read seam (overlay.Provider) reads
		// by canonical EntityType ("person"), so incumbent naming
		// ("contacts") must never leak above the Incumbent seam — m.Target
		// is that canonical name.
		ObjectClass:     m.Target,
		Fields:          out,
		ModifiedAt:      modifiedAt,
		OwnerExternalID: ownerID,
	}, nil
}

// parseWatermark parses an ObjectMapping's Baseline property (design.md
// §9: e.g. contacts' lastmodifieddate) — HubSpot's ISO-8601 timestamp
// string — into the Record's ModifiedAt. A missing or unparsable
// watermark is a mapping/data defect that must surface, never a silently
// zero-valued ModifiedAt that would corrupt watermark-ordered sync.
//
//craft:ignore naked-any v is the raw JSON baseline property value (an untyped HubSpot record field); asserts string within
func parseWatermark(v any) (time.Time, error) {
	s, ok := v.(string)
	if !ok || s == "" {
		return time.Time{}, fmt.Errorf("missing baseline watermark timestamp")
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing baseline watermark timestamp %q: %w", s, err)
	}
	return t, nil
}
