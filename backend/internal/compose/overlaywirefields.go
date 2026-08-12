// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Field extraction for the mirror-record → typed-contract assembly. The
// canonical jsonb payload is decoded data, so every reader here answers
// absent rather than erroring on a shape it did not expect: the true value
// always survives in `raw`, and a body that drops one slot beats a read that
// fails outright.

import (
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
