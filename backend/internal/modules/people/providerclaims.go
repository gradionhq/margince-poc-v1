// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// person_provider_claim: what a licensed data provider asserted about one of
// our people. People owns the table because the domain decides what a claim
// MEANS and how it renders; integrations owns the run that bought it.
//
// Claims are kept BESIDE the canonical record, never folded into it. A
// purchased email does not become person_email, and a purchased title does not
// overwrite one a human typed — the person page shows both and says which is
// which. That is the whole reason this is a separate table rather than a
// writer into the ones people already owns.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/ports/provider"
)

// WriteProviderClaims upserts one run's claims, inside the transaction
// integrations opened for the hand-off. Idempotent by UNIQUE(run_id,
// claim_key): a recovery that re-reads the same result and writes it again
// produces the same rows, which is what lets the recovery ladder retry
// without a duplicate-detection pass of its own.
//
// The fence has already run in this transaction. It is not re-checked here:
// two answers to the same question in one transaction is a second place for
// them to disagree.
func WriteProviderClaims(ctx context.Context, tx pgx.Tx, runID, personID, providerName string, claims []provider.Claim, retrievedAt time.Time) error {
	for _, c := range claims {
		confidence := c.Confidence
		if _, err := tx.Exec(ctx, `
			INSERT INTO person_provider_claim
			       (person_id, run_id, provider, claim_key, value_json, confidence,
			        source, captured_by, retrieved_at)
			VALUES ($1, $2, $3, $4, $5, $6, $3, 'connector:' || $3, $7)
			ON CONFLICT (run_id, claim_key) DO UPDATE
			   SET value_json = EXCLUDED.value_json,
			       confidence = EXCLUDED.confidence,
			       retrieved_at = EXCLUDED.retrieved_at`,
			personID, runID, providerName, string(c.Key), []byte(c.Value), confidence, retrievedAt); err != nil {
			return fmt.Errorf("people: writing the %s claim: %w", c.Key, err)
		}
	}
	return nil
}

// DeleteProviderClaims removes everything one provider asserted about anyone,
// inside a transaction integrations already holds. It is the domain half of
// the delete-data action: integrations scrubs its run ledger, people deletes
// the values, and neither writes the other's table.
func DeleteProviderClaims(ctx context.Context, tx pgx.Tx, providerName string) (int64, error) {
	tag, err := tx.Exec(ctx, `DELETE FROM person_provider_claim WHERE provider = $1`, providerName)
	if err != nil {
		return 0, fmt.Errorf("people: deleting the provider's claims: %w", err)
	}
	return tag.RowsAffected(), nil
}
