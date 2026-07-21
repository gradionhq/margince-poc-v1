// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package hubspot is the HubSpot provider I/O: a hand-rolled net/http REST
// client over the CRM v3/v4 endpoints the mirror engine needs (design.md
// §11), mirroring the gmail package's construction/timeout/error
// conventions so overlay takes on no SDK dependency. It returns
// HubSpot-shaped structs only — mapping raw properties into an
// overlay.Record is the adapter's job, not this package's.
//
// This file is the client's construction and HTTP transport: NewClient,
// its Options, and the do/mapStatus request-response plumbing every
// endpoint in search.go/records.go shares. The result types live in
// types.go.
package hubspot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// httpTimeout bounds every HubSpot call so a stalled request can't pin an
// api callback or the mirror sync poller (http.DefaultClient has no
// timeout).
const httpTimeout = 30 * time.Second

// propHSObjectID is HubSpot's numeric record-id property (design §11):
// the sole sort/filter key for the tied-timestamp keyset sweep in
// search.go, and contactsMapping's ExternalKey in mapping_hs.go — one
// spelling shared across both uses.
const propHSObjectID = "hs_object_id"

// hubspotAPIBase is HubSpot's one CRM API host. EU data residency
// (design §4.3) is a per-portal storage property, not a separate API
// host — HubSpot serves every region's CRM v3/v4 endpoints from this
// same host today. Region is still recorded and validated (regionHosts
// below) so a future region that DOES require a distinct host has a
// single place to map it, rather than every call site guessing.
const hubspotAPIBase = "https://api.hubapi.com"

// regionHosts maps a recognized region to its API host. Every
// currently-supported region resolves to the single global host; the
// map (rather than a bare constant) is the seam a future region-specific
// host lands in without touching call sites.
var regionHosts = map[string]string{
	"us":  hubspotAPIBase,
	"eu1": hubspotAPIBase,
}

// ErrUnknownRegion marks a NewClient region outside the recognized set
// (regionHosts) — a configuration mistake, not a runtime HubSpot error.
var ErrUnknownRegion = errors.New("hubspot: unrecognized region")

// ErrUnreachable marks a transport-level failure reaching HubSpot (DNS,
// TCP, TLS, timeout) or a non-2xx status this client does not map to a
// more specific sentinel (e.g. a validation 400). The transport layer
// turns it into a clean 502 without echoing HubSpot's raw body.
var ErrUnreachable = errors.New("hubspot: could not reach HubSpot")

// errorEnvelope is HubSpot's error response shape (design §11): Message
// and Errors are never surfaced to a caller — only Category drives the
// sentinel mapping below.
type errorEnvelope struct {
	Status   string `json:"status"`
	Message  string `json:"message"`
	Category string `json:"category"`
}

// Client is a hand-rolled HubSpot CRM REST client (v3 objects/search/
// pipelines/owners, v4 associations). It carries no per-workspace state
// beyond the connection's base URL and bearer token — one Client per
// incumbent connection.
type Client struct {
	httpClient *http.Client
	base       string
	region     string
	token      string
}

// Option configures a Client at construction. Tests use WithBaseURL to
// point at an httptest.Server and WithHTTPClient to inject a client with
// a custom transport; production callers pass neither.
type Option func(*Client)

// WithBaseURL overrides the API host NewClient derived from region. Used
// by tests to point the client at an httptest.Server.
func WithBaseURL(base string) Option {
	return func(c *Client) { c.base = base }
}

// WithHTTPClient overrides the bounded http.Client NewClient built.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.httpClient = hc }
}

// NewClient builds a HubSpot REST client for the given region's
// connection, sealed with the private-app bearer token. The region maps
// to HubSpot's API host (regionHosts) — every recognized region resolves
// to the same global host today (design §4.3: EU residency is a
// per-portal storage property, not a distinct API host) — and an
// unrecognized region still gets a working client against the global
// host, since no NewClient caller can fail construction to check
// ErrUnknownRegion.
func NewClient(region, token string, opts ...Option) *Client {
	c := &Client{
		httpClient: &http.Client{Timeout: httpTimeout},
		base:       hubspotAPIBase,
		region:     region,
		token:      token,
	}
	if host, ok := regionHosts[region]; ok {
		c.base = host
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Region returns the region this client was constructed with.
func (c *Client) Region() string { return c.region }

// BaseURL returns the API host this client sends requests to.
func (c *Client) BaseURL() string { return c.base }

// do performs an authorized JSON request and decodes the response into
// out (nil for callers that don't need the body decoded). A non-2xx
// status maps to a sentinel per design §11's error envelope — HubSpot's
// raw message/body is never surfaced to the caller.
//
//craft:ignore naked-any body/out are the caller-supplied JSON payload and decode target — their concrete types vary per endpoint
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("hubspot: encoding %s request: %w", path, err)
		}
		reqBody = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reqBody)
	if err != nil {
		return fmt.Errorf("hubspot: building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("hubspot: %s: %w", path, ErrUnreachable)
	}
	//craft:ignore swallowed-errors best-effort close of the response body — the decoded result/status is what matters
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("hubspot: reading %s response: %w", path, ErrUnreachable)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return mapStatus(resp.StatusCode, respBody)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("hubspot: decoding %s response: %w", path, ErrUnreachable)
	}
	return nil
}

// mapStatus turns a non-2xx HubSpot response into a sentinel. category
// (design §11's error envelope) refines 4xx classification; HubSpot's
// message/errors detail never reaches the returned error.
func mapStatus(status int, body []byte) error {
	var env errorEnvelope
	// A malformed/absent envelope still gets a clean sentinel from status
	// alone — decoding failure here is not itself surfaced.
	//craft:ignore swallowed-errors best-effort decode of the provider error envelope — a malformed/absent body still yields a clean status-derived sentinel, and the provider's message is deliberately never surfaced
	_ = json.Unmarshal(body, &env)

	switch status {
	case http.StatusTooManyRequests:
		return apperrors.ErrIncumbentBudgetExhausted
	case http.StatusUnauthorized, http.StatusForbidden:
		return apperrors.ErrPermissionDenied
	default:
		if env.Category == "RATE_LIMITS" {
			return apperrors.ErrIncumbentBudgetExhausted
		}
		return fmt.Errorf("hubspot: request failed with status %d: %w", status, ErrUnreachable)
	}
}
