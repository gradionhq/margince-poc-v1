// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The mirror-record → typed-contract assembly (design.md §4.1/§4.6): the
// ONE place an overlay datasource.Record becomes a Person/Organization/
// Deal/Lead/Activity wire struct for the human REST surface. Field-picking
// out of the mirror's canonical jsonb payload lives in overlaywirefields.go;
// this file is the struct-shaping on top of it. Every struct is stamped
// `source: overlay`, the FULL canonical payload rides `raw` (nothing the
// mapper landed is dropped just because a typed slot doesn't exist for
// it), and a timestamp is the incumbent's own wherever the mapping mirrors
// one: a person's and an organization's created_at is the incumbent's create
// instant and its updated_at the incumbent's last-modified instant, each
// falling back to the mirror's own last-synced instant — the only time the
// mirror can claim for itself — where the incumbent stamped none. The other
// three entities read the mirror's own last-synced instant into both slots,
// even where the mapping landed the incumbent's (#1016).

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"strings"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// overlaySource is the Source stamp every mirror-assembled wire struct
// carries — the contract's provenance field, matching the T2 tier the
// search surface tags (external ≠ authoritative).
const overlaySource = "overlay"

// overlayUnnamed is the honest display fallback for a record the
// incumbent kept nameless — the contract requires a display name, and a
// fabricated one would be worse than a labeled absence.
const overlayUnnamed = "Unnamed"

// overlayCapturedByValue is the Provenance captured_by every mirror-assembled
// wire struct carries. captured_by is a REQUIRED field on all five entity
// schemas, so omitting it made every overlay-served body schema-invalid.
//
// The vocabulary is `human:<uuid> | agent:<id> | connector:<name>`, and the
// producer of a mirrored row is neither a human nor an agent of ours — it is
// the incumbent mirror. The name stays "overlay" rather than the specific
// incumbent because a mirror record carries no incumbent identity for the
// mapper to read, and naming one it cannot verify would be a guess.
const overlayCapturedByValue = "connector:overlay"

// overlayCanonicalLastModified is the canonical payload key holding the
// incumbent's own last-modified instant: the mirror's structural slot for a
// mapping's Baseline property, which overlay.Apply writes under this name. It
// is NOT the mirror row's ingest time — that one is Record.Freshness.
// LastSyncedAt, stamped now() by every upsert, so reading it for a record's
// updated_at reports the whole workspace as modified at one instant.
const overlayCanonicalLastModified = "last_synced_at"

