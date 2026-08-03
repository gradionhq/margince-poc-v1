// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The organization COLUMN images an accepted cold-start read-back diffs
// against. They live apart from the apply that uses them because field
// history is their whole reason: the write path reports only THAT a column
// changed, so what it WAS has to be read and carried, and the rules for
// reading it — the row lock, the field-not-column keying, and the empty
// image a freshly created record diffs against — are one concern.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// readColdStartColumnImages reads the columns an apply may touch, so the audit
// row can carry what each one WAS. The column writes report only whether they
// changed something, never what they replaced, so the before image has to be
// read: field history projects per field from before/after, and an image-less
// audit row gives it nothing to diff.
func readColdStartColumnImages(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID) (map[string]any, error) {
	var displayName string
	var legalName, industry, addressLine1 *string
	// FOR UPDATE, and it is the audit that needs it: the before image and the
	// apply that follows must describe ONE transaction's work. Read unlocked,
	// a concurrent update landing between them lands inside this diff, and
	// field history attributes somebody else's edit to this acceptance.
	//
	// CALLER OBLIGATION: hold lockOrgNameWrites before calling this. Every
	// writer of an organization name takes that workspace-wide lock first and
	// the row lock second (see its own doc); a caller that reaches this row
	// lock without it inverts the pair and deadlocks against a human rename.
	if err := tx.QueryRow(ctx,
		`SELECT display_name, legal_name, industry, address_line1
		   FROM organization WHERE id = $1 FOR UPDATE`,
		orgID).Scan(&displayName, &legalName, &industry, &addressLine1); err != nil {
		return nil, fmt.Errorf("read organization column images: %w", err)
	}
	// Keyed by FIELD, not by column: field history projects per field, and
	// address_line1 is the column behind registered_address. Filed under the
	// column name, the account's registered address showed up in history as a
	// field the profile surface does not have.
	out := map[string]any{fieldDisplayName: displayName}
	for column, value := range map[string]*string{
		fieldLegalName: legalName, fieldIndustry: industry, fieldRegisteredAddress: addressLine1,
	} {
		if value == nil {
			// An empty column reads as an explicit null in the image, never as
			// an absent key: "it had nothing" and "nobody looked" are different
			// answers, and field history renders them differently.
			out[column] = nil
			continue
		}
		out[column] = *value
	}
	return out, nil
}

// emptyColdStartColumnImages is the before-image of a record that did not
// exist: every field explicitly null, so a create diffs against nothing rather
// than against itself.
func emptyColdStartColumnImages() map[string]any {
	return map[string]any{
		fieldDisplayName:       nil,
		fieldLegalName:         nil,
		fieldIndustry:          nil,
		fieldRegisteredAddress: nil,
	}
}

// changedColumnsOnly narrows the two images to the columns that actually moved.
// A field-history projection reads every key in the pair, so carrying an
// untouched column through would publish "industry: Automotive → Automotive"
// as a change on a run that only filled the legal name.
func changedColumnsOnly(before, after map[string]any) (map[string]any, map[string]any) {
	b, a := map[string]any{}, map[string]any{}
	for column, afterValue := range after {
		if before[column] == afterValue {
			continue
		}
		b[column] = before[column]
		a[column] = afterValue
	}
	return b, a
}
