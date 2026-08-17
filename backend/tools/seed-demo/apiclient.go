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

// sessions is a pool of signed-in clients, one per demo seat.
//
// Most records the seeder writes are the same whoever wrote them, but an
// activity is not: who recorded a conversation is who the product then says
// knows that contact. Writing every mail as one account makes that colleague
// know everybody, and "who can introduce me?" answers the same name forever.
type sessions struct {
	base     string
	password string
	byRef    map[string]*client
	fallback *client
}

func newSessions(base, password string, fallback *client) *sessions {
	return &sessions{base: base, password: password, byRef: map[string]*client{}, fallback: fallback}
}

// as returns a client signed in as one seat, opening the session on first use.
//
// A seat that cannot sign in falls back to the seeding account rather than
// failing the run: one missing name on a network card is worth less than the
// whole seed, and the fallback shows up plainly in what that card then says.
func (s *sessions) as(user demoUser) *client {
	if user.Email == "" || s.password == "" {
		return s.fallback
	}
	if existing, ok := s.byRef[user.Ref]; ok {
		return existing
	}
	signed, err := login(s.base, user.Email, s.password)
	if err != nil {
		s.byRef[user.Ref] = s.fallback
		return s.fallback
	}
	s.byRef[user.Ref] = signed
	return signed
}

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

// patch sends a partial update — the shape a record edit takes, as opposed to
// the whole-record replace put does.
func (c *client) patch(path string, body jsonBody, out any) error { //craft:ignore naked-any out is any JSON shape the caller declares; json.Decode's own contract
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encoding request: %w", err)
	}
	req, err := http.NewRequest(http.MethodPatch, c.base+path, bytes.NewReader(encoded))
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

// seedAdminPassword is what the bootstrap account lands on once the
// operator-supplied credential has been replaced.
//
// The same value scripts/seed-dev.sh uses, deliberately: an installation
// seeded by either tool answers to one documented password, and the demo
// logins in the README stay true whichever path somebody took.
const seedAdminPassword = "demo-password-123"

// replaceOperatorPassword takes the bootstrap account off its first-login
// hold, and returns a client signed in afterwards.
//
// A configured bootstrap sets must_change_password (migration 0273), and every
// write is refused with 403 password_change_required until the operator's
// chosen credential has been REPLACED. Rotating it to itself does not count —
// the product refuses that with "the new password must differ from the current
// one", which is the rule working: the operator's password is meant to have no
// life beyond the first login.
//
// The change ends every session including the one that made it, so this signs
// in again and hands back the new client.
func replaceOperatorPassword(baseURL, email, password string, c *client) (*client, string, error) {
	body := jsonBody{"current_password": password, "new_password": seedAdminPassword}
	if err := c.post("/v1/auth/change-password", body, nil); err != nil {
		// Already replaced by an earlier run or by seed-dev: the account is
		// not on hold and the current client is fine to carry on with.
		if isUnprocessable(err) || isConflict(err) {
			return c, password, nil
		}
		return nil, password, fmt.Errorf("replacing the operator-supplied password: %w", err)
	}
	fresh, err := login(baseURL, email, seedAdminPassword)
	if err != nil {
		return nil, password, fmt.Errorf("signing in after replacing the password: %w", err)
	}
	fmt.Printf("admin:         operator password replaced with %q\n", seedAdminPassword)
	return fresh, seedAdminPassword, nil
}

// delete sends a DELETE, which for some resources is how a state is REACHED
// rather than how a row is removed: disqualifying a lead is
// `DELETE /v1/leads/{id}`, and it sets status=disqualified and archives the
// row instead of deleting it.
func (c *client) delete(path string) error {
	req, err := http.NewRequest(http.MethodDelete, c.base+path, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	return c.do(req, nil)
}

// pageLimit is the page size getAll asks for. The contract caps a page well
// below the dataset's size, so the number only decides how many round trips a
// full read costs, never whether it is complete.
const pageLimit = 100

// getAll reads EVERY row a list endpoint has, following the cursor.
//
// A single `limit=200` read is a silent truncation waiting to happen: it
// answers 200 rows and looks identical whether that is all of them or the
// first page of a thousand. The seeder used to do exactly that in a dozen
// places, which was survivable at 21 companies and is not at 200 — the
// ownership pass would simply stop owning things partway down the list, and
// an unowned row is workspace-shared, so the failure would show up as a
// broken access model rather than as a missing row.
//
// The caller supplies a decode function rather than a typed slice because
// every list endpoint returns a different row shape, and Go generics on a
// method are not available. Each page is decoded into the caller's own struct
// and appended by the caller.
func (c *client) getAll(path string, query url.Values, decode func(json.RawMessage) error) error {
	page := url.Values{}
	for key, values := range query {
		page[key] = values
	}
	page.Set("limit", fmt.Sprint(pageLimit))

	// A cursor the server keeps returning unchanged would spin forever. The
	// bound is far above any real dataset (100 pages = 10,000 rows) and turns
	// a server-side pagination bug into an error instead of a hang.
	const maxPages = 100
	for i := 0; ; i++ {
		if i == maxPages {
			return fmt.Errorf("%s: still paginating after %d pages — the cursor is not advancing", path, maxPages)
		}
		var body struct {
			Data json.RawMessage `json:"data"`
			Page struct {
				NextCursor string `json:"next_cursor"`
				HasMore    bool   `json:"has_more"`
			} `json:"page"`
		}
		if err := c.get(path, page, &body); err != nil {
			return err
		}
		if err := decode(body.Data); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if !body.Page.HasMore || body.Page.NextCursor == "" {
			return nil
		}
		page.Set("cursor", body.Page.NextCursor)
	}
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

// isUnprocessable reports the server refusing a request it understood. For
// the password rotation it means the account is not on a first-login hold,
// which is a state to carry on from rather than stop for.
func isUnprocessable(err error) bool {
	var apiErr apiError
	if !asAPIError(err, &apiErr) {
		return false
	}
	return apiErr.Status == http.StatusUnprocessableEntity
}

// isNotFound reports the server hiding a record from this caller. Row scope
// answers 404 rather than 403 on purpose — a 403 would confirm the record
// exists — so this is as often "not yours" as "not there".
func isNotFound(err error) bool {
	var apiErr apiError
	return asAPIError(err, &apiErr) && apiErr.Status == http.StatusNotFound
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
