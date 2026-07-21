// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// This file is the object-read endpoints outside Search (design §11):
// the id-keyset List backfill cursor, the by-id BatchRead, v4
// Associations, and Owner resolution (single + directory) — plus the
// wire-decode types each one shares (wireObject in particular, which
// List/Search/BatchRead all decode into).

package hubspot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// List pages objectClass's records by the stable id-keyset cursor
// (design §11: GET .../objects/{type}?properties=&after=), the backfill
// cursor — uncapped, unlike Search's offset-limited after.
func (c *Client) List(ctx context.Context, objectClass string, properties []string, after string, limit int) (Page, error) {
	q := url.Values{}
	if len(properties) > 0 {
		q.Set("properties", strings.Join(properties, ","))
	}
	if after != "" {
		q.Set("after", after)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out struct {
		Results []wireObject `json:"results"`
		Paging  struct {
			Next struct {
				After string `json:"after"`
			} `json:"next"`
		} `json:"paging"`
	}
	path := "/crm/v3/objects/" + url.PathEscape(objectClass)
	if err := c.do(ctx, http.MethodGet, path+"?"+q.Encode(), nil, &out); err != nil {
		return Page{}, err
	}
	return Page{Results: toObjectRecords(out.Results), NextAfter: out.Paging.Next.After}, nil
}

// batchReadBody is the CRM v3 batch/read request body.
type batchReadBody struct {
	Properties []string           `json:"properties,omitempty"`
	Inputs     []batchReadInputID `json:"inputs"`
}

type batchReadInputID struct {
	ID string `json:"id"`
}

// BatchRead fetches ids in one call (design §11's batch object-read
// shape, 100/call) — the record-clock fetch Adapter.Get funnels into.
func (c *Client) BatchRead(ctx context.Context, objectClass string, ids []string, properties []string) ([]ObjectRecord, error) {
	inputs := make([]batchReadInputID, 0, len(ids))
	for _, id := range ids {
		inputs = append(inputs, batchReadInputID{ID: id})
	}
	body := batchReadBody{Properties: properties, Inputs: inputs}
	var out struct {
		Results []wireObject `json:"results"`
	}
	path := "/crm/v3/objects/" + url.PathEscape(objectClass) + "/batch/read"
	if err := c.do(ctx, http.MethodPost, path, body, &out); err != nil {
		return nil, err
	}
	return toObjectRecords(out.Results), nil
}

// wireAssociation is one v4 associations result entry (design §11).
// ToObjectID arrives as a JSON number, not a string — decoded via
// json.Number so a 64-bit HubSpot id never loses precision.
type wireAssociation struct {
	ToObjectID json.Number `json:"toObjectId"` //nolint:tagliatelle // HubSpot's wire format (camelCase); must match to decode
	Types      []struct {
		Category string `json:"category"`
		TypeID   int    `json:"typeId"` //nolint:tagliatelle // HubSpot's wire format (camelCase); must match to decode
		Label    string `json:"label"`
	} `json:"associationTypes"` //nolint:tagliatelle // HubSpot's wire format (camelCase); must match to decode
}

// Associations lists the v4 association edges from fromID (of fromClass)
// to every linked record of toClass (design §11).
func (c *Client) Associations(ctx context.Context, fromClass, fromID, toClass string) ([]Association, error) {
	var out struct {
		Results []wireAssociation `json:"results"`
	}
	path := "/crm/v4/objects/" + url.PathEscape(fromClass) + "/" + url.PathEscape(fromID) +
		"/associations/" + url.PathEscape(toClass)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	assocs := make([]Association, 0, len(out.Results))
	for _, r := range out.Results {
		types := make([]AssociationType, 0, len(r.Types))
		for _, t := range r.Types {
			types = append(types, AssociationType{Category: t.Category, TypeID: t.TypeID, Label: t.Label})
		}
		assocs = append(assocs, Association{ToObjectID: r.ToObjectID.String(), Types: types})
	}
	return assocs, nil
}

// Owner resolves ownerID via the CRM Owners API (design §4.3:
// crm.objects.owners.read — required for mirror_user_map's
// hubspot_owner_id→email resolution).
func (c *Client) Owner(ctx context.Context, ownerID string) (Owner, error) {
	var out struct {
		ID        string `json:"id"`
		Email     string `json:"email"`
		FirstName string `json:"firstName"` //nolint:tagliatelle // HubSpot's wire format (camelCase); must match to decode
		LastName  string `json:"lastName"`  //nolint:tagliatelle // HubSpot's wire format (camelCase); must match to decode
	}
	path := "/crm/v3/owners/" + url.PathEscape(ownerID)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return Owner{}, err
	}
	return Owner{ID: out.ID, Email: out.Email, FirstName: out.FirstName, LastName: out.LastName}, nil
}

// Owners pages the full CRM Owners directory to completion (design §4.3:
// crm.objects.owners.read) — the id→email population mirror_user_map
// seeding matches workspace users against. It follows paging.next.after
// until exhausted, the same id-keyset shape List backfills by.
func (c *Client) Owners(ctx context.Context) ([]Owner, error) {
	var owners []Owner
	after := ""
	// Termination guard: a well-behaved API advances paging.next.after and
	// eventually returns none, but a buggy/adversarial one that echoes the
	// same cursor would spin forever. The owners directory is small (100 per
	// page), so ownersMaxPages bounds it far above any real portal.
	for page := 0; page < ownersMaxPages; page++ {
		q := url.Values{}
		q.Set("limit", "100")
		if after != "" {
			q.Set("after", after)
		}
		var out struct {
			Results []struct {
				ID        string `json:"id"`
				Email     string `json:"email"`
				FirstName string `json:"firstName"` //nolint:tagliatelle // HubSpot's wire format (camelCase); must match to decode
				LastName  string `json:"lastName"`  //nolint:tagliatelle // HubSpot's wire format (camelCase); must match to decode
			} `json:"results"`
			Paging struct {
				Next struct {
					After string `json:"after"`
				} `json:"next"`
			} `json:"paging"`
		}
		if err := c.do(ctx, http.MethodGet, "/crm/v3/owners?"+q.Encode(), nil, &out); err != nil {
			return nil, err
		}
		for _, o := range out.Results {
			owners = append(owners, Owner{ID: o.ID, Email: o.Email, FirstName: o.FirstName, LastName: o.LastName})
		}
		if out.Paging.Next.After == "" {
			return owners, nil
		}
		after = out.Paging.Next.After
	}
	return nil, fmt.Errorf("hubspot: owners directory did not terminate within %d pages — the API is echoing a non-advancing cursor", ownersMaxPages)
}

// ownersMaxPages bounds Owners' pagination: 10k pages × 100/page = a
// million owners, orders of magnitude above any real portal, so it only
// ever trips on a cursor that never advances.
const ownersMaxPages = 10_000

// wireObject is one HubSpot object as returned by List/Search/BatchRead
// (design §11's object-read shape).
type wireObject struct {
	ID         string            `json:"id"`
	Properties map[string]string `json:"properties"`
	CreatedAt  string            `json:"createdAt"` //nolint:tagliatelle // HubSpot's wire format (camelCase); must match to decode
	UpdatedAt  string            `json:"updatedAt"` //nolint:tagliatelle // HubSpot's wire format (camelCase); must match to decode
	Archived   bool              `json:"archived"`
}

func toObjectRecords(in []wireObject) []ObjectRecord {
	out := make([]ObjectRecord, 0, len(in))
	for _, o := range in {
		out = append(out, ObjectRecord(o))
	}
	return out
}
