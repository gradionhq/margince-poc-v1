// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Field extraction for the mirror-record → typed-contract assembly
// (overlaywire.go does the struct-shaping on top of these). The canonical
// jsonb payload is decoded data, so every reader here — scalar field
// readers, the person/organization child-collection lookups, and the
// timestamp/integer parsers alike — answers absent rather than erroring on
// a shape it did not expect: the true value always survives in `raw`, and a
// body that drops one slot beats a read that fails outright.

import (
	"math"
	"strconv"
	"strings"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// overlayAddress lifts the mapper's address_json assembly onto the
// contract's structured Address — the one reader of that payload, shared by
// the overlay read wire and the flip import, since both see the same
// canonical jsonb. The mapper spells the members in the contract's vocabulary,
// so this mostly shapes them; the incumbent spellings are read alongside for
// the same reason overlayChildRows still reads a bare object, and that reason
// is permanent rather than transitional (see addressIncumbentMembers). An
// address with no populated member answers nil rather than an empty object, so
// a record the incumbent holds no address for reads as absent instead of as a
// blank address.
func overlayAddress(fields map[string]any) *crmcontracts.Address {
	nested, ok := fields["address"].(map[string]any)
	if !ok {
		return nil
	}
	addr := crmcontracts.Address{
		Line1:      addressMember(nested, "line1"),
		Line2:      addressMember(nested, "line2"),
		City:       addressMember(nested, "city"),
		Region:     addressMember(nested, "region"),
		PostalCode: addressMember(nested, "postal_code"),
		Country:    addressMember(nested, "country"),
	}
	if addr.Line1 == nil && addr.Line2 == nil && addr.City == nil &&
		addr.Region == nil && addr.PostalCode == nil && addr.Country == nil {
		return nil
	}
	return &addr
}

// addressIncumbentMembers maps a contract Address member to the incumbent
// property name a mirror payload assembled before the mapper's rename carries
// it under. Such a payload is not a passing state: the poller rewrites a
// record's fields only when the incumbent's own last-modified baseline
// advances, and a converged backfill does not revisit it either, so a record
// nobody edits upstream again keeps its original member names for good. Only
// the three that differ appear — city and country spell alike in both
// vocabularies, and line2 has no incumbent counterpart to fall back to.
var addressIncumbentMembers = map[string]string{
	"line1":       "address",
	"region":      "state",
	"postal_code": "zip",
}

// addressMember reads one Address member under the contract's own name,
// falling back to the incumbent spelling where the two differ.
func addressMember(nested map[string]any, member string) *string {
	if value := fieldStringPtr(nested, member); value != nil {
		return value
	}
	if incumbent, differs := addressIncumbentMembers[member]; differs {
		return fieldStringPtr(nested, incumbent)
	}
	return nil
}

// overlayChildRows reads a child collection out of the canonical payload. It
// answers both real shapes of one: the mapper builds []map[string]any
// in-process, while a payload that has been through the mirror's jsonb column
// arrives from json.Unmarshal as []any of map[string]any. A single object is
// the shape written before a child target held a collection; the mirror is a
// cache that heals as the poller touches a record, but a record never modified
// upstream keeps its original shape indefinitely, so reading it is permanent
// rather than transitional.
func overlayChildRows(fields map[string]any, parent string) []map[string]any {
	switch held := fields[parent].(type) {
	case []map[string]any:
		return held
	case []any:
		rows := make([]map[string]any, 0, len(held))
		for _, entry := range held {
			if row, ok := entry.(map[string]any); ok {
				rows = append(rows, row)
			}
		}
		return rows
	case map[string]any:
		return []map[string]any{held}
	default:
		return nil
	}
}

// overlayPersonEmail answers the mirrored contact's email alone — the first row
// of the person_email collection holding one, so a collection whose leading row
// carries only its declared attributes still yields the address. Its readers
// are the display-name fallbacks, which need an address to name a contact by
// and none of the row's attributes; a reader that publishes or imports the
// addresses takes the whole collection instead (overlayPersonEmails,
// flipPersonEmails).
func overlayPersonEmail(fields map[string]any) string {
	for _, row := range overlayChildRows(fields, "person_email") {
		if address := strings.TrimSpace(fieldString(row, "email")); address != "" {
			return address
		}
	}
	return ""
}

// overlayPersonEmails assembles the contract's email collection from the
// mirrored child rows. A row whose address is missing or blank is skipped
// rather than published as an empty address — the true payload survives in
// `raw` either way. The type rides only when it lands on the contract's own
// enum (the column is CHECK-constrained on the native side too); anything
// else reads as the work address one mapped address means.
func overlayPersonEmails(parent openapi_types.UUID, fields map[string]any) *[]crmcontracts.PersonEmail {
	var out []crmcontracts.PersonEmail
	for _, row := range overlayChildRows(fields, "person_email") {
		address := strings.TrimSpace(fieldString(row, "email"))
		if address == "" {
			continue
		}
		emailType := crmcontracts.PersonEmailEmailType(strings.TrimSpace(fieldString(row, "email_type")))
		if !emailType.Valid() {
			emailType = crmcontracts.PersonEmailEmailTypeWork
		}
		position := childRowPosition(row)
		out = append(out, crmcontracts.PersonEmail{
			Id:         overlaySyntheticID(parent, position, address),
			Email:      openapi_types.Email(address),
			EmailType:  emailType,
			IsPrimary:  childRowIsPrimary(row),
			Position:   position,
			Source:     overlaySource,
			CapturedBy: ptrString(overlayCapturedByValue),
		})
	}
	if out == nil {
		return nil
	}
	return &out
}

// overlayPersonPhones is overlayPersonEmails' counterpart for numbers: a
// contact's work and mobile numbers are separate typed rows of one collection.
func overlayPersonPhones(parent openapi_types.UUID, fields map[string]any) *[]crmcontracts.PersonPhone {
	var out []crmcontracts.PersonPhone
	for _, row := range overlayChildRows(fields, "person_phone") {
		number := strings.TrimSpace(fieldString(row, "phone"))
		if number == "" {
			continue
		}
		phoneType := crmcontracts.PersonPhonePhoneType(strings.TrimSpace(fieldString(row, "phone_type")))
		if !phoneType.Valid() {
			phoneType = crmcontracts.PersonPhonePhoneTypeWork
		}
		position := childRowPosition(row)
		out = append(out, crmcontracts.PersonPhone{
			Id:         overlaySyntheticID(parent, position, number),
			Phone:      number,
			PhoneType:  phoneType,
			IsPrimary:  childRowIsPrimary(row),
			Position:   position,
			Source:     overlaySource,
			CapturedBy: ptrString(overlayCapturedByValue),
		})
	}
	if out == nil {
		return nil
	}
	return &out
}

// overlayOrganizationDomains assembles the contract's domain collection from
// the mirrored child rows, as overlayPersonEmails does for a contact's
// addresses. A row whose domain is missing or blank is skipped rather than
// published as an empty host — the true payload survives in `raw` either way.
// The whole collection is published, not its leading row: a mapping that
// declares a second domain row is a mapping change, not a wire change.
func overlayOrganizationDomains(parent openapi_types.UUID, fields map[string]any) *[]crmcontracts.OrganizationDomain {
	var out []crmcontracts.OrganizationDomain
	for _, row := range overlayChildRows(fields, "organization_domain") {
		domain := strings.TrimSpace(fieldString(row, "domain"))
		if domain == "" {
			continue
		}
		out = append(out, crmcontracts.OrganizationDomain{
			Id:         overlaySyntheticID(parent, childRowPosition(row), domain),
			Domain:     domain,
			IsPrimary:  childRowIsPrimary(row),
			Source:     overlaySource,
			CapturedBy: ptrString(overlayCapturedByValue),
		})
	}
	if out == nil {
		return nil
	}
	return &out
}

// overlayWebsiteURL renders a mirrored company's website from its primary
// domain row. website_url is DERIVED and never stored (ADR-0085): the native
// read path renders "https://" + the primary domain, and a mirrored company
// owes the caller the same answer from the same fact. It reads the collection
// this wire already publishes rather than the payload a second time, so the two
// can never name different domains. A collection where no row claims the flag
// yields nothing — which domain is the company's is the mapping's assertion,
// never this reader's to pick by position, and the native path leaves the slot
// absent on exactly the same rows.
func overlayWebsiteURL(domains *[]crmcontracts.OrganizationDomain) *string {
	if domains == nil {
		return nil
	}
	for _, row := range *domains {
		if !row.IsPrimary {
			continue
		}
		website := "https://" + row.Domain
		return &website
	}
	return nil
}

// The attribute vocabulary a mirrored child row declares, in one place next to
// the readers below: everything a row carries beyond its own mapped column.
// The mapping module writes these keys (overlay's ChildRow.Attrs and its
// declared position), so the spellings are a cross-package seam — a reader
// asking what a child row publishes gets the whole answer here.
const (
	childAttrIsPrimary = "is_primary"
	childAttrPosition  = "position"
)

// childRowIsPrimary reports whether a child row is its collection's primary.
// A row that declares nothing is not the primary — the flag is the mapping's
// to assert, never the reader's to assume. This is the ONE rule for the flag,
// read wire and flip import alike: the native column defaults to false and
// carries a partial unique index over the primary of a collection, so a reader
// that assumed true would both invent an assertion no mapping made and abort a
// second row that made the same assumption honestly.
func childRowIsPrimary(row map[string]any) bool {
	primary, _ := row[childAttrIsPrimary].(bool)
	return primary
}

// childRowPosition answers a child row's declared place in its collection. It
// decodes as float64 through the mirror's jsonb column and stays an int
// in-process, so both are read; any other shape answers 0, the collection's
// own first slot.
func childRowPosition(row map[string]any) int {
	switch value := row[childAttrPosition].(type) {
	case float64:
		if !isExactInt64(value) {
			return 0
		}
		return int(value)
	case int:
		return value
	default:
		return 0
	}
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

// overlayTimeOr answers a canonical timestamp field, falling back to the given
// instant where the mirror holds no stamp the parser recognizes. Every contract
// timestamp slot the wire fills is required, so a record the incumbent stamped
// none for still needs an answer, and the mirror's own sync instant is the only
// time it can honestly claim for itself.
func overlayTimeOr(fields map[string]any, key string, fallback time.Time) time.Time {
	if stamped, ok := overlayTime(fields, key); ok {
		return stamped
	}
	return fallback
}

// isExactInt64 reports whether f is a finite, integral value that fits
// int64. float64(math.MaxInt64) rounds UP to 2^63, so the upper bound is
// an exclusive >=; the lower bound -2^63 is exactly representable.
func isExactInt64(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0) && f == math.Trunc(f) &&
		f >= math.MinInt64 && f < math.MaxInt64
}
