// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// THE TWO OUTCOMES A CALL TO ZALO CAN HAVE, and the redaction that makes either one
// safe to write down.
//
// Split from zalohttp.go, which owns the jar and the request helper, because a reader
// arrives here with a different question: not "how is a call made" but "what does a
// failed one mean, and what may it say". The pair is the whole of the answer —
// "Zalo refused" and "we never heard" have opposite recovery actions on a send — and
// the redaction is what keeps a credential out of the one value on this path that is
// designed to be logged.

import (
	"errors"
	"fmt"
	"net/url"
)

// transportError is the outcome-unknown boundary: the request left this process
// and no answer this package can read came back. It is a distinct type because
// "Zalo refused" and "we never heard" have opposite recovery actions on a send
// — retrying the first is correct, retrying the second duplicates a message.
type transportError struct {
	Method string
	// URL is already stripped of its query by safeURL at construction, so this
	// struct cannot carry a credential even into a %+v.
	URL string
	Err error
}

func (e *transportError) Error() string {
	return fmt.Sprintf("%s %s did not complete: %v", e.Method, e.URL, e.Err)
}

// safeURL renders a request URL with its QUERY REMOVED, which is the only form
// that may reach a log or an operator.
//
// This layer's secrets travel as query parameters: `imei` is the device
// identity, and `zcid`/`zcid_ext` derive the ephemeral login key through a
// constant Zalo ships in its own bundle — so anyone holding a logged URL holds
// the key that decrypts the `params` blob beside it. An error is the one value
// on this path that is designed to be written down, so it is the one place the
// query may never appear.
func safeURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		// The URL is unusable for reporting, and guessing at a prefix of a
		// string that may hold a credential is worse than saying nothing.
		return "(an unparseable URL)"
	}
	u.RawQuery = ""
	u.Fragment = ""
	u.User = nil
	return u.String()
}

func (e *transportError) Unwrap() error { return e.Err }

// unreachable wraps a failed round trip, with the query stripped from the error
// the round trip itself answered.
//
// IT IS NOT ENOUGH TO REDACT THE URL FIELD. http.Client.Do answers a *url.Error
// whose own Error() prints the WHOLE request URL — the stdlib redacts userinfo and
// the query never — so the commonest failures on this path (a timeout, a DNS miss,
// a refused connection, a TLS handshake Zalo did not finish, the per-member budget
// expiring mid-login) carry imei, zcid, zcid_ext, params and signkey inside the
// WRAPPED error, where the redacted URL beside them proves nothing. These strings
// reach the job runner's log and a member's delivery record, and per safeURL above
// anyone holding one holds the key that decrypts the params blob next to it.
//
// The url.Error is rebuilt rather than dropped, so what actually failed still
// unwraps: errors.Is against context.DeadlineExceeded, and net.Error.Timeout()
// through it, both keep answering.
func unreachable(method, safe string, err error) *transportError {
	var routed *url.Error
	if errors.As(err, &routed) {
		err = &url.Error{Op: routed.Op, URL: safe, Err: routed.Err}
	}
	return &transportError{Method: method, URL: safe, Err: err}
}

// refusalError is the mirror of transportError: an answer Zalo actually sent,
// in which the server read the request and said no. Every endpoint in this
// layer reports one, and the whole point of the pair is that a caller can tell
// "the provider refused" from "we never heard" — see errUnanswered.
type refusalError struct {
	Endpoint string
	Code     int
	Message  string
}

func (e *refusalError) Error() string {
	return fmt.Sprintf("%s refused (error_code %d): %s", e.Endpoint, e.Code, e.Message)
}
