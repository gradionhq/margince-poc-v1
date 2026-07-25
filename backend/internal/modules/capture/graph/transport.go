// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The Graph HTTP transport: the one request path every read call goes through
// and the single verdict it maps a response onto. Split out of client.go
// because it belongs to no individual call — the OAuth handshake and the mail
// surface both sit on it — so a reader looking for "how does a Graph failure
// become a sentinel" finds one file, not a tail.

package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// retryAfter parses the provider's Retry-After (delta-seconds form; Graph's
// throttling responses use it). Zero when absent — the caller's own backoff
// takes over.
func retryAfter(resp *http.Response) time.Duration {
	if s := resp.Header.Get("Retry-After"); s != "" {
		if secs, err := strconv.Atoi(s); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return 0
}

// messageOp names the raw-MIME fetch in a ProviderError. Its sibling calls carry
// their URL path as the op; this one is a hand-built request whose URL embeds the
// message id, so it names the endpoint rather than the instance.
const messageOp = "message"

// requestOp names a Graph call in a ProviderError the way the other connectors
// do — as a path relative to the API base. The query string is cut because it
// carries per-request filters and cursors, and the base is trimmed because an
// error string wants the endpoint that failed, not the scheme and host it lives
// on (which are fixed for the deployment and, under test, an ephemeral port).
func (a *httpAPI) requestOp(fullURL string) string {
	op, _, _ := strings.Cut(fullURL, "?")
	trimmed := strings.TrimPrefix(op, a.base)
	if trimmed == "" {
		// The base itself, with nothing after it: name the root rather than
		// render an empty op behind a leading colon.
		return "/"
	}
	return trimmed
}

// graphErrorBody is the subset of Microsoft's OData error envelope that names
// the failure. code is the fixed machine code (InvalidAuthenticationToken,
// accessDenied, …); the prose message beside it is deliberately not read.
type graphErrorBody struct {
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

// graphReason extracts Microsoft's machine error code from a response body, ""
// when the body carries none or does not decode — an unparsable body must not
// masquerade as a named reason.
func graphReason(body []byte) string {
	var parsed graphErrorBody
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	return connector.MachineReason(parsed.Error.Code)
}

// classifyStatus maps a non-2xx Graph response onto the shared connector
// vocabulary: 429 honors Retry-After, 401/403 parks the credential, anything
// else backs off. The classification is unchanged by op/body — those only carry
// Microsoft's own machine code into the error so a log line says WHICH call
// failed and WHY Microsoft refused it. The raw body is never surfaced to the
// caller.
func classifyStatus(resp *http.Response, op string, body []byte) error {
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return &connector.RateLimitedError{RetryAfter: retryAfter(resp)}
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return &connector.ProviderError{
			Op: op, Status: resp.StatusCode, Reason: graphReason(body), Class: ErrAuthRejected,
		}
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return &connector.ProviderError{
			Op: op, Status: resp.StatusCode, Reason: graphReason(body), Class: ErrUnreachable,
		}
	}
	return nil
}

// get performs an authorized GET on a full URL (extra headers optional) and
// JSON-decodes into out. It returns the HTTP status (so deltaWalk can
// special-case 410) alongside the classified error.
//
//craft:ignore naked-any out is the caller-supplied JSON decode target — its concrete type varies per endpoint
func (a *httpAPI) get(ctx context.Context, accessToken, fullURL string, hdr http.Header, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return 0, fmt.Errorf("graph: building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("graph: request: %w", ErrUnreachable)
	}
	//craft:ignore swallowed-errors best-effort close of the response body — the decoded result/status is what matters
	defer func() { _ = resp.Body.Close() }()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	// Classify on status/headers first: a 429/401 must be honored even if the
	// body read failed. Only on an otherwise-OK response does a read failure
	// matter — a truncated-but-valid-JSON prefix must never pass as complete.
	if err := classifyStatus(resp, a.requestOp(fullURL), body); err != nil {
		return resp.StatusCode, err
	}
	if readErr != nil {
		return resp.StatusCode, fmt.Errorf("graph: reading response: %w", ErrUnreachable)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return resp.StatusCode, fmt.Errorf("graph: decoding response: %w", ErrUnreachable)
	}
	return resp.StatusCode, nil
}
