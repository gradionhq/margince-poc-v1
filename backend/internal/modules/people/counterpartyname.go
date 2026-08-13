// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Naming a counterparty from what the INSTALLATION knows, when the header does
// not say enough on its own.
//
// personname.go reads one message's header and is deliberately willing to
// abstain: a display name of "Lars" is one token, and one token is a first
// name, a surname or a nickname with nothing to say which. That is the right
// answer for a stranger and the wrong one for somebody the workspace already
// has on file — mail from lars@jankowfsky.de is not a mystery when there is an
// app_user row with that address and a full name a person typed.
//
// So this file is the second question, asked only when the first abstains:
// does this installation already know who this is.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// KnownHumanName is what the installation already knows a mail address belongs
// to — the display name on the app_user row with that address, or "" when the
// address is nobody the installation knows.
//
// It exists because the header is not always the best evidence available. A
// message signed only "Lars" from lars@jankowfsky.de parses to a single token,
// which the parser refuses to split — correctly, since one word is a first
// name, a surname or a nickname and the header does not say which. But when
// that address is a HUMAN THIS INSTALLATION HAS, the answer is not a guess: the
// workspace already holds their full name, typed by a person.
func KnownHumanName(ctx context.Context, tx pgx.Tx, email string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" {
		return "", nil
	}
	var name string
	err := tx.QueryRow(ctx, `
		SELECT display_name FROM app_user
		 WHERE lower(email) = $1 AND archived_at IS NULL`, normalized).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("people: reading the human behind %s: %w", normalized, err)
	}
	return strings.TrimSpace(name), nil
}

// nameCounterparty reads the best name available for a captured address.
//
// The header is the usual evidence and the parser reads it well, but it is not
// always the BEST evidence the installation holds. A message signed only "Lars"
// parses to one token, which the parser refuses to split — one word is a first
// name, a surname or a nickname, and the header does not say which. When that
// same address belongs to a human this installation already has, the workspace
// holds their full name, typed by a person: better evidence than a header, and
// not a guess.
//
// It only ever fills a blank. A CONFIDENT header parse stands, because the
// header is what that person calls themselves in mail, and a stale app_user
// display name must not overwrite it.
func (s *Store) nameCounterparty(ctx context.Context, tx pgx.Tx, in EnsureCounterpartyInput) (ParsedName, error) {
	parsed := ParsePersonName(in.DisplayName, in.Email)
	if parsed.Confident {
		return parsed, nil
	}
	known, err := KnownHumanName(ctx, tx, in.Email)
	if err != nil {
		return ParsedName{}, err
	}
	if known == "" {
		return parsed, nil
	}
	// Re-parsed rather than stored raw: an app_user display name is typed by a
	// human and can carry the same shapes a header does — "Jankowfsky, Lars",
	// a trailing company — and it deserves the same reading.
	fromUser := ParsePersonName(known, in.Email)
	if fromUser.Full == "" {
		return parsed, nil
	}
	return fromUser, nil
}
