// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Field extraction for the mirror-record → typed-contract assembly
// (overlaywire.go does the struct-shaping on top of these). The canonical
// jsonb payload is decoded data, so every reader here — scalar field
// readers, the nested person/organization child lookups, and the
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
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// overlayAddress lifts the mapper's address_json assembly onto the
// contract's structured Address. An address with no populated member answers
// nil rather than an empty object, so a contact the incumbent holds no
// address for reads as absent instead of as a blank address.
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

// addressMember answers one trimmed non-empty address member, nil otherwise.
func addressMember(nested map[string]any, key string) *string {
	return fieldStringPtr(nested, key)
}

// overlayOwnerID answers the app_user the mapper resolved this record's
// incumbent owner to through mirror_user_map. An owner the mapping could not
// resolve stays absent: the raw incumbent owner id is not an app_user id, and
// publishing it in a uuid slot would name a user that does not exist.
func overlayOwnerID(fields map[string]any) *openapi_types.UUID {
	raw := fieldString(fields, "owner_id")
	if raw == "" {
		return nil
	}
	parsed, err := ids.Parse(raw)
	if err != nil {
		return nil
	}
	owner := openapi_types.UUID(parsed)
	return &owner
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

// overlayOrgDomain digs the mapped domain out of the organization_domain
// child payload (the mapper's "organization_domain.domain" child target lands
// as a nested object in the canonical fields), mirroring overlayPersonEmail.
func overlayOrgDomain(fields map[string]any) string {
	child, ok := fields["organization_domain"].(map[string]any)
	if !ok {
		return ""
	}
	domain, ok := child["domain"].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(domain)
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