// overlayRecordFields decodes a mirror record's canonical jsonb payload.
// A record the overlay provider served always carries an object payload;
// a decode failure is a real defect (the provider marshaled this very
// shape), surfaced, never papered over with an empty map.
func overlayRecordFields(rec datasource.Record) (map[string]any, error) {
	var fields map[string]any
	if err := json.Unmarshal(rec.Fields, &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

// overlayWirePerson assembles the contract Person from a mirror record.
// full_name is the canonical value the mapper assembled (OVA-MAP-3:
// first+last → email local part → placeholder); it is reused as-is so the
// wire cannot diverge from the mirror, with a first+last → email → "Unnamed"
// re-derivation kept only as a fallback for a pre-mapping mirror row that
// carries no full_name. The emails and phones collections are the mirrored
// child rows themselves — the type, the primary flag and the order are the
// mapping's own declarations, carried across rather than assumed, save for a
// declared type outside the contract's enum, which reads as the work type one
// mapped address or number means rather than shipping an invalid value — with
// only the contract-required row id synthesized, since a mirrored child row has
// no native row of its own to carry one.
func overlayWirePerson(ctx context.Context, rec datasource.Record) (crmcontracts.Person, error) {
	fields, err := overlayRecordFields(rec)
	if err != nil {
		return crmcontracts.Person{}, err
	}
	syncedAt := rec.Freshness.LastSyncedAt
	// Prefer the canonical full_name the mapping already assembled (OVA-MAP-3:
	// firstname+lastname → email local part → stable placeholder) so the wire
	// value cannot diverge from what was mirrored. The re-derivation below is
	// only a fallback for a mirror row that predates the full_name mapping.
	fullName := fieldString(fields, "full_name")
	if fullName == "" {
		fullName = strings.TrimSpace(strings.TrimSpace(fieldString(fields, "first_name")) + " " + strings.TrimSpace(fieldString(fields, "last_name")))
	}
	if fullName == "" {
		fullName = overlayPersonEmail(fields)
	}
	if fullName == "" {
		fullName = overlayUnnamed
	}
	personID := openapi_types.UUID(rec.Ref.ID)
	return crmcontracts.Person{
		Id:         personID,
		Source:     overlaySource,
		CapturedBy: ptrString(overlayCapturedByValue),
		FullName:   fullName,
		FirstName:  fieldStringPtr(fields, "first_name"),
		LastName:   fieldStringPtr(fields, "last_name"),
		Title:      fieldStringPtr(fields, "title"),
		Address:    overlayAddress(fields),
		Emails:     overlayPersonEmails(personID, fields),
		Phones:     overlayPersonPhones(personID, fields),
		CreatedAt:  overlayTimeOr(fields, "created_at", syncedAt),
		UpdatedAt:  overlayTimeOr(fields, overlayCanonicalLastModified, syncedAt),
		Raw:        &fields,
	}, nil
}

// overlayWireOrganization assembles the contract Organization from a
// mirror record. size_band rides only when it lands on the contract's
// own enum (the mapper's transform already targets those band labels);
// an off-enum value stays in raw rather than shipping an invalid enum.
// The address is the mapper's own address_json assembly, already spelled in
// the contract's member vocabulary, so it is shaped rather than re-derived.
func overlayWireOrganization(ctx context.Context, rec datasource.Record) (crmcontracts.Organization, error) {
	fields, err := overlayRecordFields(rec)
	if err != nil {
		return crmcontracts.Organization{}, err
	}
	syncedAt := rec.Freshness.LastSyncedAt
	displayName := strings.TrimSpace(fieldString(fields, "display_name"))
	if displayName == "" {
		displayName = overlayUnnamed
	}
	org := crmcontracts.Organization{
		Id:          openapi_types.UUID(rec.Ref.ID),
		Source:      overlaySource,
		CapturedBy:  ptrString(overlayCapturedByValue),
		DisplayName: displayName,
		Industry:    fieldStringPtr(fields, "industry"),
		Address:     overlayAddress(fields),
		CreatedAt:   overlayTimeOr(fields, "created_at", syncedAt),
		UpdatedAt:   overlayTimeOr(fields, overlayCanonicalLastModified, syncedAt),
		Raw:         &fields,
		// Stated rather than omitted: a mirror-backed organization is one of
		// the incumbent's accounts, and the installation's own company is a
		// native row that is never among them. Leaving the field absent would
		// make a client read "unknown" where the answer is known
		// (ADR-0082/A127).
		IsAnchor: ptrBool(false),
	}
	if band := crmcontracts.OrganizationSizeBand(fieldString(fields, "size_band")); band.Valid() {
		org.SizeBand = &band
	}
	if row, domain := overlayOrgDomainRow(fields); domain != "" {
		org.Domains = &[]crmcontracts.OrganizationDomain{{
			Id:         overlaySyntheticID(openapi_types.UUID(rec.Ref.ID), childRowPosition(row), domain),
			Domain:     domain,
			IsPrimary:  true,
			Source:     overlaySource,
			CapturedBy: ptrString(overlayCapturedByValue),
		}}
	}
	return org, nil
}

// overlaySyntheticID derives an id for a mirrored child row from its parent id,
// its declared position in that parent's collection, and its own value. An
// overlay child row has no native row of its own to carry an id and the
// contract requires one, so the identity the SPA keys its render on has to hold
// on two axes: DISTINCT for every row of one parent, and STABLE across reads of
// the same record. The value alone carries neither — a contact reachable on the
// same number as both work and mobile is ordinary data the native model permits
// — so the position joins it, which Apply keeps unique within a parent. Both
// leading inputs each end where the next begins — the parent id is a fixed
// 16-byte prefix and the position rides as a self-terminating varint — so no
// separator is needed to keep the triple unambiguous, the free-length value
// being last. The version/variant nibbles are stamped to RFC 9562 v8
// (application-defined — the honest label for a custom hash-derived id) so the
// value is a well-formed UUID. This layer stays off the
// `github.com/google/uuid` package by arch rule, so the bits are set by hand.
// Non-authoritative like every overlay wire value — it is never persisted or
// resolved back to a row.
func overlaySyntheticID(parent openapi_types.UUID, position int, value string) openapi_types.UUID {
	buf := make([]byte, 0, len(parent)+binary.MaxVarintLen64+len(value))
	buf = append(buf, parent[:]...)
	buf = binary.AppendVarint(buf, int64(position))
	buf = append(buf, value...)
	sum := sha256.Sum256(buf)
	var id openapi_types.UUID
	copy(id[:], sum[:])
	id[6] = (id[6] & 0x0f) | 0x80 // RFC 9562 version 8 (application-defined)
	id[8] = (id[8] & 0x3f) | 0x80 // RFC 4122 variant
	return id
}

// overlayWireDeal assembles the contract Deal from a mirror record.
// pipeline_id/stage_id are NULL in overlay mode (OVA-MAP-6): the contract
// makes them nullable ([string,'null']), and an overlay-mirror deal has no
// native Margince pipeline/stage row to reference — so the wire leaves both
// nil (never a fabricated/zero UUID, a forbidden dangling FK). The
// incumbent's own pipeline/dealstage identifiers ride raw. status is derived
// from HubSpot's canonical closed-stage keys (closedwon/closedlost); a custom
// pipeline's closed stages answer open until the stage-semantic derivation
// lands with the write path (branch 2).
func overlayWireDeal(ctx context.Context, rec datasource.Record) (crmcontracts.Deal, error) {
	fields, err := overlayRecordFields(rec)
	if err != nil {
		return crmcontracts.Deal{}, err
	}
	syncedAt := rec.Freshness.LastSyncedAt
	name := strings.TrimSpace(fieldString(fields, "name"))
	if name == "" {
		name = overlayUnnamed
	}
	deal := crmcontracts.Deal{
		Id:         openapi_types.UUID(rec.Ref.ID),
		Source:     overlaySource,
		CapturedBy: ptrString(overlayCapturedByValue),
		Name:       name,
		Currency:   fieldStringPtr(fields, "currency"),
		Status:     overlayDealStatus(fieldString(fields, "stage_id")),
		CreatedAt:  syncedAt,
		UpdatedAt:  syncedAt,
		Raw:        &fields,
	}
	if minor, ok := fieldInt64(fields, "amount_minor"); ok {
		deal.AmountMinor = &minor
	}
	if closeDate, ok := overlayTime(fields, "expected_close_date"); ok {
		deal.ExpectedCloseDate = &openapi_types.Date{Time: closeDate}
	}
	return deal, nil
}

// overlayDealStatus derives the contract DealStatus from HubSpot's
// canonical closed-stage keys.
func overlayDealStatus(stageKey string) crmcontracts.DealStatus {
	switch strings.ToLower(stageKey) {
	case "closedwon":
		return crmcontracts.DealStatusWon
	case "closedlost":
		return crmcontracts.DealStatusLost
	default:
		return crmcontracts.DealStatusOpen
	}
}

// overlayWireLead assembles the contract Lead from a mirror record.
// score/status are REQUIRED by the contract but unmapped in branch 1:
// 0 is the unscored floor and `new` the pipeline entry state — both the
// same defaults a native lead starts from, with the incumbent's own
// values (if any) in raw.
func overlayWireLead(ctx context.Context, rec datasource.Record) (crmcontracts.Lead, error) {
	fields, err := overlayRecordFields(rec)
	if err != nil {
		return crmcontracts.Lead{}, err
	}
	syncedAt := rec.Freshness.LastSyncedAt
	lead := crmcontracts.Lead{
		Id:          openapi_types.UUID(rec.Ref.ID),
		Source:      overlaySource,
		CapturedBy:  ptrString(overlayCapturedByValue),
		FullName:    fieldStringPtr(fields, "full_name"),
		CompanyName: fieldStringPtr(fields, "company_name"),
		Score:       0,
		Status:      crmcontracts.LeadStatusNew,
		CreatedAt:   syncedAt,
		UpdatedAt:   syncedAt,
		Raw:         &fields,
	}
	if email := strings.TrimSpace(fieldString(fields, "email")); email != "" {
		e := openapi_types.Email(email)
		lead.Email = &e
	}
	return lead, nil
}

// overlayWireActivity assembles the contract Activity from a mirror
// record. kind rides the mapper's lowercased engagement type when it
// lands on the contract enum; an engagement kind the contract doesn't
// know answers `note` (the semantically-empty timeline entry) with the
// true kind preserved in raw. occurred_at falls back to the sync
// instant when the incumbent stamped none. duration is deliberately NOT
// surfaced: HubSpot reports call duration in milliseconds and the
// branch-1 mapper stores it raw — labelling that value "seconds" would
// be a silent 1000× lie, so it stays in raw until the mapping grows the
// unit transform.
func overlayWireActivity(ctx context.Context, rec datasource.Record) (crmcontracts.Activity, error) {
	fields, err := overlayRecordFields(rec)
	if err != nil {
		return crmcontracts.Activity{}, err
	}
	syncedAt := rec.Freshness.LastSyncedAt
	kind := crmcontracts.ActivityKind(fieldString(fields, "kind"))
	if !kind.Valid() {
		kind = crmcontracts.ActivityKindNote
	}
	act := crmcontracts.Activity{
		Id:         openapi_types.UUID(rec.Ref.ID),
		Source:     overlaySource,
		CapturedBy: ptrString(overlayCapturedByValue),
		Kind:       kind,
		Subject:    fieldStringPtr(fields, "subject"),
		Body:       fieldStringPtr(fields, "body"),
		OccurredAt: overlayTimeOr(fields, "occurred_at", syncedAt),
		CreatedAt:  syncedAt,
		UpdatedAt:  syncedAt,
		Raw:        &fields,
	}
	if dir := crmcontracts.ActivityDirection(strings.ToLower(fieldString(fields, "direction"))); dir == crmcontracts.ActivityDirectionInbound || dir == crmcontracts.ActivityDirectionOutbound {
		act.Direction = &dir
	}
	if ms := crmcontracts.ActivityMeetingStatus(strings.ToLower(fieldString(fields, "meeting_status"))); ms.Valid() {
		act.MeetingStatus = &ms
	}
	// duration_seconds (meeting/call) is already stored in canonical seconds
	// by the ms_to_seconds mapping transform (OVA-MAP-2) — surface it as-is,
	// never re-divide.
	if secs, ok := fieldInt64(fields, "duration_seconds"); ok {
		d := int(secs)
		act.DurationSeconds = &d
	}
	// due_at (task) is the deadline the tasks mapping lands from hs_timestamp
	// (OVA-MAP-8); occurred_at above already comes from the task's creation.
	if due, ok := overlayTime(fields, "due_at"); ok {
		act.DueAt = &due
	}
	return act, nil
}

// overlayWireTitle is the search-hit display label per entity type — the
// same name the typed assembly above would lead with.
func overlayWireTitle(et datasource.EntityType, fields map[string]any) string {
	switch et {
	case datasource.EntityPerson:
		// Prefer the canonical full_name the mapping assembled (OVA-MAP-3), so
		// a search hit's title matches the person-detail full_name; re-derive
		// only for a pre-mapping mirror row that carries no full_name.
		if name := strings.TrimSpace(fieldString(fields, "full_name")); name != "" {
			return name
		}
		name := strings.TrimSpace(strings.TrimSpace(fieldString(fields, "first_name")) + " " + strings.TrimSpace(fieldString(fields, "last_name")))
		if name == "" {
			name = overlayPersonEmail(fields)
		}
		return name
	case datasource.EntityOrganization:
		return strings.TrimSpace(fieldString(fields, "display_name"))
	case datasource.EntityDeal:
		return strings.TrimSpace(fieldString(fields, "name"))
	case datasource.EntityLead:
		return strings.TrimSpace(fieldString(fields, "full_name"))
	case datasource.EntityActivity:
		return strings.TrimSpace(fieldString(fields, "subject"))
	default:
		return ""
	}
}
