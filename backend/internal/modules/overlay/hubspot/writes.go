// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package hubspot

import (
	"context"
	"net/http"
	"net/url"
)

// This file is the client's write transport: the v3 object create/update/
// archive endpoints the write-back engine reaches HubSpot through (design.md
// §4.5). It mirrors records.go's read plumbing — same do/mapStatus error
// path, same wireObject decode — and returns HubSpot-shaped ObjectRecords
// only; projecting canonical fields onto HubSpot properties (mapWrite) and
// mapping the response back is the adapter's job, not this package's.

// writeBody is the v3 object create/update request envelope: HubSpot takes
// the property set under a "properties" object (design §11).
type writeBody struct {
	Properties map[string]string `json:"properties"`
}

// CreateObject POSTs a new object of objectClass with props and returns the
// created object (id + stored properties + updatedAt) — the state the mirror
// ingests as authoritative-on-the-incumbent.
func (c *Client) CreateObject(ctx context.Context, objectClass string, props map[string]string) (ObjectRecord, error) {
	path := "/crm/v3/objects/" + url.PathEscape(objectClass)
	var out wireObject
	if err := c.do(ctx, http.MethodPost, path, writeBody{Properties: props}, &out); err != nil {
		return ObjectRecord{}, err
	}
	return ObjectRecord(out), nil
}

// UpdateObject PATCHes props onto the object objectClass/id and returns its
// updated state. HubSpot v3 has no per-request If-Match primitive, so the
// write-back drift check is applied by the adapter (a stored-baseline
// comparison) BEFORE this call — this is the raw transport only.
func (c *Client) UpdateObject(ctx context.Context, objectClass, id string, props map[string]string) (ObjectRecord, error) {
	path := "/crm/v3/objects/" + url.PathEscape(objectClass) + "/" + url.PathEscape(id)
	var out wireObject
	if err := c.do(ctx, http.MethodPatch, path, writeBody{Properties: props}, &out); err != nil {
		return ObjectRecord{}, err
	}
	return ObjectRecord(out), nil
}

// ArchiveObject archives (soft-deletes) the object objectClass/id via
// HubSpot's own DELETE — the incumbent's archive, mirrored by the caller as a
// removal. A 2xx with no body is success; the do path decodes nothing.
func (c *Client) ArchiveObject(ctx context.Context, objectClass, id string) error {
	path := "/crm/v3/objects/" + url.PathEscape(objectClass) + "/" + url.PathEscape(id)
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}
