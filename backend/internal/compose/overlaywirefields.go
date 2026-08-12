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
// canonical jsonb. The mapper already spells the members in the contract's
// vocabulary, so this only shapes them. An address with no populated member
// answers nil rather than an empty object, so a record the incumbent holds
// no address for reads as absent instead of as a blank address.
func overlayAddress(fields map[string]any) *crmcontracts.Address {
	nested, ok := fields["address"].(map[string]any)
	if !ok {
		return nil
	}
	addr := crmcontracts.Address{
		Line1:      fieldStringPtr(nested, "line1"),
		Line2:      fieldStringPtr(nested, "line2"),
		City:       fieldStringPtr(nested, "city"),
		Region:     fieldStringPtr(nested, "region"),
		PostalCode: fieldStringPtr(nested, "postal_code"),
		Country:    fieldStringPtr(nested, "country"),
	}
	if addr.Line1 == nil && addr.Line2 == nil && addr.City == nil &&
		addr.Region == nil && addr.PostalCode == nil && addr.Country == nil {
		return nil
	}
	return &addr
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

// overlayPersonEmailRow answers the mirrored contact's email together with the
// row that carries it — the first row of the person_email collection holding
// one, so a collection whose leading row holds only its declared attributes
// still yields the address. The row is what a caller reads the address's type
// and primary flag from; it is nil exactly when the address is "".
func overlayPersonEmailRow(fields map[string]any) (map[string]any, string) {
	return overlayFirstChildRow(fields, "person_email", "email")
}

// overlayPersonEmail answers the mirrored contact's email alone, for a caller
// that needs the address and none of the row's declared attributes.
func overlayPersonEmail(fields map[string]any) string {
	_, email := overlayPersonEmailRow(fields)
	return email
}

// overlayOrgDomainRow answers the mirrored company's domain and its row out of
// the organization_domain collection, mirroring overlayPersonEmailRow. Both
// readers of a company domain need the row — the flip import for its primary
// flag, the read wire for the position its row id is derived from — so there is
// no domain-only counterpart to overlayPersonEmail.
func overlayOrgDomainRow(fields map[string]any) (map[string]any, string) {
	return overlayFirstChildRow(fields, "organization_domain", "domain")
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
// to assert, never the reader's to assume.
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

// overlayFirstChildRow answers the first row of a child collection holding a
// non-empty string under column, with that trimmed value; nil and "" when no
// row holds one.
func overlayFirstChildRow(fields map[string]any, parent, column string) (map[string]any, string) {
	for _, row := range overlayChildRows(fields, parent) {
		if value, ok := row[column].(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return row, trimmed
			}
		}
	}
	return nil, ""
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
