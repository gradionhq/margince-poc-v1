// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalooa

// The transport under the calls: how a request is built for this provider's
// unusual GET grammar, and how the one envelope every endpoint answers in is
// read.
//
// It is a file of its own because this is where the provider's central trap
// lives, and it reads better beside the classifier than among the calls: EVERY
// response is HTTP 200, so the status line is consulted only to notice that
// something OTHER than this API answered, and every outcome a caller acts on is
// derived from the body.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// get calls one of the OpenAPI's GET endpoints.
//
// The parameter grammar is the provider's and it is unusual enough to be worth
// naming: the WHOLE parameter object rides in one `data` query parameter as
// JSON, rather than as ordinary query arguments.
//
//craft:ignore naked-any the decode target is whatever the caller reads a provider answer into — the same contract encoding/json itself has, and there is no method-level type parameter in Go to state it with
func (c *client) get(ctx context.Context, path string, params map[string]any, into any) error {
	endpoint, err := url.Parse(c.base + path)
	if err != nil {
		return fmt.Errorf("%w: building the request: %s", errProvider, err.Error())
	}
	if params != nil {
		encoded, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("%w: encoding the data parameter: %s", errProvider, err.Error())
		}
		endpoint.RawQuery = url.Values{"data": {string(encoded)}}.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return fmt.Errorf("%w: building the request: %s", errProvider, err.Error())
	}
	return c.do(req, into)
}

//craft:ignore naked-any the request body and the decode target are both the caller's shapes; see get
func (c *client) post(ctx context.Context, path string, body any, into any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("%w: encoding the request: %s", errProvider, err.Error())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, strings.NewReader(string(encoded)))
	if err != nil {
		return fmt.Errorf("%w: building the request: %s", errProvider, err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, into)
}

// do runs one request and reads the envelope every OA endpoint answers in.
//
//craft:ignore naked-any the decode target is the caller's shape; see get
func (c *client) do(req *http.Request, into any) error {
	// The credential rides its OWN header, not Authorization: Zalo names it
	// `access_token` and ignores a bearer.
	req.Header.Set("access_token", c.token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		// A transport failure is transient from a read's point of view.
		// errUnanswered rides along because the SEND path needs a distinction a
		// poll does not: the request may have arrived. See send.go.
		return fmt.Errorf("%w: %w: %s", errTransient, errUnanswered, err.Error())
	}
	//craft:ignore swallowed-errors best-effort close: the capped read below may leave the body mid-stream, so a close error carries no signal for this call's result
	defer func() { _ = resp.Body.Close() }()
	// Bounded, because a provider deciding how much this worker reads into
	// memory is the same class of problem as one deciding how much it stores.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		// Also unanswered: the provider accepted the request and this side never
		// learned what it decided.
		return fmt.Errorf("%w: %w: reading the answer: %s", errTransient, errUnanswered, err.Error())
	}
	if len(body) > maxResponseBytes {
		// UNANSWERED, not merely unusable: the provider accepted the request and
		// this side gave up on reading what it decided. For a read that is the
		// same as any other failure; for a send it is the difference between
		// retrying and messaging a customer twice.
		return fmt.Errorf("%w: %w: the answer is over the %d-byte cap", errProvider, errUnanswered, maxResponseBytes)
	}
	// The status is read ONLY to notice that something other than the API
	// answered — a proxy, a maintenance page, a gateway. Every real answer this
	// provider gives is 200 whatever it decided, so the classification below is
	// the body's.
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: the transport answered %d, which is not this API speaking", errTransient, resp.StatusCode)
	}
	return decodeEnvelope(body, into)
}

// decodeEnvelope reads the `{error, message, data}` wrapper every OA endpoint
// answers in, classifies the code, and hands `data` to the caller.
//
//craft:ignore naked-any the decode target is the caller's shape; see get
func decodeEnvelope(body []byte, into any) error {
	var envelope struct {
		Error   int             `json:"error"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		// Unanswered for the reason the over-cap refusal above is: something came
		// back and this side cannot say what it decided.
		return fmt.Errorf("%w: %w: the answer is not the envelope this unit reads", errProvider, errUnanswered)
	}
	// FROM HERE THE PROVIDER HAS SPOKEN. A code it names is a decision, and a
	// decision is never the unknown-outcome class — reporting a refusal as
	// unknowable would stop a delivery that could simply be corrected.
	if err := classify(envelope.Error); err != nil {
		return err
	}
	if len(envelope.Data) == 0 {
		// A success carrying no data is a shape this unit has no reading of, and
		// decoding nothing into the caller's target would leave it holding the
		// zero value as though the provider had said so. It reported SUCCESS, so
		// for a send the message is gone and the outcome is unknowable only in
		// its detail — which is the same class, for the same reason.
		return fmt.Errorf("%w: %w: the answer reported success and carried no data", errProvider, errUnanswered)
	}
	if err := json.Unmarshal(envelope.Data, into); err != nil {
		return fmt.Errorf("%w: %w: the answer's data is not the shape this unit reads", errProvider, errUnanswered)
	}
	return nil
}

// classify maps the body's own code onto the classes a caller can act on.
//
// The three refusals that are told apart here are told apart because each costs
// something different to resolve: a token is refreshed by this unit, an app
// permission is a free click in a console, and a package is an annual purchase.
// Collapsing any two of them into "not available" hands an operator an
// instruction that is wrong in a way they cannot check.
func classify(code int) error {
	switch code {
	case codeOK:
		return nil
	case codeTokenExpired, codeTokenInvalid:
		return errUnauthorized
	case codeTierTooLow:
		return errTierTooLow
	case codeAPINotRegisterd:
		return errAPINotRegistered
	case codeRateLimited:
		return fmt.Errorf("%w: the provider is rate limiting this account", errTransient)
	case codeInvalidArgument, codePageTooLarge:
		// A caller bug, and it must not be retried on a cadence: the same
		// arguments produce the same refusal forever.
		return fmt.Errorf("%w: the provider refused the arguments (%d)", errProvider, code)
	default:
		return fmt.Errorf("%w: the provider answered %d", errProvider, code)
	}
}
