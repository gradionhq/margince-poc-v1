// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The HTTP client the seeder writes through — a session cookie and JSON, the
// same door a browser uses. Nothing here reaches the database.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

type client struct {
	base string
	http *http.Client
}

// login opens a session and returns a client that carries its cookie.
func login(base, email, password string) (*client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("cookie jar: %w", err)
	}
	c := &client{
		base: strings.TrimSuffix(base, "/"),
		http: &http.Client{Jar: jar, Timeout: 30 * time.Second},
	}

	var out struct {
		User struct {
			Email string `json:"email"`
		} `json:"user"`
	}
	body := jsonBody{"email": email, "password": password}
	if err := c.post("/v1/auth/login", body, &out); err != nil {
		return nil, fmt.Errorf("login as %s: %w", email, err)
	}
	return c, nil
}

// jsonBody is a request body on its way to the API: field names exactly as
// the contract spells them. A map rather than a struct per endpoint because
// the seeder builds bodies conditionally — a company with no legal name sends
// no legal_name key at all, which a struct would have to model with pointers
// everywhere.
type jsonBody map[string]any

// post sends a JSON body and decodes the reply into out, which is a pointer
// to the caller's result struct, or nil to discard the response.
func (c *client) post(path string, body jsonBody, out any) error { //craft:ignore naked-any out is any JSON shape the caller declares; json.Decode's own contract
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encoding request: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, c.base+path, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

// put sends a JSON body to an idempotent endpoint — a save that creates on
// first call and updates after, so it needs no probe of its own.
func (c *client) put(path string, body jsonBody, out any) error { //craft:ignore naked-any out is any JSON shape the caller declares; json.Decode's own contract
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encoding request: %w", err)
	}
	req, err := http.NewRequest(http.MethodPut, c.base+path, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

// get decodes a GET into out. Query values are escaped here rather than by
// the caller so a company name with an ampersand cannot build a broken URL.
func (c *client) get(path string, query url.Values, out any) error { //craft:ignore naked-any out is any JSON shape the caller declares; json.Decode's own contract
	full := c.base + path
	if len(query) > 0 {
		full += "?" + query.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, full, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	return c.do(req, out)
}

func (c *client) do(req *http.Request, out any) error { //craft:ignore naked-any out is any JSON shape the caller declares; json.Decode's own contract
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", req.Method, req.URL.Path, err)
	}
	defer func() {
		// Draining before close lets the connection be reused; the seeder
		// makes hundreds of these calls. Neither result can be acted on here
		// — the response has already been read or has already failed — so
		// both are deliberately discarded rather than silently ignored.
		_, _ = io.Copy(io.Discard, resp.Body) //craft:ignore swallowed-errors a drain before close has no failure the caller can act on
		_ = resp.Body.Close()                 //craft:ignore swallowed-errors closing a read response body cannot fail usefully
	}()

	if resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return apiError{Status: resp.StatusCode, Method: req.Method, Path: req.URL.Path, Body: string(detail)}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding %s %s: %w", req.Method, req.URL.Path, err)
	}
	return nil
}

// apiError carries the server's own problem detail, because a seeder that
// reports "409" without saying what conflicted sends the reader to the logs.
type apiError struct {
	Status int
	Method string
	Path   string
	Body   string
}

func (e apiError) Error() string {
	body := strings.TrimSpace(e.Body)
	if len(body) > 300 {
		body = body[:300] + "…"
	}
	if body == "" {
		return fmt.Sprintf("%s %s: HTTP %d", e.Method, e.Path, e.Status)
	}
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.Method, e.Path, e.Status, body)
}

// isConflict reports whether an error is the server refusing a duplicate,
// which for a converging seeder means "an earlier run already did this".
func isConflict(err error) bool {
	var apiErr apiError
	if !asAPIError(err, &apiErr) {
		return false
	}
	return apiErr.Status == http.StatusConflict
}

func asAPIError(err error, target *apiError) bool {
	if converted, ok := err.(apiError); ok { //nolint:errorlint // the only wrapper here is fmt.Errorf at call sites that do not wrap this type
		*target = converted
		return true
	}
	return false
}

// conflictingID pulls the existing record's id out of a duplicate refusal.
// The server names what it collided with (`details.existing_id`), which is a
// firmer answer than searching for the row again: search runs behind an
// index, and a record written moments ago is the one case it may not carry
// yet — that gap is what made a second seeding run fail rather than converge.
func conflictingID(err error) (string, bool) {
	var apiErr apiError
	if !asAPIError(err, &apiErr) || apiErr.Status != http.StatusConflict {
		return "", false
	}
	var problem struct {
		Details struct {
			ExistingID string `json:"existing_id"`
		} `json:"details"`
	}
	if json.Unmarshal([]byte(apiErr.Body), &problem) != nil {
		return "", false
	}
	return problem.Details.ExistingID, problem.Details.ExistingID != ""
}
