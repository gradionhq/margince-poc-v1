// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package storekit

// The lead identity probes (ADR-0008 keeps leads out of person matching, so
// they carry exact keys of their own: a live email and a LinkedIn profile
// URL). Two write shapes create leads — the direct create and the
// evidence-backed capture — and they answer a hit with deliberately
// different policies: the direct create refuses with the incumbent's id,
// capture stages a merge proposal for a human. What they must NOT hold
// separately is the question itself, which is why the SQL lives here rather
// than once per module: the modules cannot import each other, and a second
// copy of "is this identity already claimed" is how the two drifted apart
// before (a LinkedIn URL was probed by neither).
//
// This is the same seam EmailSuppressed already occupies — a tx-carrying
// predicate that both people and capture call.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// LeadSourceKey names the source record a probe must not report against
// itself. Re-capturing one message is a replay, not a collision, and without
// this the second delivery of the same mail would stage a merge proposal
// pairing a lead with itself.
type LeadSourceKey struct {
	SourceSystem string
	SourceID     string
}

// LiveLeadByEmail reports the live lead holding this address, if any. The
// address is lowercased by the query to match lead_email_norm's storage
// contract, so a caller that has not normalized still compares like for like.
func LiveLeadByEmail(ctx context.Context, tx pgx.Tx, email string, exclude *LeadSourceKey) (ids.LeadID, bool, error) {
	if email == "" {
		return ids.LeadID{}, false, nil
	}
	return liveLeadBy(ctx, tx, `email = lower($1)`, email, exclude, "email")
}

// LiveLeadByLinkedInURL reports the live lead holding this profile URL. The
// caller normalizes first (people.NormalizeLinkedInURL) — a profile URL has
// no SQL-side normalization to fall back on, so an unnormalized argument
// finds nothing rather than matching loosely.
//
// The earliest row wins when a workspace already carries duplicates from
// before this probe existed: idx_lead_linkedin is deliberately NOT unique
// (merging what is already there is a human decision), so the probe has to
// pick the canonical original deterministically rather than assume one row.
func LiveLeadByLinkedInURL(ctx context.Context, tx pgx.Tx, normalizedURL string, exclude *LeadSourceKey) (ids.LeadID, bool, error) {
	if normalizedURL == "" {
		return ids.LeadID{}, false, nil
	}
	return liveLeadBy(ctx, tx, `linkedin_url = $1`, normalizedURL, exclude, "linkedin_url")
}

// liveLeadBy is the one query shape behind both probes. The predicate is a
// fixed literal chosen by the exported wrappers, never caller text; the value
// is always a bind parameter.
func liveLeadBy(ctx context.Context, tx pgx.Tx, predicate, value string, exclude *LeadSourceKey, lane string) (ids.LeadID, bool, error) {
	var excludeSystem, excludeID *string
	if exclude != nil {
		excludeSystem, excludeID = &exclude.SourceSystem, &exclude.SourceID
	}
	var id ids.LeadID
	err := tx.QueryRow(ctx, `
		SELECT id FROM lead
		 WHERE `+predicate+` AND archived_at IS NULL
		   AND ($2::text IS NULL
		        OR source_system IS DISTINCT FROM $2
		        OR source_id IS DISTINCT FROM $3)
		 ORDER BY created_at, id
		 LIMIT 1`, value, excludeSystem, excludeID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ids.LeadID{}, false, nil
	}
	if err != nil {
		return ids.LeadID{}, false, fmt.Errorf("probe lead %s dedupe: %w", lane, err)
	}
	return id, true, nil
}
