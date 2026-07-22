// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

import (
	"context"
	"fmt"
)

// MirrorSink is the ingest + cursor-checkpoint surface Backfill drives.
// *MirrorStore satisfies it in production; a unit test can inject an
// in-memory fake so Backfill's paging/resume/idempotency logic is
// provable against overlay/fake's paging Incumbent without a real
// Postgres — the real *MirrorStore's own storage behavior (tombstone,
// staleness, no-clobber-dirty guards) is proven separately by
// mirrorstore_integration_test.go.
type MirrorSink interface {
	Ingest(ctx context.Context, rec Record) error
	UpsertAssoc(ctx context.Context, a Assoc) error
	// LoadBackfillCursor reads back objectClass's persisted backfill
	// cursor. No prior run answers ("", false, nil) — a backfill that
	// has never started, not an error.
	LoadBackfillCursor(ctx context.Context, objectClass string) (cursor string, done bool, err error)
	// SaveBackfillCursor checkpoints objectClass's cursor after a page
	// lands — the restart-resume point (design.md §4.4).
	SaveBackfillCursor(ctx context.Context, objectClass, cursor string, done bool) error
}

// backfillAssocTargets names, for each incumbent object class Backfill
// drives, the toClass(es) design.md §9 explicitly calls for an
// association fetch on:
//
//   - deals→companies ("assoc→company→organization_id")
//   - each engagement class (calls/meetings/emails/notes/tasks) →
//     {contacts,companies,deals,leads} (§9: "assocs→activity_link" —
//     activity_link is a polymorphic pointer, so an engagement's association
//     can land on any of the other four canonical record types). HubSpot v3
//     reads the five engagement classes separately (OVA-MAP-1), so each
//     carries the same activity-link targets.
//
// An object class absent from this map has no §9-documented association
// need yet, so Backfill fetches none for it rather than guessing at a
// toClass no design section names.
var backfillAssocTargets = func() map[string][]string {
	m := map[string][]string{
		IncumbentClassDeals: {IncumbentClassCompanies},
	}
	for _, engagement := range incumbentEngagementClasses {
		m[engagement] = []string{IncumbentClassContacts, IncumbentClassCompanies, IncumbentClassDeals, IncumbentClassLeads}
	}
	return m
}()

// Backfill lists objectClass's full record set from inc via its
// list-cursor Backfill method, ingesting every page into ms and
// checkpointing the cursor after each page (design.md §4.4:
// "checkpointed, resumable, list-cursor paginated per object"). A prior
// run's persisted cursor resumes exactly where it left off rather than
// re-listing from the incumbent's start; a converged prior run (done)
// makes this call a cheap no-op.
//
// objectClass is the INCUMBENT class name (e.g. "companies") — the SEAM
// RULE every Incumbent method obeys: Backfill drives the seam with the
// incumbent's own vocabulary, while the Record pages it gets back already
// carry the CANONICAL entity type in ObjectClass (the mapping adapter's
// job, see hubspot/adapter.go's mapRecord) — Backfill itself never
// translates between the two, it only ever reads objectClass to call
// inc.Backfill/inc.Associations and to key the cursor checkpoint.
func Backfill(ctx context.Context, inc Incumbent, ms MirrorSink, objectClass string) error {
	cursor, done, err := ms.LoadBackfillCursor(ctx, objectClass)
	if err != nil {
		return fmt.Errorf("overlay: backfill %s: loading the persisted cursor: %w", objectClass, err)
	}
	if done {
		// A prior run already converged — resuming a finished backfill
		// re-lists nothing.
		return nil
	}

	for {
		page, err := inc.Backfill(ctx, objectClass, cursor)
		if err != nil {
			return fmt.Errorf("overlay: backfill %s: listing page at cursor %q: %w", objectClass, cursor, err)
		}

		for _, rec := range page.Records {
			if err := ms.Ingest(ctx, rec); err != nil {
				return fmt.Errorf("overlay: backfill %s: ingesting %s: %w", objectClass, rec.ExternalID, err)
			}
			if err := backfillAssociations(ctx, inc, ms, objectClass, rec.ExternalID); err != nil {
				return err
			}
		}

		cursor = page.NextCursor
		done = cursor == ""
		if err := ms.SaveBackfillCursor(ctx, objectClass, cursor, done); err != nil {
			return fmt.Errorf("overlay: backfill %s: checkpointing cursor: %w", objectClass, err)
		}
		if done {
			return nil
		}
	}
}

// backfillAssociations fetches and upserts the association edges
// backfillAssocTargets declares for objectClass from fromID — a no-op
// for any object class the table doesn't name.
func backfillAssociations(ctx context.Context, inc Incumbent, ms MirrorSink, objectClass, fromID string) error {
	for _, toClass := range backfillAssocTargets[objectClass] {
		assocs, err := inc.Associations(ctx, objectClass, fromID, toClass)
		if err != nil {
			return fmt.Errorf("overlay: backfill %s: fetching %s->%s associations for %s: %w",
				objectClass, objectClass, toClass, fromID, err)
		}
		for _, a := range assocs {
			if err := ms.UpsertAssoc(ctx, a); err != nil {
				return fmt.Errorf("overlay: backfill %s: upserting association %s/%s -> %s/%s: %w",
					objectClass, a.FromType, a.FromID, a.ToType, a.ToID, err)
			}
		}
	}
	return nil
}
