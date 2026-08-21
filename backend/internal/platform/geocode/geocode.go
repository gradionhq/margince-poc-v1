// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package geocode turns a postal address into a point on the earth.
//
// It exists because "which customers are near Stuttgart" is a question the
// query surface could parse and not answer: within_radius was in the grammar
// and every plan carrying it came back unavailable, because no row had
// coordinates to compare.
//
// ONE PROVIDER TODAY, and the interface is what keeps that from hardening.
// Nominatim is OpenStreetMap's free service: no key, no contract, and a usage
// policy this package takes seriously rather than works around. A self-hosted
// instance is the same client with a different base URL.
package geocode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Point is a resolved location.
type Point struct {
	Lat, Lon float64
}

// Client resolves free text or a structured address to a point.
//
// An address that resolves to nothing is NOT an error: the geocoder did its
// job and the answer is "no such place", which is a fact about the address and
// must be recorded as one rather than retried forever. ok=false says that;
// err is reserved for a lookup that did not complete.
type Client interface {
	Resolve(ctx context.Context, query string) (Point, bool, error)
}

// The public Nominatim service, and the rate it may be asked at.
const (
	// PublicBaseURL is OSM's own instance. POC-only: a deployment that geocodes
	// at any volume should point at its own.
	PublicBaseURL = "https://nominatim.openstreetmap.org"

	// RecurringInterval is the floor between requests, and it is 15 SECONDS
	// rather than the 1s the policy allows an interactive client.
	//
	// OSMF's usage policy caps the public service at roughly 1 request/second
	// in general, but a script that runs regularly or for longer than a day is
	// held to 4 requests per minute, single-threaded, with caching. Geocoding
	// on ingestion is exactly such a job — it runs whenever a company is read,
	// indefinitely — so 15s is the rate this package is entitled to, not a
	// conservative reading of a faster one.
	RecurringInterval = 15 * time.Second

	// UserAgent names this software and follows the same convention as the
	// site reader. The policy requires an identifiable agent; an anonymous or
	// spoofed one is how an installation gets the whole service blocked.
	UserAgent = "margince-geocode/1.0"
)

// ErrNotConfigured says this deployment has no geocoder. It is a REFUSAL, not
// a failure: an offline or demo installation geocodes nothing on purpose, and
// the caller records that rather than retrying.
var ErrNotConfigured = errors.New("geocode: no geocoding provider is configured")

// Nominatim is the OSM client.
type Nominatim struct {
	baseURL string
	http    *http.Client
	pacer   *Pacer
}

// NewNominatim builds the client. An empty baseURL means the public service.
//
// The pacer is created here and held by the client, so ONE client is one
// requester: the policy's single-thread requirement is a property of the
// object, and two clients would be two requesters however carefully each
// paced itself. The composition root builds exactly one.
func NewNominatim(baseURL string, httpClient *http.Client) *Nominatim {
	if baseURL == "" {
		baseURL = PublicBaseURL
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	return &Nominatim{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		http:    httpClient,
		pacer:   NewPacer(RecurringInterval),
	}
}

// nominatimResult is the one field set this package reads back. The service
// returns a great deal more; taking only the coordinates keeps the parse
// stable across its versions.
type nominatimResult struct {
	Lat string `json:"lat"`
	Lon string `json:"lon"`
}

// Resolve asks the provider where a place is.
//
// Three outcomes, and telling them apart is the caller's whole retry policy: a
// point, a definite "not a place" (ok=false, no error — recorded and never
// re-asked), and a lookup that did not complete (an error — retried, and after
// a 429 retried on the provider's own schedule via ProviderRefusedError).
func (n *Nominatim) Resolve(ctx context.Context, query string) (Point, bool, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return Point{}, false, nil
	}
	// The pace is taken BEFORE the request is built, and it is the whole
	// reason this client is safe to run on a schedule.
	if err := n.pacer.Wait(ctx); err != nil {
		return Point{}, false, err
	}

	endpoint := n.baseURL + "/search?" + url.Values{
		"q":      {query},
		"format": {"jsonv2"},
		"limit":  {"1"},
	}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Point{}, false, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := n.http.Do(req)
	if err != nil {
		return Point{}, false, fmt.Errorf("geocode: asking the provider: %w", err)
	}
	//craft:ignore swallowed-errors best-effort close: the decode below reads what it needs and may leave the body mid-stream, so a close error says nothing about whether the address resolved
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// A 429 or a 403 means this installation is being rate-limited or
		// blocked — a condition to surface rather than absorb, and one that
		// carries its own instruction about when to come back.
		return Point{}, false, &ProviderRefusedError{
			Status:     resp.StatusCode,
			RetryAfter: retryAfter(resp.Header.Get("Retry-After")),
		}
	}

	var results []nominatimResult
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return Point{}, false, fmt.Errorf("geocode: reading the provider's answer: %w", err)
	}
	if len(results) == 0 {
		// No match. A FACT about the address, not a failure of the lookup.
		return Point{}, false, nil
	}
	lat, err := strconv.ParseFloat(results[0].Lat, 64)
	if err != nil {
		return Point{}, false, fmt.Errorf("geocode: the provider's latitude %q is not a number: %w",
			results[0].Lat, err)
	}
	lon, err := strconv.ParseFloat(results[0].Lon, 64)
	if err != nil {
		return Point{}, false, fmt.Errorf("geocode: the provider's longitude %q is not a number: %w",
			results[0].Lon, err)
	}
	if !plausible(lat, lon) {
		return Point{}, false, fmt.Errorf("geocode: the provider answered (%v, %v), which is not on the earth", lat, lon)
	}
	return Point{Lat: lat, Lon: lon}, true, nil
}

// plausible rejects a coordinate outside the earth's own range.
//
// Not paranoia about the provider: it is the cheapest place to catch a
// transposed lat/lon pair, which is the mistake that produces confidently
// wrong distances rather than an error anyone notices.
func plausible(lat, lon float64) bool {
	return lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}

// ProviderRefusedError is the provider declining, with the wait it asked for.
//
// The wait is the part worth carrying. A 429 means this installation is asking
// too often, and retrying on the job runner's own schedule rather than the
// provider's is how a rate limit becomes a block.
type ProviderRefusedError struct {
	Status int
	// RetryAfter is what the provider asked for, or zero when it said nothing.
	RetryAfter time.Duration
}

func (e *ProviderRefusedError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("geocode: the provider answered %d and asked for %s before the next request",
			e.Status, e.RetryAfter)
	}
	return fmt.Sprintf("geocode: the provider answered %d", e.Status)
}

// retryAfter reads the header, in either form the RFC allows.
//
// A malformed or absent value is zero rather than a guess: the caller's own
// backoff is the fallback, and inventing a number the provider did not give
// would be a worse answer than admitting it said nothing.
func retryAfter(header string) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(header); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(header); err == nil {
		if wait := time.Until(at); wait > 0 {
			return wait
		}
	}
	return 0
}
