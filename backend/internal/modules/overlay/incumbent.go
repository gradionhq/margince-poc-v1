// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

import (
	"context"
	"time"
)

// The five HubSpot incumbent object classes design.md §9 maps
// (hubspot/mapping_hs.go's own objectClass* constants restate these —
// duplicated across the seam boundary rather than exported from
// hubspot, since a module never imports a sibling's internal naming,
// and this package's own callers (backfill.go's association-target
// table, compose/jobs.go's reconcile sweep) all need the same
// incumbent vocabulary spelled once on THIS side of the seam).
const (
	IncumbentClassContacts    = "contacts"
	IncumbentClassCompanies   = "companies"
	IncumbentClassDeals       = "deals"
	IncumbentClassEngagements = "engagements"
	IncumbentClassLeads       = "leads"
)

// Record is one incumbent-CRM object as read through the Incumbent seam:
// the mirror ingest maps it into the workspace's mirrored cache row.
type Record struct {
	ExternalID      string
	ObjectClass     string
	Fields          map[string]any
	ModifiedAt      time.Time
	OwnerExternalID string
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
