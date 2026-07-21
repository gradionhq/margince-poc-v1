// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package fake is an in-memory overlay.Incumbent used by tests: it pages,
// filters, and answers association/owner lookups over a seeded record
// set without ever reaching a real incumbent CRM.
package fake

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
)

// pageSize is the fixed Backfill/Modified page size the fake pages by.
const pageSize = 100

// Adapter is an in-memory overlay.Incumbent: records are seeded per
// objectClass and served back through the same paging/filtering
// contract a real incumbent client would honor.
type Adapter struct {
	records map[string][]overlay.Record
	assocs  map[assocKey][]overlay.Assoc
	owners  map[string]string
}

// assocKey identifies one Associations(fromClass, fromID, toClass) query
// — the same triple SeedAssoc and Associations key their lookup by.
type assocKey struct {
	fromClass, fromID, toClass string
}

var _ overlay.Incumbent = (*Adapter)(nil)

// New returns an empty Adapter ready for Seed.
func New() *Adapter {
	return &Adapter{records: make(map[string][]overlay.Record), owners: make(map[string]string)}
}

// SeedOwner adds one owner (id → email) to the fake's owners directory —
// the fixture mirror_user_map seeding matches against, and the same value
// OwnerEmail resolves that id to.
func (a *Adapter) SeedOwner(ownerExternalID, email string) {
	a.owners[ownerExternalID] = email
}

// Rec builds an overlay.Record for externalID carrying fields, stamped
// with the current time as ModifiedAt.
func Rec(externalID string, fields map[string]any) overlay.Record {
	return overlay.Record{
		ExternalID: externalID,
		Fields:     fields,
		ModifiedAt: time.Now(),
	}
}

// Seed appends rec to objectClass's in-memory record set — objectClass
// is the key Backfill/Modified page by (the incumbent's own paging
// vocabulary). rec.ObjectClass defaults to objectClass when the caller
// leaves it unset (the common case: fake.Rec never sets it), but a
// caller that already set rec.ObjectClass (e.g. to a CANONICAL entity
// type, simulating what a real adapter's mapRecord already translated
// it to) keeps that value — a real overlay.Incumbent's Backfill/Modified
// always returns Records keyed by canonical ObjectClass even though it
// pages by the incumbent's own class name, and a fake that silently
// overwrote a caller-supplied ObjectClass could never simulate that.
func (a *Adapter) Seed(objectClass string, rec overlay.Record) {
	if rec.ObjectClass == "" {
		rec.ObjectClass = objectClass
	}
	a.records[objectClass] = append(a.records[objectClass], rec)
}

// SeedAssoc records the association edges Associations(fromClass,
// fromID, toClass) answers for that exact triple — an unseeded triple
// still answers no edges (Associations' existing honest-gap behavior),
// never a fabricated one.
func (a *Adapter) SeedAssoc(fromClass, fromID, toClass string, assocs ...overlay.Assoc) {
	if a.assocs == nil {
		a.assocs = make(map[assocKey][]overlay.Assoc)
	}
	key := assocKey{fromClass, fromID, toClass}
	a.assocs[key] = append(a.assocs[key], assocs...)
}

// Name identifies this incumbent implementation.
func (a *Adapter) Name() string { return "fake" }

// Backfill pages objectClass's seeded records by index cursor, pageSize
// records at a time.
func (a *Adapter) Backfill(_ context.Context, objectClass, cursor string) (overlay.Page, error) {
	all := a.records[objectClass]
	start, err := parseCursor(cursor)
	if err != nil {
		return overlay.Page{}, err
	}
	if start > len(all) {
		start = len(all)
	}
	end := start + pageSize
	if end > len(all) {
		end = len(all)
	}

	page := overlay.Page{Records: append([]overlay.Record(nil), all[start:end]...)}
	if end < len(all) {
		page.NextCursor = fmt.Sprint(end)
	}
	return page, nil
}

// Modified filters objectClass's seeded records to those modified at or
// after since, sorted ascending by ModifiedAt, then pages the result the
// same way Backfill does.
func (a *Adapter) Modified(_ context.Context, objectClass string, since time.Time, cursor string) (overlay.Page, error) {
	var matched []overlay.Record
	for _, rec := range a.records[objectClass] {
		if !rec.ModifiedAt.Before(since) {
			matched = append(matched, rec)
		}
	}
	sort.Slice(matched, func(i, j int) bool {
		return matched[i].ModifiedAt.Before(matched[j].ModifiedAt)
	})

	start, err := parseCursor(cursor)
	if err != nil {
		return overlay.Page{}, err
	}
	if start > len(matched) {
		start = len(matched)
	}
	end := start + pageSize
	if end > len(matched) {
		end = len(matched)
	}

	page := overlay.Page{Records: append([]overlay.Record(nil), matched[start:end]...)}
	if end < len(matched) {
		page.NextCursor = fmt.Sprint(end)
	}
	return page, nil
}

// Get returns objectClass's seeded record for externalID.
func (a *Adapter) Get(_ context.Context, objectClass, externalID string) (overlay.Record, error) {
	for _, rec := range a.records[objectClass] {
		if rec.ExternalID == externalID {
			return rec, nil
		}
	}
	return overlay.Record{}, fmt.Errorf("fake: no %s record with external id %s", objectClass, externalID)
}

// Associations returns the edges SeedAssoc recorded for this exact
// (fromClass, fromID, toClass) triple — an unseeded triple answers no
// edges rather than fabricating a link the fake has no data for.
func (a *Adapter) Associations(_ context.Context, fromClass, fromID, toClass string) ([]overlay.Assoc, error) {
	return a.assocs[assocKey{fromClass, fromID, toClass}], nil
}

// OwnerEmail resolves a seeded owner id to its email, reporting the
// reference unknown rather than fabricating an email for an unseeded one.
func (a *Adapter) OwnerEmail(_ context.Context, ownerExternalID string) (string, error) {
	email, ok := a.owners[ownerExternalID]
	if !ok {
		return "", fmt.Errorf("fake: no owner with external id %s", ownerExternalID)
	}
	return email, nil
}

// Owners lists the seeded owners directory in id order — deterministic so
// a test asserting the seeded set never flakes on map iteration order.
func (a *Adapter) Owners(_ context.Context) ([]overlay.OwnerRef, error) {
	ids := make([]string, 0, len(a.owners))
	for id := range a.owners {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]overlay.OwnerRef, 0, len(a.owners))
	for _, id := range ids {
		out = append(out, overlay.OwnerRef{ExternalID: id, Email: a.owners[id]})
	}
	return out, nil
}

// parseCursor decodes a Backfill/Modified cursor to its index offset;
// "" (the start-of-paging cursor) decodes to 0.
func parseCursor(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	var n int
	if _, err := fmt.Sscanf(cursor, "%d", &n); err != nil {
		return 0, fmt.Errorf("fake: invalid cursor %q: %w", cursor, err)
	}
	return n, nil
}
