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
	records   map[string][]overlay.Record
	assocs    map[assocKey][]overlay.Assoc
	owners    map[string]string
	deletions map[string][]overlay.Deletion
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

// SeedDeletion records that objectClass's deletion feed reports del — the
// removal signal the deletion sweep pages back through Deletions to purge
// from the mirror. objectClass is the incumbent bucket the feed is paged
// by (as Seed's objectClass is), while del.ObjectClass carries the
// CANONICAL class a real adapter's mapping would have translated it to
// (defaulting to objectClass when the caller leaves it unset), so the fake
// faithfully simulates the incumbent→canonical translation the deletion
// sweep relies on to purge the right mirror row.
//
// Call order matters, mirroring the incumbent: SeedDeletion drops any live
// row it currently holds for del.ExternalID (an archived record leaves the
// live feed), so a test that wants a record both live-then-deleted must
// Seed it BEFORE calling SeedDeletion. A Seed of the same id AFTER
// SeedDeletion re-introduces it as live — the fake models "restored in the
// incumbent", not an inconsistency.
func (a *Adapter) SeedDeletion(objectClass string, del overlay.Deletion) {
	if del.ObjectClass == "" {
		del.ObjectClass = objectClass
	}
	if a.deletions == nil {
		a.deletions = make(map[string][]overlay.Deletion)
	}
	a.deletions[objectClass] = append(a.deletions[objectClass], del)

	// An archived record no longer appears in the live Backfill/Modified
	// feed (HubSpot excludes archived objects from Search) — drop it from
	// the live set so the fake never serves a record that is simultaneously
	// live and deleted, the same invariant the deletion sweep's ordering
	// relies on.
	live := a.records[objectClass][:0]
	for _, rec := range a.records[objectClass] {
		if rec.ExternalID != del.ExternalID {
			live = append(live, rec)
		}
	}
	a.records[objectClass] = live
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
	page, next, err := filterSortPage(a.records[objectClass], since, cursor, func(r overlay.Record) time.Time { return r.ModifiedAt })
	if err != nil {
		return overlay.Page{}, err
	}
	return overlay.Page{Records: page, NextCursor: next}, nil
}

// filterSortPage is the since-filter → ascending-timestamp-sort → cursor-page
// pipeline Modified and Deletions share: it keeps items whose at(item) is at
// or after since, sorts the survivors ascending by that timestamp, and pages
// them the same index-cursor way Backfill does. at extracts the ordering
// timestamp (ModifiedAt for records, DeletedAt for deletions) so the one
// pipeline serves both feeds without either restating it.
func filterSortPage[T any](items []T, since time.Time, cursor string, at func(T) time.Time) ([]T, string, error) {
	var matched []T
	for _, it := range items {
		if !at(it).Before(since) {
			matched = append(matched, it)
		}
	}
	sort.Slice(matched, func(i, j int) bool { return at(matched[i]).Before(at(matched[j])) })

	start, err := parseCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	if start > len(matched) {
		start = len(matched)
	}
	end := start + pageSize
	if end > len(matched) {
		end = len(matched)
	}

	next := ""
	if end < len(matched) {
		next = fmt.Sprint(end)
	}
	return append([]T(nil), matched[start:end]...), next, nil
}

// Deletions filters objectClass's seeded deletions to those at or after
// since, sorted ascending by DeletedAt, then pages the result the same
// way Modified pages live records.
func (a *Adapter) Deletions(_ context.Context, objectClass string, since time.Time, cursor string) (overlay.DeletionPage, error) {
	page, next, err := filterSortPage(a.deletions[objectClass], since, cursor, func(d overlay.Deletion) time.Time { return d.DeletedAt })
	if err != nil {
		return overlay.DeletionPage{}, err
	}
	return overlay.DeletionPage{Deletions: page, NextCursor: next}, nil
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
