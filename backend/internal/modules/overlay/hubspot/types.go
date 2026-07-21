// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// This file holds the public result shapes every client.go/search.go/
// records.go endpoint returns — kept separate from the request/transport
// and wire-decode types so a caller can see the whole public surface at
// a glance.

package hubspot

// Property is a HubSpot object's properties map: design §11 notes every
// value is a string (numbers/dates ISO-stringified; empty is "" or
// null) — the mapper, not this client, parses typed values from these.
type Property = map[string]string

// ObjectRecord is one HubSpot CRM object as returned by List/Search/
// BatchRead (design §11's object-read shape).
type ObjectRecord struct {
	ID         string
	Properties Property
	CreatedAt  string
	UpdatedAt  string
	Archived   bool
}

// Page is one page of List results: NextAfter is "" when there is no
// further page (design §11's paging.next.after).
type Page struct {
	Results   []ObjectRecord
	NextAfter string
}

// SearchPage is one page of Search results: Total is the query's overall
// match count (design §11 — List has no such count; Search does, and the
// mirror sweep uses it for backfill progress).
type SearchPage struct {
	Total     int
	Results   []ObjectRecord
	NextAfter string
}

// AssociationType is one HubSpot v4 association edge label (design §11's
// associationTypes entry).
type AssociationType struct {
	Category string
	TypeID   int
	Label    string
}

// Association is one v4 association edge from the queried record to
// ToObjectID, carrying every type label HubSpot has assigned the edge.
type Association struct {
	ToObjectID string
	Types      []AssociationType
}

// Owner is a HubSpot CRM owner (the Owners API), enough to resolve
// hubspot_owner_id to an email for mirror_user_map (design §4.3).
type Owner struct {
	ID        string
	Email     string
	FirstName string
	LastName  string
}
