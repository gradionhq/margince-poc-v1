// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What a failed site read RECORDS, and which failures are worth another try.
//
// Before this, every failure was stored as a bare status='failed' with no code,
// no sentence and no retry time: a real import produced 58 of them, all
// identical, and the companies behind them kept an empty record whose cause
// nobody could see. These cases pin each cause to the code an operator groups
// by and to the retry decision that follows from it.

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/webread"
)

func TestDiagnoseCrawlFailureNamesTheCauseAndItsRetryPolicy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		cause     error
		wantCode  string
		wantRetry bool
	}{
		{
			// The Surfe shape: an edge refused the crawler, intermittently.
			// Settling the domain on this is how a real business ends up
			// permanently recorded as nothing but its domain name.
			name:     "bot protection is worth another attempt",
			cause:    &webread.StatusError{Status: 403, URL: "https://surfe.com"},
			wantCode: "bot_blocked", wantRetry: true,
		},
		{
			name:     "rate limiting is the same answer",
			cause:    &webread.StatusError{Status: 429, URL: "https://example.test"},
			wantCode: "bot_blocked", wantRetry: true,
		},
		{
			name:     "the site's own fault clears on its own",
			cause:    &webread.StatusError{Status: 503, URL: "https://example.test"},
			wantCode: "http_server_error", wantRetry: true,
		},
		{
			name:     "a missing page will still be missing tomorrow",
			cause:    &webread.StatusError{Status: 404, URL: "https://example.test"},
			wantCode: "http_client_error", wantRetry: false,
		},
		{
			// The Ausgezeichnet shape: the apex served a certificate that does
			// not verify.
			name:     "an unverifiable certificate is not retried",
			cause:    fmt.Errorf("get: %w", x509.UnknownAuthorityError{}),
			wantCode: "tls", wantRetry: false,
		},
		{
			name:     "a name that does not resolve is not retried",
			cause:    fmt.Errorf("dial: %w", &net.DNSError{Err: "no such host", Name: "nope.test"}),
			wantCode: "dns", wantRetry: false,
		},
		{
			name:     "a robots refusal is the site's answer, not a failure to repeat",
			cause:    fmt.Errorf("fetch: %w", webread.ErrRobotsDisallowed),
			wantCode: "robots_disallowed", wantRetry: false,
		},
		{
			name:     "a timeout is worth another attempt",
			cause:    fmt.Errorf("read: %w", context.DeadlineExceeded),
			wantCode: "timeout", wantRetry: true,
		},
		{
			name:     "anything else is recorded as unreadable rather than guessed at",
			cause:    errors.New("something went sideways"),
			wantCode: "unreadable", wantRetry: false,
		},
		{
			name:     "a failure with no cause still says so",
			cause:    nil,
			wantCode: "internal", wantRetry: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			code, detail := diagnoseCrawlFailure(tc.cause)
			if code != tc.wantCode {
				t.Errorf("code = %q, want %q", code, tc.wantCode)
			}
			if detail == "" {
				t.Error("detail is empty — a failure with no sentence is the state this replaces")
			}
			if retry := people.SiteReadFailureCodes[code]; retry != tc.wantRetry {
				t.Errorf("retryable = %v, want %v", retry, tc.wantRetry)
			}
		})
	}
}

// Every code the classifier can emit has to be one the store and the database
// accept, or a real failure would be recorded as a constraint violation.
func TestEveryDiagnosedCodeIsInTheStoreVocabulary(t *testing.T) {
	t.Parallel()
	causes := []error{
		nil,
		errors.New("unknown"),
		&webread.StatusError{Status: 403},
		&webread.StatusError{Status: 500},
		&webread.StatusError{Status: 404},
		fmt.Errorf("%w", webread.ErrRobotsDisallowed),
		fmt.Errorf("%w", context.DeadlineExceeded),
		&net.DNSError{Err: "no such host"},
		x509.UnknownAuthorityError{},
	}
	for _, cause := range causes {
		code, _ := diagnoseCrawlFailure(cause)
		if _, known := people.SiteReadFailureCodes[code]; !known {
			t.Errorf("diagnoseCrawlFailure(%v) produced %q, which the store would reject", cause, code)
		}
	}
}
