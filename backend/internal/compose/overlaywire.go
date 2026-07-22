// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The mirror-record → typed-contract assembly (design.md §4.1/§4.6): the
// ONE place an overlay datasource.Record becomes a Person/Organization/
// Deal/Lead/Activity wire struct for the human REST surface. The mirror
// holds canonical-named fields in one jsonb payload (the mapping
// adapter's targets, hubspot/mapping_hs.go), so assembly here is
// field-picking, not translation. Every struct is stamped
// `source: overlay`, the FULL canonical payload rides `raw` (nothing the
// mapper landed is dropped just because a typed slot doesn't exist for
// it), and both timestamps carry the mirror's own last-synced instant —
// the only time the mirror can honestly claim (the incumbent's
// create/update instants are not mapped in branch 1).

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
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
// full_name is derived first+last (the mapper lands the parts, not the
// join); a person the incumbent kept nameless falls back to their mapped
// email, then to the honest "Unnamed". Structured child rows (emails,
// phones) are NOT fabricated: the contract's PersonEmail demands a row
// identity/type/position the mirror does not hold, so the mapped email
// stays in raw rather than riding a made-up child row.
func overlayWirePerson(ctx context.Context, rec datasource.Record) (crmcontracts.Person, error) {
	fields, err := overlayRecordFields(rec)
	if err != nil {
		return crmcontracts.Person{}, err
	}
	wsID, err := overlayWorkspaceID(ctx)
	if err != nil {
		return crmcontracts.Person{}, err
	}
	syncedAt := rec.Freshness.LastSyncedAt
	fullName := strings.TrimSpace(strings.TrimSpace(fieldString(fields, "first_name")) + " " + strings.TrimSpace(fieldString(fields, "last_name")))
	if fullName == "" {
		fullName = overlayPersonEmail(fields)
	}
	if fullName == "" {
		fullName = overlayUnnamed
	}
	return crmcontracts.Person{
		Id:          openapi_types.UUID(rec.Ref.ID),
		WorkspaceId: wsID,
		Source:      overlaySource,
		FullName:    fullName,
		FirstName:   fieldStringPtr(fields, "first_name"),
		LastName:    fieldStringPtr(fields, "last_name"),
		Title:       fieldStringPtr(fields, "title"),
		CreatedAt:   syncedAt,
		UpdatedAt:   syncedAt,
		Raw:         &fields,
	}, nil
}

// overlayWireOrganization assembles the contract Organization from a
// mirror record. size_band rides only when it lands on the contract's
// own enum (the mapper's transform already targets those band labels);
// an off-enum value stays in raw rather than shipping an invalid enum.
func overlayWireOrganization(ctx context.Context, rec datasource.Record) (crmcontracts.Organization, error) {
	fields, err := overlayRecordFields(rec)
	if err != nil {
		return crmcontracts.Organization{}, err
	}
	wsID, err := overlayWorkspaceID(ctx)
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
		WorkspaceId: wsID,
		Source:      overlaySource,
		DisplayName: displayName,
		Industry:    fieldStringPtr(fields, "industry"),
		CreatedAt:   syncedAt,
		UpdatedAt:   syncedAt,
		Raw:         &fields,
	}
	if band := crmcontracts.OrganizationSizeBand(fieldString(fields, "size_band")); band.Valid() {
		org.SizeBand = &band
	}
	return org, nil
}

