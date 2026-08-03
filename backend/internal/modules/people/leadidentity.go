// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// A lead's exact identity keys and what a create does when one is already
// claimed. ADR-0008 keeps leads out of person matching, so these keys — a live
// email address and a LinkedIn profile URL — are the whole of lead dedupe, and
// each refusal is the 409 contract with the incumbent's id.
//
// The QUESTION is storekit's (LiveLeadByEmail / LiveLeadByLinkedInURL), shared
// with the captured-lead write shape in the capture module; only the ANSWER
// differs between the two, and it lives here.

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// DuplicateLeadError carries the live lead already holding an email
// (uq_lead_email_dedupe → 409, features/01 §6.2).
type DuplicateLeadError struct {
	Email      string
	ExistingID ids.LeadID
}

func (e *DuplicateLeadError) Error() string { return "lead with email " + e.Email + " already exists" }

// Is maps the claim onto the shared conflict sentinel, so every caller that
// only cares that the create was refused keeps working unchanged.
func (e *DuplicateLeadError) Is(target error) bool { return target == apperrors.ErrConflict }

// DuplicateLeadLinkedInError is the same 409 contract on the LinkedIn key
// (E12.11): one profile URL, one lead.
type DuplicateLeadLinkedInError struct {
	URL        string
	ExistingID ids.LeadID
}

func (e *DuplicateLeadLinkedInError) Error() string {
	return "lead with linkedin url " + e.URL + " already exists"
}

// Is maps the claim onto the shared conflict sentinel, so every caller that
// only cares that the create was refused keeps working unchanged.
func (e *DuplicateLeadLinkedInError) Is(target error) bool { return target == apperrors.ErrConflict }

// ensureLeadEmailUnclaimed answers the live-email dedupe probe with the
// contract's 409, disclosing the existing id only when the caller could
// read that row.
func ensureLeadEmailUnclaimed(ctx context.Context, tx pgx.Tx, email *string) error {
	if email == nil {
		return nil
	}
	existing, found, err := storekit.LiveLeadByEmail(ctx, tx, *email, nil)
	if err != nil || !found {
		return err
	}
	dup := &DuplicateLeadError{Email: *email}
	visible, err := auth.VisibleTo(ctx, tx, "lead", existing.UUID)
	if err != nil {
		return err
	}
	if visible {
		dup.ExistingID = existing
	}
	return dup
}

// lockLeadIdentity takes the write identities a lead create is about to claim,
// BEFORE either probe reads.
//
// The LinkedIn key has no unique index to fall back on — idx_lead_linkedin is
// deliberately non-UNIQUE, because a workspace may already hold duplicates from
// before this refusal existed and merging those is a human decision — so the
// advisory lock is the whole race guard, not a nicety.
//
// It runs before BOTH probes rather than beside its own. Two creates sharing an
// address and a profile would otherwise interleave between the email probe and
// the LinkedIn one, and each would report whichever key it happened to lose —
// the same pair of requests answering `duplicate_email` on one run and
// `duplicate_linkedin_url` on the next.
func lockLeadIdentity(ctx context.Context, tx pgx.Tx, url *string) error {
	if url == nil {
		return nil
	}
	return storekit.LockWriteIdentity(ctx, tx, "lead_linkedin", *url)
}

// ensureLeadLinkedInUnclaimed is the same refusal on the OTHER exact key a
// lead carries. A profile URL names one human as firmly as an address does,
// and the probe that answers it existed while nothing called it — so two
// imports of the same person under different addresses each minted a lead.
func ensureLeadLinkedInUnclaimed(ctx context.Context, tx pgx.Tx, url *string) error {
	if url == nil {
		return nil
	}
	existing, found, err := storekit.LiveLeadByLinkedInURL(ctx, tx, *url, nil)
	if err != nil || !found {
		return err
	}
	dup := &DuplicateLeadLinkedInError{URL: *url}
	visible, err := auth.VisibleTo(ctx, tx, "lead", existing.UUID)
	if err != nil {
		return err
	}
	if visible {
		dup.ExistingID = existing
	}
	return dup
}
