// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The shared webhook chassis provider callbacks sit on: one route shape, one
// admission sequence — method guard,
// constant-time secret comparison, an optional second verifier, a bounded
// body read — and one response discipline. What differs between providers
// (secret granularity, payload durability, routing key) stays in each
// caller's WebhookSpec; the chassis enforces only the half that is
// genuinely shared, per the design's table of what cannot be.
//
// The response discipline is the reason Disposition exists: a malformed
// payload arrived and failed on our terms, so redelivering the same bytes
// forever would never help — that answers 2xx, same as success, so the
// provider stops retrying. A transient fault (a database blip, a queue
// momentarily down) is the opposite: redelivery is exactly the recovery
// mechanism, so that answers 500. A bare error cannot carry this
// difference, which is why Handle reports a Disposition alongside it.

package compose

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"io"
	"log/slog"
	"net/http"
)

// Disposition is why a webhook handler stopped, not merely whether it
// failed — the status a provider sees follows from the reason, not from a
// bare success/failure bit.
type Disposition int

const (
	// Accepted means the delivery was understood and acted on; the
	// caller's OnAccept status closes it out (Gmail answers 204).
	Accepted Disposition = iota
	// Poison means the delivery arrived but could not be understood or
	// acted on, and the fault is ours to inspect, not the provider's to
	// retry — redelivering the same bytes cannot change the outcome, so
	// this still answers 2xx.
	Poison
	// Transient means we failed to learn or act for a reason redelivery
	// can fix — a database blip, a queue momentarily down — so this
	// answers 500 and asks the provider to try again.
	Transient
)

// WebhookSpec is what a provider declares to sit on the chassis; the
// chassis owns admission and response discipline, Handle owns everything
// provider-specific (payload shape, durability, routing).
type WebhookSpec struct {
	// Provider names the webhook in logs (for example, "gmail").
	Provider string
	// MaxBody bounds the request body the chassis will read, so an
	// unauthenticated public endpoint cannot be used to exhaust memory.
	MaxBody int64
	// Secret returns the expected and presented shared secret for this
	// request (for example, an operator token in Gmail's query string) —
	// compared in constant time regardless of which shape it came from.
	Secret func(*http.Request) (want, got string)
	// Verify is an optional second admission factor layered on top of the
	// secret (Gmail's Google-signed OIDC bearer); nil skips it — the
	// chassis composes it rather than flattening it into the secret check.
	Verify func(context.Context, *http.Request) error
	// Handle acts on a body that has cleared admission and reports why it
	// stopped.
	Handle func(ctx context.Context, r *http.Request, body []byte) (Disposition, error)
	// OnAccept is the status written for Accepted — the one thing the
	// design allows to differ between providers on the success path.
	OnAccept int
}

// Webhook builds the shared chassis: method guard, constant-time secret
// comparison, the optional second verifier, a bounded body read, then
// Handle — with the response discipline documented on Disposition applied
// uniformly regardless of provider. Correlate/AccessLog wrap the result at
// the mount point (routes.go), not here — this handler is the webhook
// itself.
func Webhook(spec WebhookSpec, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		// Digesting both values before comparing equalizes their length, so
		// ConstantTimeCompare leaks neither the secret's content nor its
		// length: a wrong guess of any size gets the identical 401, with an
		// empty body naming no connection ids for an attacker to learn from.
		want, got := spec.Secret(r)
		wantSum := sha256.Sum256([]byte(want))
		gotSum := sha256.Sum256([]byte(got))
		if subtle.ConstantTimeCompare(wantSum[:], gotSum[:]) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		if spec.Verify != nil {
			if err := spec.Verify(r.Context(), r); err != nil {
				log.WarnContext(r.Context(), "webhook: second-factor verification failed",
					"provider", spec.Provider, "err", err)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}

		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, spec.MaxBody))
		if err != nil {
			// Oversized or truncated is ours to refuse, not the provider's
			// to retry: the same bytes would exceed the bound again on
			// redelivery, so this follows the poison-payload discipline.
			log.WarnContext(r.Context(), "webhook: body exceeded the bound or could not be read",
				"provider", spec.Provider, "err", err)
			w.WriteHeader(http.StatusOK)
			return
		}

		disposition, handleErr := spec.Handle(r.Context(), r, body)
		switch disposition {
		case Accepted:
			w.WriteHeader(spec.OnAccept)
		case Poison:
			if handleErr != nil {
				log.WarnContext(r.Context(), "webhook: poison payload",
					"provider", spec.Provider, "err", handleErr)
			}
			w.WriteHeader(http.StatusOK)
		case Transient:
			log.ErrorContext(r.Context(), "webhook: transient fault",
				"provider", spec.Provider, "err", handleErr)
			w.WriteHeader(http.StatusInternalServerError)
		default:
			// A Handle that returns neither of the three declared values is
			// itself a bug in the caller, not a fact about this delivery —
			// fail the way an unrecoverable server error should.
			log.ErrorContext(r.Context(), "webhook: handler returned an unrecognized disposition",
				"provider", spec.Provider, "disposition", int(disposition))
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
}