// overlayWireDeal assembles the contract Deal from a mirror record.
// pipeline_id/stage_id are REQUIRED as row UUIDs by the contract, but
// the mirror holds the incumbent's own string identifiers (HubSpot
// pipeline/stage keys) that reference no native row — they ride raw,
// and the UUID slots stay zero: an honest "no native pipeline/stage row
// exists in overlay mode", never a fabricated reference. status is
// derived from HubSpot's canonical closed-stage keys (closedwon/
// closedlost); a custom pipeline's closed stages answer open until the
// design §9 stage-semantic derivation lands with the write path.
func overlayWireDeal(ctx context.Context, rec datasource.Record) (crmcontracts.Deal, error) {
	fields, err := overlayRecordFields(rec)
	if err != nil {
		return crmcontracts.Deal{}, err
	}
	wsID, err := overlayWorkspaceID(ctx)
	if err != nil {
		return crmcontracts.Deal{}, err
	}
	syncedAt := rec.Freshness.LastSyncedAt
	name := strings.TrimSpace(fieldString(fields, "name"))
	if name == "" {
		name = overlayUnnamed
	}
	deal := crmcontracts.Deal{
		Id:          openapi_types.UUID(rec.Ref.ID),
		WorkspaceId: wsID,
		Source:      overlaySource,
		Name:        name,
		Currency:    fieldStringPtr(fields, "currency"),
		Status:      overlayDealStatus(fieldString(fields, "stage_id")),
		CreatedAt:   syncedAt,
		UpdatedAt:   syncedAt,
		Raw:         &fields,
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
	wsID, err := overlayWorkspaceID(ctx)
	if err != nil {
		return crmcontracts.Lead{}, err
	}
	syncedAt := rec.Freshness.LastSyncedAt
	lead := crmcontracts.Lead{
		Id:          openapi_types.UUID(rec.Ref.ID),
		WorkspaceId: wsID,
		Source:      overlaySource,
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
	wsID, err := overlayWorkspaceID(ctx)
	if err != nil {
		return crmcontracts.Activity{}, err
	}
	syncedAt := rec.Freshness.LastSyncedAt
	kind := crmcontracts.ActivityKind(fieldString(fields, "kind"))
	if !kind.Valid() {
		kind = crmcontracts.ActivityKindNote
	}
	occurredAt := syncedAt
	if ts, ok := overlayTime(fields, "occurred_at"); ok {
		occurredAt = ts
	}
	act := crmcontracts.Activity{
		Id:          openapi_types.UUID(rec.Ref.ID),
		WorkspaceId: wsID,
		Source:      overlaySource,
		Kind:        kind,
		Subject:     fieldStringPtr(fields, "subject"),
		Body:        fieldStringPtr(fields, "body"),
		OccurredAt:  occurredAt,
		CreatedAt:   syncedAt,
		UpdatedAt:   syncedAt,
		Raw:         &fields,
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

// overlayPersonEmail digs the mapped email out of the person_email child
// payload (the mapper's "person_email.email" child target lands as a
// nested object in the canonical fields).
func overlayPersonEmail(fields map[string]any) string {
	child, ok := fields["person_email"].(map[string]any)
	if !ok {
		return ""
	}
	email, ok := child["email"].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(email)
}

// overlayWorkspaceID stamps the caller's own workspace. The overlay read
// path only ever runs under a workspace-bound ctx (isOverlay answered
// true, which requires one) — an unbound ctx here is a broken invariant
// and surfaces as an error, never a zero-workspace stamp on the wire.
func overlayWorkspaceID(ctx context.Context) (openapi_types.UUID, error) {
	wsID, ok := principal.WorkspaceID(ctx)
	if !ok {
		return openapi_types.UUID{}, errors.New("overlay wire: no workspace bound to the request context")
	}
	return openapi_types.UUID(wsID), nil
}

// fieldString answers the string value of a canonical field, "" when
// absent or non-string.
func fieldString(fields map[string]any, key string) string {
	s, _ := fields[key].(string)
	return s
}

// fieldStringPtr answers a trimmed non-empty string field as a pointer,
// nil otherwise — optional wire slots stay absent, never "".
func fieldStringPtr(fields map[string]any, key string) *string {
	s := strings.TrimSpace(fieldString(fields, key))
	if s == "" {
		return nil
	}
	return &s
}

// fieldInt64 answers a numeric field as int64. JSON numbers decode as
// float64; a numeric string (HubSpot amounts arrive as strings) parses
// too. A fractional, non-finite, or int64-overflowing number answers
// absent (the raw payload keeps the true value) — a narrowed cast would
// silently invent a different amount.
func fieldInt64(fields map[string]any, key string) (int64, bool) {
	switch v := fields[key].(type) {
	case float64:
		if !isExactInt64(v) {
			return 0, false
		}
		return int64(v), true
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

// overlayTime parses a canonical timestamp field. HubSpot stamps arrive
// as RFC 3339, date-only, or epoch-milliseconds — each is tried; an
// unparseable stamp answers absent (the value stays in raw) rather than
// a fabricated instant.
func overlayTime(fields map[string]any, key string) (time.Time, bool) {
	switch v := fields[key].(type) {
	case string:
		s := strings.TrimSpace(v)
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t, true
		}
		if t, err := time.Parse("2006-01-02", s); err == nil {
			return t, true
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return time.UnixMilli(n).UTC(), true
		}
	case float64:
		if !isExactInt64(v) {
			return time.Time{}, false
		}
		return time.UnixMilli(int64(v)).UTC(), true
	}
	return time.Time{}, false
}

// isExactInt64 reports whether f is a finite, integral value that fits
// int64. float64(math.MaxInt64) rounds UP to 2^63, so the upper bound is
// an exclusive >=; the lower bound -2^63 is exactly representable.
func isExactInt64(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0) && f == math.Trunc(f) &&
		f >= math.MinInt64 && f < math.MaxInt64
}
