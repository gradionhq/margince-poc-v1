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

// ArchivedObject is one archived (deleted) record from the archived-object
// list feed: only the id and the archivedAt timestamp the deletion sweep
// keys the mirror purge by — the deletion feed does not project fields.
type ArchivedObject struct {
	ID         string
	ArchivedAt string
}

// ArchivedPage is one page of ListArchived results; NextAfter is "" when
// there is no further page.
type ArchivedPage struct {
	Results   []ArchivedObject
	NextAfter string
}

// ListArchived pages objectClass's ARCHIVED (deleted) records via the list
// endpoint's archived=true flag (GET .../objects/{type}?archived=true&after=)
// — the deletion feed continuous sync sweeps to purge mirror rows the
// incumbent has removed. HubSpot's list endpoint is not archivedAt-ordered,
// so the deletion sweep (overlay/reconcile.go) pages the full archived set
// each pass and filters by its watermark; that stays correct because the
// per-record purge is idempotent, at the cost of re-listing archived
// records already handled — a cheaper incremental deletion feed is a later
// optimization, not a correctness gap.
func (c *Client) ListArchived(ctx context.Context, objectClass, after string, limit int) (ArchivedPage, error) {
	q := url.Values{}
	q.Set("archived", "true")
	if after != "" {
		q.Set("after", after)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out struct {
		Results []struct {
			ID         string `json:"id"`
			ArchivedAt string `json:"archivedAt"` //nolint:tagliatelle // HubSpot's wire format (camelCase); must match to decode
		} `json:"results"`
		Paging struct {
			Next struct {
				After string `json:"after"`
			} `json:"next"`
		} `json:"paging"`
	}
	path := "/crm/v3/objects/" + url.PathEscape(objectClass)
	if err := c.do(ctx, http.MethodGet, path+"?"+q.Encode(), nil, &out); err != nil {
		return ArchivedPage{}, err
	}
	res := make([]ArchivedObject, 0, len(out.Results))
	for _, r := range out.Results {
		res = append(res, ArchivedObject{ID: r.ID, ArchivedAt: r.ArchivedAt})
	}
	return ArchivedPage{Results: res, NextAfter: out.Paging.Next.After}, nil
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
	var assocs []Association
	base := "/crm/v4/objects/" + url.PathEscape(fromClass) + "/" + url.PathEscape(fromID) +
		"/associations/" + url.PathEscape(toClass)
	after := ""
	// The v4 associations list is paged (paging.next.after): a record with
	// more than one page of edges (HubSpot returns up to 500 per page) would
	// silently lose every edge past the first page if we read only page one.
	// Termination guard, same shape as Owners: a buggy/adversarial API that
	// echoes a non-advancing cursor would otherwise spin forever.
	for page := 0; page < associationsMaxPages; page++ {
		q := url.Values{}
		q.Set("limit", "500")
		if after != "" {
			q.Set("after", after)
		}
		var out struct {
			Results []wireAssociation `json:"results"`
			Paging  struct {
				Next struct {
					After string `json:"after"`
				} `json:"next"`
			} `json:"paging"`
		}
		if err := c.do(ctx, http.MethodGet, base+"?"+q.Encode(), nil, &out); err != nil {
			return nil, err
		}
		for _, r := range out.Results {
			types := make([]AssociationType, 0, len(r.Types))
			for _, t := range r.Types {
				types = append(types, AssociationType{Category: t.Category, TypeID: t.TypeID, Label: t.Label})
			}
			assocs = append(assocs, Association{ToObjectID: r.ToObjectID.String(), Types: types})
		}
		if out.Paging.Next.After == "" {
			return assocs, nil
		}
		// A cursor that does not advance would otherwise spin to the page cap,
		// firing thousands of duplicate requests and accumulating duplicate
		// edges — catch it on the first repeat instead.
		if out.Paging.Next.After == after {
			return nil, fmt.Errorf("hubspot: associations %s/%s->%s returned a non-advancing paging cursor %q", fromClass, fromID, toClass, after)
		}
		after = out.Paging.Next.After
	}
	return nil, fmt.Errorf("hubspot: associations %s/%s->%s did not terminate within %d pages", fromClass, fromID, toClass, associationsMaxPages)
}

// associationsMaxPages bounds Associations' pagination: 10k pages × 500/page
// = five million edges from a single record, far above any real portal, so
// it only trips on a cursor that never advances.
const associationsMaxPages = 10_000

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
		// Catch a non-advancing cursor on the first repeat rather than
		// spinning to the page cap (the same guard Associations uses).
		if out.Paging.Next.After == after {
			return nil, fmt.Errorf("hubspot: owners directory returned a non-advancing paging cursor %q", after)
		}
		after = out.Paging.Next.After
	}
	return nil, fmt.Errorf("hubspot: owners directory did not terminate within %d pages", ownersMaxPages)
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

// AccountID returns the HubSpot portal (hub) id for the connected private-app
// token — the incumbent account identity recorded at connect (OVA-DDL-3) so an
// inbound webhook's portalId binds to this workspace. portalId arrives as a
// JSON number; it is returned as its decimal string, matching the webhook
// payload's own portalId rendering.
func (c *Client) AccountID(ctx context.Context) (string, error) {
	var out struct {
		PortalID json.Number `json:"portalId"` //nolint:tagliatelle // HubSpot's wire format (camelCase); must match to decode
	}
	if err := c.do(ctx, http.MethodGet, "/account-info/v3/details", nil, &out); err != nil {
		return "", err
	}
	if out.PortalID.String() == "" {
		return "", fmt.Errorf("hubspot: account-info returned no portalId")
	}
	return out.PortalID.String(), nil
}
