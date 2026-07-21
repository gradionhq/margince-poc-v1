// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// This file is the CRM v3 Search endpoint (design §11): the
// watermark-modified sweep (SearchModified) plus the Search request-body
// wire types it uses. The >10k-per-timestamp numeric-id keyset fallback
// design §4.4/§7 calls for is an open, spike-validate item, not built
// here (the Incumbent seam's Modified signature has no way to signal a
// mode switch through to the adapter yet).

package hubspot

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// SearchModified sweeps objectClass records whose watermarkProperty is at
// or after since, sorted ascending by that single property (design
// §4.4/§7/§11: HubSpot Search honors only one sort). after pages within
// the offset-capped (10k) window this one watermark query is good for;
// paging past that ceiling into a tied-timestamp numeric-id keyset is the
// open spike this file's header describes.
func (c *Client) SearchModified(
	ctx context.Context,
	objectClass, watermarkProperty string,
	since time.Time,
	after string,
	limit int,
	properties []string,
) (SearchPage, error) {
	body := searchBody{
		Limit:      limit,
		After:      after,
		Properties: properties,
		Sorts:      []searchSort{{PropertyName: watermarkProperty, Direction: "ASCENDING"}},
		FilterGroups: []searchFilterGroup{{
			Filters: []searchFilter{{
				PropertyName: watermarkProperty,
				Operator:     "GTE",
				Value:        since.UTC().Format(time.RFC3339Nano),
			}},
		}},
	}
	return c.search(ctx, objectClass, body)
}

// searchSort is one entry of a Search request's sorts array. HubSpot
// accepts at most one (design §11).
type searchSort struct {
	PropertyName string `json:"propertyName"` //nolint:tagliatelle // HubSpot's wire format (camelCase); must match to decode
	Direction    string `json:"direction"`
}

// searchFilter is one Search filterGroups[].filters entry.
type searchFilter struct {
	PropertyName string `json:"propertyName"` //nolint:tagliatelle // HubSpot's wire format (camelCase); must match to decode
	Operator     string `json:"operator"`
	Value        string `json:"value"`
}

// searchFilterGroup is one Search filterGroups entry (filters within a
// group are AND-ed; groups are OR-ed — this client only ever needs one
// group per query).
type searchFilterGroup struct {
	Filters []searchFilter `json:"filters"`
}

// searchBody is the CRM v3 Search request body (design §11).
type searchBody struct {
	Limit        int                 `json:"limit,omitempty"`
	After        string              `json:"after,omitempty"`
	Properties   []string            `json:"properties,omitempty"`
	Sorts        []searchSort        `json:"sorts,omitempty"`
	FilterGroups []searchFilterGroup `json:"filterGroups,omitempty"` //nolint:tagliatelle // HubSpot's wire format (camelCase); must match to decode
}

func (c *Client) search(ctx context.Context, objectClass string, body searchBody) (SearchPage, error) {
	var out struct {
		Total   int          `json:"total"`
		Results []wireObject `json:"results"`
		Paging  struct {
			Next struct {
				After string `json:"after"`
			} `json:"next"`
		} `json:"paging"`
	}
	path := "/crm/v3/objects/" + url.PathEscape(objectClass) + "/search"
	if err := c.do(ctx, http.MethodPost, path, body, &out); err != nil {
		return SearchPage{}, err
	}
	return SearchPage{
		Total:     out.Total,
		Results:   toObjectRecords(out.Results),
		NextAfter: out.Paging.Next.After,
	}, nil
}
