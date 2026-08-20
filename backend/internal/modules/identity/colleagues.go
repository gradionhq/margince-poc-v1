// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The colleague roster: who works here, as opposed to who we sell to.
//
// SeatNames beside this answers "what is this id called" for ids a caller
// already holds. This answers the question that comes first — WHICH id — and
// nothing could ask it before: app_user appeared nowhere on the tool surface,
// so an assistant asked to assign work searched `person`, found a customer
// contact with a similar name, and offered that. The distinction between a
// colleague and a contact was missing as a concept, not merely as a lookup.

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// colleagueCap bounds one answer. A roster is tens of people, not thousands,
// and a caller wanting a particular one narrows with q rather than paging —
// so this is a ceiling that says "narrow it", never a page boundary a reader
// has to walk.
const colleagueCap = 200

// Colleague is one seat: who they are, and whether they can still be given
// work. Nothing about permissions — what a colleague may do is a question for
// the operation they are named on, not for a roster.
type Colleague struct {
	UserID      ids.UUID
	DisplayName string
	Email       string
	SeatType    string
	// Active is false for a suspended or locked-out seat, which is the whole
	// reason status is read: assigning work to a seat nobody can sign into is
	// a task that will never be seen.
	Active bool
	// IsAgent marks a machine seat (the workspace's own agent account). Named
	// rather than filtered out, because an assistant listing colleagues should
	// not silently pretend the agent seat does not exist — and must not offer
	// it as a person to give work to either.
	IsAgent bool
}

// Colleagues lists the workspace's seats, newest-relevant first by name, with
// an optional case-insensitive filter over display name and email.
//
// Archived seats are absent: a person who has left is not a colleague, and
// naming one would offer work to an account that cannot receive it.
func (s *Service) Colleagues(ctx context.Context, q string) ([]Colleague, error) {
	var out []Colleague
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, display_name, email, seat_type, status, is_agent
			  FROM app_user
			 WHERE archived_at IS NULL
			   AND ($1 = '' OR display_name ILIKE '%' || $1 || '%' OR email ILIKE '%' || $1 || '%')
			 ORDER BY display_name
			 LIMIT $2`, strings.TrimSpace(q), colleagueCap)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c Colleague
			var status string
			if err := rows.Scan(&c.UserID, &c.DisplayName, &c.Email, &c.SeatType, &status, &c.IsAgent); err != nil {
				return err
			}
			c.Active = status == "active"
			out = append(out, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("identity: listing colleagues: %w", err)
	}
	return out, nil
}
