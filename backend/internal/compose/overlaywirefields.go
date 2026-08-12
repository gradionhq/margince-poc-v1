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
// the organization_domain collection, mirroring overlayPersonEmailRow.
func overlayOrgDomainRow(fields map[string]any) (map[string]any, string) {
	return overlayFirstChildRow(fields, "organization_domain", "domain")
}

// overlayOrgDomain answers the mirrored company's domain alone.
func overlayOrgDomain(fields map[string]any) string {
	_, domain := overlayOrgDomainRow(fields)
	return domain
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

// isExactInt64 reports whether f is a finite, integral value that fits
// int64. float64(math.MaxInt64) rounds UP to 2^63, so the upper bound is
// an exclusive >=; the lower bound -2^63 is exactly representable.
func isExactInt64(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0) && f == math.Trunc(f) &&
		f >= math.MinInt64 && f < math.MaxInt64
}
