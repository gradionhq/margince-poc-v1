// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// An account-started send names its own addressees, which a reply never
// does — a reply inherits the conversation it answers. So this is the one
// outbound path where a caller can type an address that belongs to nobody
// on file, and ADR-0087 §2 refuses that rather than accepting it.
//
// The refusal is a FIELD fault naming the address, not a generic validation
// error, because the composer's job at that moment is to offer the fix:
// attach this address to a contact, or pick a different recipient. A caller
// who cannot see which address was the problem cannot act on either.

import (
	"context"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// RecipientDirectory answers whether an address belongs to a person the
// caller may read. The people capability implements it and compose injects
// it, because a module never imports a sibling.
//
// It answers about VISIBILITY, not existence: an address on a person the
// caller's row scope excludes is answered the same as one on nobody at all.
// Telling the two apart would leak the existence of a record through a
// composer field, which is the disclosure the row-scope gate exists to
// prevent everywhere else.
type RecipientDirectory interface {
	// VisibleAddresses returns the subset of addresses that belong to a
	// person this caller can read, normalized lowercase.
	VisibleAddresses(ctx context.Context, tx pgx.Tx, addresses []string) (map[string]bool, error)
}

// WithRecipientDirectory returns a store whose account-started sends resolve
// their addressees. A store without one refuses an account-started send
// rather than mailing an address nobody is accountable for.
func (s *Store) WithRecipientDirectory(dir RecipientDirectory) *Store {
	clone := *s
	clone.recipients = dir
	return &clone
}

// UnresolvedRecipientError refuses an account-started send that names an
// address belonging to no person the sender can see.
//
// It carries ONE address even when several failed, because the composer
// fixes them one at a time and the first unresolved address is the one the
// rep is looking at. The count travels beside it so the message can say
// there are more without listing addresses the caller may not be entitled
// to reason about.
type UnresolvedRecipientError struct {
	Address string
	More    int
}

func (e *UnresolvedRecipientError) Error() string {
	msg := "no contact you can see has the address " + e.Address +
		" — add it to a person's record, or pick a different recipient"
	switch {
	case e.More == 1:
		msg += " (and 1 other address like it)"
	case e.More > 1:
		msg += " (and " + strconv.Itoa(e.More) + " other addresses like it)"
	}
	return msg
}

// FieldFault names the address the caller must correct.
func (e *UnresolvedRecipientError) FieldFault() (field, code, message string) {
	return "to", "recipient_not_on_file", e.Error()
}

// NoRecipientDirectoryError refuses an account-started send on a surface
// wired without address resolution. Like errNoDeliveryStager it is a
// composition defect rather than a client-correctable condition, so it
// carries no FieldFault and must surface as the 500 it is — a caller told
// to fix their address list would be told something untrue.
type NoRecipientDirectoryError struct{}

func (e *NoRecipientDirectoryError) Error() string {
	return "activities: account-started send has no recipient directory wired"
}

// resolveRecipients refuses before anything is staged when an addressee
// belongs to nobody the sender can read.
//
// A REPLY skips this entirely, and that is the decision rather than an
// oversight: its addressees come from a conversation the workspace already
// captured, so an address on it is evidence of correspondence that happened
// whether or not anyone ever filed a contact for it. Refusing there would
// block answering mail the product itself put on the timeline.
func (s *Store) resolveRecipients(ctx context.Context, tx pgx.Tx, origin SendOrigin, recipients []string) error {
	if origin.isReply() {
		return nil
	}
	if s.recipients == nil {
		return &NoRecipientDirectoryError{}
	}
	visible, err := s.recipients.VisibleAddresses(ctx, tx, recipients)
	if err != nil {
		return err
	}
	var first string
	unresolved := 0
	for _, addr := range recipients {
		if normalized := strings.ToLower(strings.TrimSpace(addr)); !visible[normalized] {
			if first == "" {
				first = addr
			}
			unresolved++
		}
	}
	if unresolved > 0 {
		return &UnresolvedRecipientError{Address: first, More: unresolved - 1}
	}
	return nil
}
