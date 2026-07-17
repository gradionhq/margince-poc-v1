// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package connector

import (
	"errors"
	"fmt"
	"time"
)

// The shared sync-failure vocabulary (ADR-0063). Providers wrap their own
// package errors with these so the registry can schedule without knowing any
// provider: auth parks the connection until its human reconnects, a rate
// limit honors Retry-After, everything else backs off — and no class ever
// tombstones a connection.

// ErrAuthRejected marks a credential the provider refused: expired, revoked,
// or insufficient. The connection needs its human, not a retry.
var ErrAuthRejected = errors.New("connector: authorization rejected")

// ErrUnreachable marks a transient provider/network failure worth backing
// off and retrying.
var ErrUnreachable = errors.New("connector: provider unreachable")

// ErrCursorGone marks a stored sync watermark the provider no longer honors
// (e.g. Gmail 404 on an old historyId): the connector recovers with its
// bounded re-list; the registry records the class, nothing more.
var ErrCursorGone = errors.New("connector: sync cursor no longer valid")

// ErrRateLimited is the errors.Is target for RateLimitedError.
var ErrRateLimited = errors.New("connector: provider rate limit")

// RateLimitedError carries the provider's Retry-After. RetryAfter zero means
// the provider named no delay — the caller falls back to its own backoff.
type RateLimitedError struct {
	RetryAfter time.Duration
}

func (e *RateLimitedError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("connector: provider rate limit (retry after %s)", e.RetryAfter)
	}
	return "connector: provider rate limit"
}

// Is makes every RateLimitedError answer errors.Is(err, ErrRateLimited), so
// callers classify on the sentinel and read Retry-After via errors.As.
func (e *RateLimitedError) Is(target error) bool { return target == ErrRateLimited }
