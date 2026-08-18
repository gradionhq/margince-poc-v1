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
	// DispositionDerived means the mirror carries this slot's INPUTS and the
	// wire computes the slot from them, so it reads no canonical key of its
	// own. It exists because the alternative spellings are both false: mapped
	// would claim a key a second slot already reads, and the registry rejects
	// two slots claiming one key precisely so a real double-write cannot hide;
	// native_only would say no mirror could help, when the mirror is exactly
	// where the value comes from. The line between the two is whose data the
	// computation runs over — native_only computes from THIS installation's
	// own rows, derived computes from mirrored ones. DerivedFrom names the
	// wire slots it is computed from, and each must be mapped on the same
	// entity: a slot derived from something the mirror does not carry is
	// native_only wearing a friendlier name. What the gates reach stops
	// there, and an author declaring one should know it: they prove the named
	// sources are mapped and that the slot's value follows from the mirrored
	// payload, but nothing proves the computation reads THOSE sources — a slot
	// computed from other mirrored data passes both. The list is a claim about
	// the code that has to be kept true by reading it.
	DispositionDerived Disposition = "derived"
)

// FieldBinding is one contract field's overlay disposition for one entity.
// CanonicalKey is the mirror's own jsonb key, which keeps the core column's
// spelling rather than the contract's where the two differ; it is empty
// unless the field is mapped. DerivedFrom names the wire slots a derived
// field is computed from — slots, not canonical keys, so the dependency is
// stated in the vocabulary the wire assembly itself reads back.
type FieldBinding struct {
	WireSlot     string
	CanonicalKey string
	Incumbent    []string
	DerivedFrom  []string
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

// FieldBindings is the registry. Every gate derives from this one slice.
func FieldBindings() []EntityBinding {
	return []EntityBinding{personBindings, organizationBindings, dealBindings, leadBindings, activityBindings}
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

// mirrorStructuralBindings are the contract fields whose disposition follows
// from what a mirror IS, not from which entity is being mirrored: an identity
// bridged from the incumbent's object id, the provenance stamp the overlay
// writes on everything it serves, an incumbent owner id nothing resolves to an
// app_user, and the native machinery — versioning, merge, derived strength —
// that acts on native rows a mirror does not have. Every mirrored entity owes
// the same answer, so it is stated once: declared per entity, two entities
// could answer one structural question two ways and both look deliberate.
func mirrorStructuralBindings() []FieldBinding {
	return []FieldBinding{
		{
			WireSlot: "owner_id", Disposition: DispositionDeferred,
			Reason:   "The mirror holds the incumbent's own owner id, which row visibility is projected from; nothing joins it through mirror_user_map to the app_user the contract's uuid slot names.",
			IssueURL: "https://github.com/gradionhq/margince-poc-v1/issues/994",
		},
		{
			WireSlot: "id", Disposition: DispositionNativeOnly,
			Reason: "Bridged from the incumbent's own object id by externalIDToUUID, not carried as a mirrored field.",
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
			WireSlot: "strength", Disposition: DispositionNativeOnly,
			Reason: "Derived from captured interactions; the mirror holds no interaction history.",
		},
		{
			WireSlot: "last_activity_at", Disposition: DispositionNativeOnly,
			Reason: "Derived from this installation's own timeline; the mirror holds no interaction history.",
		},
		{
			WireSlot: "merged_into_id", Disposition: DispositionNativeOnly,
			Reason: "Merge is a native operation over native rows; a mirrored record is never merged away.",
		},
	}
}

// personBindings disposition every contract Person field. Armed: the coverage
// gate holds this entity to accounting for every field the contract publishes
// on it, in both directions.
//
//nolint:goconst // the rows are read as data, and each column is its own vocabulary: a wire slot, the mirror's jsonb key and an incumbent property spell "address" alike here by coincidence, so hiding any of them behind one shared name would assert a correspondence the table exists to keep separate
var personBindings = EntityBinding{
	Entity: "person",
	Armed:  true,
	Bindings: append([]FieldBinding{
		{WireSlot: "first_name", CanonicalKey: "first_name", Incumbent: []string{"firstname"}, Disposition: DispositionMapped},
		{WireSlot: "last_name", CanonicalKey: "last_name", Incumbent: []string{"lastname"}, Disposition: DispositionMapped},
		{WireSlot: "full_name", CanonicalKey: "full_name", Incumbent: []string{"firstname", "lastname", "email"}, Transform: "full_name", Disposition: DispositionMapped},
		{WireSlot: "title", CanonicalKey: "title", Incumbent: []string{"jobtitle"}, Disposition: DispositionMapped},
		{WireSlot: "address", CanonicalKey: "address", Incumbent: []string{"address", "city", "state", "zip", "country"}, Transform: "address_json", Disposition: DispositionMapped},
		{WireSlot: "emails", CanonicalKey: "person_email", Incumbent: []string{"email"}, Transform: "lowercase", Disposition: DispositionMapped},
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
			WireSlot: "reachability", Disposition: DispositionNativeOnly,
			Reason: "Read-only, derived from this installation's own channel identities.",
		},
		{
			WireSlot: "converted_from_lead_id", Disposition: DispositionNativeOnly,
			Reason: "Lead conversion is a native operation; a mirrored person has no native lead to point back to.",
		},
	}, mirrorStructuralBindings()...),
}

// organizationBindings disposition every contract Organization field. Armed,
// on the same terms personBindings is.
//
// Six fields the incumbent could fill are deferred rather than mapped, and
// each names the reason it is not a one-line addition: one needs a projection
// that does not exist yet (an association sweep for the parent), four need a
// decision the mapping cannot make on its own (a length rule, a URL
// normalization, two remaps onto vocabularies this product defines on its own
// axes), and one needs a read the adapter does not perform (the archive flag).
//
//nolint:goconst // the rows are read as data, and each column is its own vocabulary: a wire slot, the mirror's jsonb key and an incumbent property spell "address" and "industry" alike here by coincidence, so hiding any of them behind one shared name would assert a correspondence the table exists to keep separate
var organizationBindings = EntityBinding{
	Entity: "organization",
	Armed:  true,
	Bindings: append([]FieldBinding{
		{WireSlot: "display_name", CanonicalKey: "display_name", Incumbent: []string{"name"}, Disposition: DispositionMapped},
		{WireSlot: "industry", CanonicalKey: "industry", Incumbent: []string{"industry"}, Disposition: DispositionMapped},
		{WireSlot: "size_band", CanonicalKey: "size_band", Incumbent: []string{"numberofemployees"}, Transform: "employees_to_size_band", Disposition: DispositionMapped},
		{WireSlot: "address", CanonicalKey: "address", Incumbent: []string{"address", "city", "state", "zip", "country"}, Transform: "address_json", Disposition: DispositionMapped},
		{WireSlot: "domains", CanonicalKey: "organization_domain", Incumbent: []string{"domain"}, Transform: "lowercase", Disposition: DispositionMapped},
		{WireSlot: "created_at", CanonicalKey: "created_at", Incumbent: []string{"createdate"}, Disposition: DispositionMapped},
		{WireSlot: "updated_at", CanonicalKey: "last_synced_at", Incumbent: []string{"hs_lastmodifieddate"}, Disposition: DispositionMapped},

		{WireSlot: "website_url", Disposition: DispositionDerived, DerivedFrom: []string{"domains"}},

		{WireSlot: "parent_org_id", Disposition: DispositionDeferred, IssueURL: "https://github.com/gradionhq/margince-poc-v1/issues/1023"},
		{WireSlot: "archived_at", Disposition: DispositionDeferred, IssueURL: "https://github.com/gradionhq/margince-poc-v1/issues/1024"},
		{WireSlot: "description", Disposition: DispositionDeferred, IssueURL: "https://github.com/gradionhq/margince-poc-v1/issues/1026"},
		{WireSlot: "linkedin_url", Disposition: DispositionDeferred, IssueURL: "https://github.com/gradionhq/margince-poc-v1/issues/1027"},
		{WireSlot: "lifecycle", Disposition: DispositionDeferred, IssueURL: "https://github.com/gradionhq/margince-poc-v1/issues/1028"},
		{WireSlot: "relationship_types", Disposition: DispositionDeferred, IssueURL: "https://github.com/gradionhq/margince-poc-v1/issues/1031"},

		{
			WireSlot: "legal_name", Disposition: DispositionUnmappable,
			Reason: "HubSpot publishes no legal-entity name distinct from the company name, so the only value available would restate display_name.",
		},

		{
			WireSlot: "is_anchor", Disposition: DispositionNativeOnly,
			Reason: "A mirrored company is one of the incumbent's accounts; this installation's own company is a native row never among them.",
		},
		{
			WireSlot: "computed_fields", Disposition: DispositionNativeOnly,
			Reason: "Evaluated from this installation's own formula definitions over native rows.",
		},
		{
			WireSlot: "contact_count", Disposition: DispositionNativeOnly,
			Reason: "Counted from this installation's own employment edges; the mirror's roster is not the native one.",
		},
		{
			WireSlot: "open_deal_count", Disposition: DispositionNativeOnly,
			Reason: "Counted from native deal rows, the same rows computed_fields' open pipeline sums.",
		},
		{
			WireSlot: "classification", Disposition: DispositionNativeOnly,
			Reason: "Retired by ADR-0079/A124 and written by nothing; it survives only so a native row's pre-migration value can be compared, and a mirrored record has no such value.",
		},
		{
			WireSlot: "logo_url", Disposition: DispositionNativeOnly,
			Reason: "The getOrganizationLogo path for this record, which streams a resolved asset out of this installation's own object storage; the mirror holds no asset there.",
		},
		{
			WireSlot: "partner", Disposition: DispositionNativeOnly,
			Reason: "A native partner extension row; a mirrored company has none.",
		},
	}, mirrorStructuralBindings()...),
}

// dealBindings is unarmed: pipeline and stage are this product's own
// semantics, so what an incumbent's deal offers is a reconciliation of its
// own rather than a row-by-row disposition.
var dealBindings = EntityBinding{Entity: "deal"}

// leadBindings is unarmed: the lead mapping's targets were chosen before this
// registry existed, so each one has to be re-decided against the contract
// before an exhaustive claim about them would be true.
var leadBindings = EntityBinding{Entity: "lead"}

// activityBindings is unarmed: an activity spans five incumbent engagement
// classes, and a disposition that held for one of them would say nothing
// about the other four.
var activityBindings = EntityBinding{Entity: "activity"}
