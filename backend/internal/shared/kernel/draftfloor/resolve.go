// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package draftfloor

import (
	"context"
	"log/slog"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/convstate"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/textlang"
)

// Sender answers who is writing. It is a READ of the acting principal's own
// identity and writes nothing, which is what lets a drafting service that
// guarantees zero writes depend on it.
//
// A principal with no human behind it — the system, a connector acting on
// nobody's authority — answers with two empty strings and no error. That is not
// a failure: DRAFT-AC-E-6 makes an unsigned draft the specified answer there.
type Sender interface {
	ActorIdentity(ctx context.Context) (name, email string, err error)
}

// Clock is the current time, injected rather than read, per the house pattern.
type Clock func() time.Time

// Resolver assembles the envelope every drafting surface is handed.
//
// It is one type rather than a method on each service because the fallbacks are
// the interesting part and they must not differ by surface: a language that
// will not resolve becomes the default, and a sender that will not resolve
// becomes nobody. Two copies of that would eventually disagree about which
// failure is fatal, and the drafting screen would break on one surface only.
type Resolver struct {
	sender Sender
	now    Clock
	logger *slog.Logger
}

// NewResolver builds a resolver on the real clock, with no sender bound.
func NewResolver() *Resolver { return &Resolver{now: time.Now} }

// WithSender binds the identity lookup. Without one, every draft is unsigned.
func (r *Resolver) WithSender(sender Sender) *Resolver {
	r.sender = sender
	return r
}

// WithClock replaces the clock, so a test states the date its case is about.
func (r *Resolver) WithClock(now Clock) *Resolver {
	if now != nil {
		r.now = now
	}
	return r
}

// WithLogger binds the logger the degrade paths report through.
func (r *Resolver) WithLogger(logger *slog.Logger) *Resolver {
	r.logger = logger
	return r
}

// Now is the resolver's clock, for a caller that needs the same instant the
// envelope was stamped with.
func (r *Resolver) Now() time.Time {
	if r == nil || r.now == nil {
		return time.Now()
	}
	return r.now()
}

// Resolve assembles the envelope from the correspondence's own text and where
// it stands.
//
// Neither half can fail the draft. An unresolvable language falls back to the
// default (DRAFT-AC-E-2), and an unresolvable sender leaves the draft unsigned
// (DRAFT-AC-E-6) — a drafting screen that errors because it could not work out
// a greeting is worse than a draft the rep edits.
func (r *Resolver) Resolve(ctx context.Context, correspondence string, state convstate.State) Envelope {
	now := r.Now()
	name, email := r.actor(ctx)
	// The register is read from the WHOLE correspondence, quoted history
	// included: which register two people are on is a property of the
	// relationship, and a single reply may contain neither form while the
	// exchange behind it is unmistakably du.
	return NewEnvelopeWithRegister(textlang.Detect(correspondence),
		textlang.DetectRegister(correspondence), state, now, name, email)
}

// actor names the acting human, or nobody.
//
// A lookup failure is reported and then degraded past, not swallowed: the
// caller gets an unsigned draft, and the reason it happened is in the log
// rather than only in the shape of the output.
func (r *Resolver) actor(ctx context.Context) (name, email string) {
	if r == nil || r.sender == nil {
		return "", ""
	}
	name, email, err := r.sender.ActorIdentity(ctx)
	if err != nil {
		r.log().WarnContext(ctx, "draft sender identity unavailable; drafting unsigned", "err", err)
		return "", ""
	}
	return name, email
}

// log is the resolver's logger, defaulting rather than being required at
// construction so a caller that only wants a draft is not made to supply one.
func (r *Resolver) log() *slog.Logger {
	if r == nil || r.logger == nil {
		return slog.Default()
	}
	return r.logger
}
