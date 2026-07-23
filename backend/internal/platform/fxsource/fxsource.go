// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package fxsource fetches current FX rates from a configured public JSON API
// and returns them as from_currency -> base decimal strings (full precision,
// no float rounding), for the rate-refresh producer to diff against the sheet.
package fxsource

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/netguard"
)

const (
	dialTimeout = 10 * time.Second
	httpTimeout = 15 * time.Second
	// ratePrecision matches fx_rate.rate numeric(20,10).
	ratePrecision = 10
	// maxResponseBytes caps the source response before decoding (1 MiB).
	maxResponseBytes = 1 << 20
)

// Client fetches from a base-relative rates API. The expected response shape is
// the common `{"base":"EUR","rates":{"USD":1.08,...}}` form (base -> symbol);
// LatestRates inverts each to symbol -> base.
type Client struct {
	baseURL string
	hc      *http.Client
}

// New builds a Client. A nil http.Client gets the SSRF-guarded default
// (netguard.RefusePrivate in the dialer Control hook, as webread uses).
func New(baseURL string, hc *http.Client) *Client {
	if hc == nil {
		dialer := &net.Dialer{Timeout: dialTimeout, Control: netguard.RefusePrivate}
		hc = &http.Client{Timeout: httpTimeout, Transport: &http.Transport{DialContext: dialer.DialContext}}
	}
	return &Client{baseURL: baseURL, hc: hc}
}

type apiResponse struct {
	Base  string                 `json:"base"`
	Rates map[string]json.Number `json:"rates"`
}

// LatestRates returns symbol -> base decimal strings for the requested
// symbols. A symbol the API omits is skipped (not an error); the caller only
// prices currencies it already tracks.
func (c *Client) LatestRates(ctx context.Context, base string, symbols []string) (map[string]string, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, fmt.Errorf("fxsource: bad base url: %w", err)
	}
	q := u.Query()
	q.Set("base", base)
	if len(symbols) > 0 {
		q.Set("symbols", strings.Join(symbols, ","))
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("fxsource: build request: %w", err)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fxsource: fetch: %w", err)
	}
	//craft:ignore swallowed-errors best-effort close of a response body already fully read; the decode result is the outcome
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fxsource: unexpected status %d", resp.StatusCode)
	}
	var body apiResponse
	// Cap the response before decoding — a large or compromised source must not
	// exhaust worker memory (the SSRF guard and timeout don't bound body size).
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&body); err != nil {
		return nil, fmt.Errorf("fxsource: decode: %w", err)
	}
	// The rates are only correct as symbol->base if the API answered in the base
	// we asked for; a mismatched base would silently stage wrong proposals.
	if !strings.EqualFold(strings.TrimSpace(body.Base), base) {
		return nil, fmt.Errorf("fxsource: response base %q does not match requested %q", body.Base, base)
	}

	out := make(map[string]string, len(symbols))
	want := make(map[string]bool, len(symbols))
	for _, s := range symbols {
		want[strings.ToUpper(s)] = true
	}
	for sym, raw := range body.Rates {
		up := strings.ToUpper(sym)
		if len(symbols) > 0 && !want[up] {
			continue
		}
		inv, err := invert(raw.String())
		if err != nil {
			return nil, fmt.Errorf("fxsource: rate for %s: %w", up, err)
		}
		out[up] = inv
	}
	return out, nil
}

// invert turns a base->symbol rate string into a symbol->base decimal string
// (1/rate) at fx_rate precision, using big.Rat so no float error creeps in. It
// rejects an inverse that rounds to zero or exceeds numeric(20,10)'s 10 integer
// digits — either would only be refused later at the store's write anyway.
func invert(baseToSym string) (string, error) {
	r, ok := new(big.Rat).SetString(strings.TrimSpace(baseToSym))
	if !ok || r.Sign() <= 0 {
		return "", fmt.Errorf("not a positive decimal: %q", baseToSym)
	}
	s := new(big.Rat).Inv(r).FloatString(ratePrecision)
	intPart, fracPart, _ := strings.Cut(s, ".")
	if len(strings.TrimLeft(intPart, "0")) > 10 {
		return "", fmt.Errorf("inverted rate %s exceeds numeric(20,10)", s)
	}
	if strings.Trim(intPart, "0") == "" && strings.Trim(fracPart, "0") == "" {
		return "", fmt.Errorf("inverted rate rounds to zero: %s", s)
	}
	return s, nil
}
