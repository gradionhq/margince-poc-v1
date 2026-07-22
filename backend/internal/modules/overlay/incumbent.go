// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

import (
	"context"
	"slices"
	"time"
)

// The HubSpot incumbent object classes design.md §9 maps
// (hubspot/mapping_hs.go's own objectClass* constants restate these —
// duplicated across the seam boundary rather than exported from
// hubspot, since a module never imports a sibling's internal naming,
// and this package's own callers (backfill.go's association-target
// table, compose/jobs.go's reconcile sweep) all need the same
// incumbent vocabulary spelled once on THIS side of the seam).
//
// HubSpot v3 exposes no generic "engagements" object: the five engagement
// object classes (calls/meetings/emails/notes/tasks) are read separately —
// each is its own /crm/v3/objects/<class> endpoint — and each maps to a
// distinct Margince activity kind (OVA-MAP-1). IncumbentEngagementClasses
// groups them for the callers (the sweep list, the association-target table)
// that treat the whole engagement family uniformly.
const (
	IncumbentClassContacts  = "contacts"
	IncumbentClassCompanies = "companies"
	IncumbentClassDeals     = "deals"
	IncumbentClassLeads     = "leads"
	IncumbentClassCalls     = "calls"
	IncumbentClassMeetings  = "meetings"
	IncumbentClassEmails    = "emails"
	IncumbentClassNotes     = "notes"
	IncumbentClassTasks     = "tasks"
)

// incumbentEngagementClasses is the private source of truth for the five v3
// engagement object classes, in a stable order, that all map onto the
// canonical "activity" type. It is private so no other package can mutate the
// shared set; external callers get a fresh copy via IncumbentEngagementClasses.
var incumbentEngagementClasses = []string{
	IncumbentClassCalls, IncumbentClassMeetings, IncumbentClassEmails, IncumbentClassNotes, IncumbentClassTasks,
}

// IncumbentEngagementClasses returns the five engagement object classes in
// their stable order — a FRESH copy each call, so a caller (the sweep list,
// the association-target table) can range over it without any risk of
// mutating the shared set.
func IncumbentEngagementClasses() []string {
	return slices.Clone(incumbentEngagementClasses)
}

// Record is one incumbent-CRM object as read through the Incumbent seam:
// the mirror ingest maps it into the workspace's mirrored cache row.
type Record struct {
	ExternalID      string
	ObjectClass     string
	Fields          map[string]any
	ModifiedAt      time.Time
	OwnerExternalID string
}

// Deletion is one incumbent-CRM record reported deleted or archived
// through the Incumbent seam. Continuous sync purges its mirrored cache
// row — with its association edges and visibility projection — on
// observing it, so an incumbent-side deletion stops being readable from
// the mirror rather than lingering visible until disconnect.
//
// ObjectClass MUST be the CANONICAL Margince class (the adapter mapping's
// Target — e.g. "person", "deal"), the same value the mirror row is keyed
// by, NOT the incumbent's own source name ("contacts", "deals"). An
// adapter that returned the raw incumbent class here would make PurgeRecord
// key a class no mirror row uses and silently purge nothing — the same
// canonical-not-incumbent rule mapRecord obeys for Record.ObjectClass.
type Deletion struct {
	ExternalID  string
	ObjectClass string
	DeletedAt   time.Time
}

// DeletionPage is one page of Deletions results. NextCursor is "" when
// there is no further page.
type DeletionPage struct {
	Deletions  []Deletion
	NextCursor string
}

// Assoc is one incumbent-CRM association edge between two records.
type Assoc struct {
	FromType  string
	FromID    string
	ToType    string
	ToID      string
	TypeID    int
	Category  string
	Label     string
	Direction string
}

// Page is one page of Backfill/Modified results. NextCursor is "" when
// there is no further page.
type Page struct {
	Records    []Record
	NextCursor string
}

// OwnerRef is one entry of the incumbent's owners directory: an owner's
// stable incumbent-side id and current email. The pair is what
// mirror_user_map seeding matches against workspace app_user emails
// (design.md §4.6: a MATCH against existing users, never an import that
// creates them).
type OwnerRef struct {
	ExternalID string
	Email      string
}

// Incumbent is the inner seam the overlay module reaches every specific
// incumbent CRM (HubSpot, …) through. It decouples mirror sync, budget,
// and conflict logic from any one incumbent's wire format.
type Incumbent interface {
	// Name identifies the incumbent implementation (e.g. "hubspot").
	Name() string
	// Backfill lists objectClass records id-cursor style, oldest sync
	// state first, for the initial full mirror load.
	Backfill(ctx context.Context, objectClass, cursor string) (Page, error)
	// Modified searches objectClass records modified at or after since,
	// ascending, watermark-cursor style, for incremental mirror sync.
	Modified(ctx context.Context, objectClass string, since time.Time, cursor string) (Page, error)
	// Deletions lists objectClass records deleted or archived in the
	// incumbent at or after since, cursor-paged, for the continuous-sync
	// removal feed — the mirror purges each so an incumbent-side deletion
	// stops being readable rather than lingering until disconnect.
	Deletions(ctx context.Context, objectClass string, since time.Time, cursor string) (DeletionPage, error)
	// Get fetches one record's current incumbent-side state (the record
	// clock a force-fresh read-through reads).
	Get(ctx context.Context, objectClass, externalID string) (Record, error)
	// Associations lists the association edges from one record to every
	// record of toClass it is linked to.
	Associations(ctx context.Context, fromClass, fromID, toClass string) ([]Assoc, error)
	// OwnerEmail resolves an incumbent-side owner reference to an email
	// address.
	OwnerEmail(ctx context.Context, ownerExternalID string) (string, error)
	// Owners lists the incumbent's full owners directory (id → email) —
	// the population mirror_user_map seeding matches against the
	// workspace's app_user rows.
	Owners(ctx context.Context) ([]OwnerRef, error)
}
