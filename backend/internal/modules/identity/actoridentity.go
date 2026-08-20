// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// Naming the caller to themselves.
//
// Separate from SeatNames, which names OTHER people in the workspace, because
// the two answer different questions and carry different reasoning. SeatNames
// is a directory read about colleagues. This one answers "who am I", and the
// answer is exactly the identity the caller already authenticated as.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ActorIdentity names the human behind this call, for a draft to be written as.
//
// A draft has to know who is writing it. Without that the model works the
// sender out of quoted headers, which is how a reply ends up written as the
// wrong party — the defect this exists to fix (DRAFT-AC-E-6).
//
// It resolves UserID first and OnBehalfOf second, and the order matters. An
// agent drafting through the governed tool surface is a non-human principal
// acting under a specific human's authority; OnBehalfOf is that human, and
// returning nothing there would blank the sender on exactly the path that
// inherits every drafting fix. A system principal with no human behind it
// resolves to nothing, and drafting degrades to an unsigned draft rather than
// failing.
//
// It carries NO object gate, and that is a considered position rather than an
// omission. There is no object to grant on: the question is "who is the caller"
// and the answer is the caller's own row, which they already presented
// credentials for. The workspace binding is the transaction's, so a seat in
// another tenant cannot be named however the principal was built. This is the
// same reasoning that waives SeatNames, one step narrower — that one names
// colleagues, this one names only the asker.
func (s *Service) ActorIdentity(ctx context.Context) (name, email string, err error) {
	human := actingHuman(ctx)
	if human.IsZero() {
		// The system principal, or a connector acting on nobody's authority.
		// Not an error: an unsigned draft is the specified answer here.
		return "", "", nil
	}

	err = s.db.Tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT display_name, email FROM app_user WHERE id = $1 AND archived_at IS NULL`,
			human).Scan(&name, &email)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// A live principal whose seat this workspace does not hold, or holds
		// archived. Nothing to sign with, and nothing the caller can do about
		// it, so it reads the same as no human behind the call.
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("identity: read acting seat: %w", err)
	}
	return name, email, nil
}

// actingHuman is which human a call is written as: the caller themselves, or
// the human whose authority an agent or connector is acting under.
//
// Separate from the read above so the decision can be exercised without a
// database — it is the half that has an order to get right, and the query is
// the half that only looks a row up.
func actingHuman(ctx context.Context) ids.UUID {
	actor, ok := principal.Actor(ctx)
	if !ok {
		return ids.Nil
	}
	if !actor.UserID.IsZero() {
		return actor.UserID
	}
	return actor.OnBehalfOf
}

// ActorProfile is ActorIdentity's fuller answer: who the call acts for, for a
// surface that must NAME them rather than sign as them.
//
// Same resolution and same posture — no object gate, because the question is
// "who is the caller" and a caller may always know that. It answers a zero
// profile rather than an error when nobody is behind the call (a system
// principal, or a seat this workspace no longer holds), which is what
// ActorIdentity does for the same cases.
func (s *Service) ActorProfile(ctx context.Context) (ActorProfile, error) {
	human := actingHuman(ctx)
	if human.IsZero() {
		return ActorProfile{}, nil
	}
	var out ActorProfile
	var locale, timezone *string
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT id, display_name, email, locale, timezone
			   FROM app_user WHERE id = $1 AND archived_at IS NULL`,
			human).Scan(&out.UserID, &out.DisplayName, &out.Email, &locale, &timezone)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ActorProfile{}, nil
	}
	if err != nil {
		return ActorProfile{}, fmt.Errorf("identity: read acting profile: %w", err)
	}
	// NULL locale stays empty: a person who never chose a language is not the
	// same as one who chose English, and only the caller can decide what to
	// fall back to.
	if locale != nil {
		out.Locale = *locale
	}
	if timezone != nil {
		out.Timezone = *timezone
	}
	return out, nil
}

// ActorProfile is who a call acts for. Every field is empty when nobody is.
type ActorProfile struct {
	UserID      ids.UUID
	DisplayName string
	Email       string
	Locale      string
	Timezone    string
}
