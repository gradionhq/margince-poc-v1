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

// ProviderError carries the provider's OWN diagnosis alongside the shared
// class. The class alone answers "park or retry?", which is all the scheduler
// needs — but it cannot tell an operator WHY, and two failures that schedule
// identically can need opposite human responses: a refused credential wants
// its human to reconnect, while a provider API that was never enabled for the
// deployment wants an administrator, and no amount of reconnecting will fix
// it. Op names the call that failed, Status the provider's HTTP status, and
// Reason the provider's machine reason code — the three facts that turn "the
// authorization was rejected" into an actionable log line.
//
// Reason is the provider's fixed machine code (Google's "accessNotConfigured",
// OAuth2's "invalid_grant"), never its prose message and never a fragment of
// its body: the raw body still stops at the transport boundary.
//
// It classifies exactly as the sentinel it wraps — Unwrap keeps errors.Is
// answering the same way the bare sentinel did — so scheduling can never come
// to depend on the detail.
type ProviderError struct {
	// Op is the failing call, in the provider's own terms: an API path
	// ("/calendars/primary") or the handshake step ("token").
	Op string
	// Status is the provider's HTTP status. Every construction site is an
	// HTTP-response path, so it is always set — a transport failure that never
	// reached a response carries its own wrapped sentinel instead.
	Status int
	// Reason is the provider's machine reason code; empty when it named none.
	Reason string
	// Class is the ADR-0063 sentinel (through the connector's own wrapper, so
	// the provider's log identity survives) this failure classifies as.
	Class error
}

func (e *ProviderError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("%s: provider said %d %s: %v", e.Op, e.Status, e.Reason, e.Class)
	}
	return fmt.Sprintf("%s: provider said %d: %v", e.Op, e.Status, e.Class)
}

// maxReasonLen bounds a machine reason. Every real one is a short identifier
// (accessNotConfigured, invalid_grant, AADSTS700016); the bodies they are parsed
// out of, by contrast, are read up to megabytes. Without a bound, one hostile or
// corrupted response could put an arbitrarily long string into a log line and
// into the system_log row a sync failure writes.
const maxReasonLen = 64

// MachineReason accepts s only if it looks like a provider's machine code — a
// short identifier of letters, digits, '_', '-' and '.' — and returns "" for
// anything else. Every parser passes its extracted reason through here, so
// prose, an oversized value, and control characters (a newline would be a
// log-forgery attempt) are all rejected at one chokepoint rather than trusted
// because the provider is nominally reputable.
func MachineReason(s string) string {
	if s == "" || len(s) > maxReasonLen {
		return ""
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_', r == '-', r == '.':
		default:
			return ""
		}
	}
	return s
}

// Unwrap exposes the wrapped class, so errors.Is(err, ErrAuthRejected) — and
// every other sentinel check — answers exactly as it did before the detail
// was carried.
func (e *ProviderError) Unwrap() error { return e.Class }

// ProviderReason returns the provider's machine reason code carried by err, or
// "" when err carries none. It is the one read path for the detail, so a
// caller never has to know whether the error is wrapped.
func ProviderReason(err error) string {
	if pe, ok := errors.AsType[*ProviderError](err); ok {
		return pe.Reason
	}
	return ""
}
