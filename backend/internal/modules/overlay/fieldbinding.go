// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// The overlay field registry: for one canonical entity, what every contract
// field's relationship to the mirror IS. Three layers have to agree about a
// field — the incumbent mapping's target string, the mirror's jsonb key, and
// the wire assembly's field pick — and nothing but this declaration connects
// them. A field with no entry here is a field nobody decided about, which is
// how a mapping goes stale against a core model that moved underneath it.

// Disposition is why a contract field does or does not carry mirrored data.
type Disposition string

const (
	// DispositionMapped means the mirror carries this field; Incumbent names
	// the source properties.
	DispositionMapped Disposition = "mapped"
	// DispositionDeferred means the field is mappable from the incumbent but
	// deliberately out of scope for now. IssueURL is required — a deferral
	// nobody tracks is a gap that never closes.
	DispositionDeferred Disposition = "deferred"
	// DispositionUnmappable means the incumbent has no analogue at all.
	DispositionUnmappable Disposition = "unmappable"
	// DispositionNativeOnly means the field is derived or server-stamped, so
	// no mirror could ever supply it (a version counter, a relationship
	// strength computed from captured interactions).
	DispositionNativeOnly Disposition = "native_only"
)

// FieldBinding is one contract field's overlay disposition for one entity.
// CanonicalKey is the mirror's own jsonb key, which keeps the core column's
// spelling rather than the contract's where the two differ; it is empty
// unless the field is mapped.
type FieldBinding struct {
	WireSlot     string
	CanonicalKey string
	Incumbent    []string
	Transform    string
	Disposition  Disposition
	Reason       string
	IssueURL     string
}

// EntityBinding is one canonical entity's complete field disposition. Armed
// reports whether the exhaustive-coverage gate applies to it yet: an entity
// is armed only once every contract field it publishes has been decided, so
// the remaining entities stay visible in code rather than in a backlog note.
type EntityBinding struct {
	Entity   string
	Armed    bool
	Bindings []FieldBinding
}

// Field-name literals repeated across the bindings table below, named once
// so a future rename touches one declaration rather than every row that
// spells it out by hand. wireSlotAddress and canonicalKeyAddress name two
// genuinely different vocabularies that happen to share a spelling for this
// field — the contract's wire slot and the mirror's own jsonb key —
// and incumbentAddress/incumbentEmail name the incumbent's own source-
// property spelling, a third vocabulary again.
const (
	wireSlotAddress     = "address"
	canonicalKeyAddress = "address"
	incumbentAddress    = "address"
	incumbentEmail      = "email"
)

// fieldFullName names the entity's assembled display name — the wire slot,
// the mirror's canonical key, and the transform registered under that same
// name in transforms.go are one concept seen from three call sites, so one
// constant keeps a rename from touching only some of them.
const fieldFullName = "full_name"

// FieldBindings is the registry. Every gate derives from this one slice.
func FieldBindings() []EntityBinding {
	return []EntityBinding{personBindings}
}

// BindingsFor resolves one canonical entity's bindings. An entity the
// registry never declared is an honest miss (ok=false), never an empty
// EntityBinding a caller would read as "nothing to check".
func BindingsFor(entity string) (EntityBinding, bool) {
	for _, e := range FieldBindings() {
		if e.Entity == entity {
			return e, true
		}
	}
	return EntityBinding{}, false
}

// personBindings disposition every contract Person field. Unarmed until the
// wire actually delivers what the mapped rows below claim.
var personBindings = EntityBinding{
	Entity: "person",
	Armed:  true,
	Bindings: []FieldBinding{
		{WireSlot: "first_name", CanonicalKey: "first_name", Incumbent: []string{"firstname"}, Disposition: DispositionMapped},
		{WireSlot: "last_name", CanonicalKey: "last_name", Incumbent: []string{"lastname"}, Disposition: DispositionMapped},
		{WireSlot: fieldFullName, CanonicalKey: fieldFullName, Incumbent: []string{"firstname", "lastname", incumbentEmail}, Transform: fieldFullName, Disposition: DispositionMapped},
		{WireSlot: "title", CanonicalKey: "title", Incumbent: []string{"jobtitle"}, Disposition: DispositionMapped},
		{WireSlot: wireSlotAddress, CanonicalKey: canonicalKeyAddress, Incumbent: []string{incumbentAddress, "city", "state", "zip", "country"}, Transform: "address_json", Disposition: DispositionMapped},
		{WireSlot: "owner_id", CanonicalKey: "owner_id", Incumbent: []string{"hubspot_owner_id"}, Disposition: DispositionMapped},
		{WireSlot: "emails", CanonicalKey: "person_email", Incumbent: []string{incumbentEmail}, Transform: "lowercase", Disposition: DispositionMapped},
		{WireSlot: "phones", CanonicalKey: "person_phone", Incumbent: []string{"phone", "mobilephone"}, Disposition: DispositionMapped},
		{WireSlot: "created_at", CanonicalKey: "created_at", Incumbent: []string{"createdate"}, Disposition: DispositionMapped},
		{WireSlot: "updated_at", CanonicalKey: "last_synced_at", Incumbent: []string{"lastmodifieddate"}, Disposition: DispositionMapped},

		{WireSlot: "social", Disposition: DispositionDeferred, IssueURL: "https://github.com/gradionhq/margince-poc-v1/issues/985"},
		{WireSlot: "archived_at", Disposition: DispositionDeferred, IssueURL: "https://github.com/gradionhq/margince-poc-v1/issues/986"},

		{
			WireSlot: "consent", Disposition: DispositionUnmappable,
			Reason: "Consent is per-purpose and demonstrable from this installation's own proof log; an incumbent's flag cannot stand in for it.",
		},

		{
			WireSlot: "id", Disposition: DispositionNativeOnly,
			Reason: "Bridged from the incumbent's own object id by externalIDToUUID, not carried as a mirrored field.",
		},
		{
			WireSlot: "workspace_id", Disposition: DispositionNativeOnly,
			Reason: "Stamped from the request's own workspace; the mirror row's workspace is that workspace by construction.",
		},
		{
			WireSlot: "source", Disposition: DispositionNativeOnly,
			Reason: "Always the overlay provenance stamp; a mirrored record has exactly one source.",
		},
		{
			WireSlot: "captured_by", Disposition: DispositionNativeOnly,
			Reason: "Always connector:overlay — a mirror record carries no incumbent identity to name instead.",
		},
		{
			WireSlot: "raw", Disposition: DispositionNativeOnly,
			Reason: "The full canonical payload itself; it cannot be one field within that payload.",
		},
		{
			WireSlot: "version", Disposition: DispositionNativeOnly,
			Reason: "An optimistic-concurrency counter over native rows; the mirror holds no row to version.",
		},
		{
			WireSlot: "reachability", Disposition: DispositionNativeOnly,
			Reason: "Read-only, derived from this installation's own channel identities.",
		},
		{
			WireSlot: "strength", Disposition: DispositionNativeOnly,
			Reason: "Derived from captured interactions; the mirror holds no interaction history.",
		},
		{
			WireSlot: "merged_into_id", Disposition: DispositionNativeOnly,
			Reason: "Merge is a native operation over native rows; a mirrored record is never merged away.",
		},
		{
			WireSlot: "converted_from_lead_id", Disposition: DispositionNativeOnly,
			Reason: "Lead conversion is a native operation; a mirrored person has no native lead to point back to.",
		},
	},
}
