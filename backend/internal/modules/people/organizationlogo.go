// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The organization's visual identity (A55): the row holds a reference to the
// normalized bytes in object storage plus the page URL they were resolved
// from, never the bytes themselves. A logo is a DISPLAY asset, so resolving
// one is a 🟢 write that needs no confirm — but it obeys the same
// human-precedence rule every enriched field does: a mark a person uploaded
// is never replaced by one a machine found, and a resolve that meets one
// leaves it alone rather than staging a change nobody asked for.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// logoFieldName is how the logo is spelled in field_provenance and in the
// audit/event delta. One spelling, so the provenance display and the write
// cannot disagree about which field was set.
const logoFieldName = "logo"

// SetOrganizationLogo records a resolved company mark: the storage key its
// normalized bytes live at, and the asset URL it came from. It reports whether
// the row was written — false means a human's own logo holds the field, which
// is a normal outcome and not an error.
//
// The bytes must already be stored: this store is blob-free (the same division
// the offer PDF's asset ref keeps), so a caller writes the object first and the
// reference second. That order also means a crash between the two leaves an
// unreferenced object at a key derived from the organization id — the next
// resolve overwrites it, and erasure deletes it, because both derive the key
// rather than reading it back.
func (s *Store) SetOrganizationLogo(ctx context.Context, id ids.OrganizationID, objectKey, originURL string) (bool, error) {
	if err := auth.Require(ctx, "organization", principal.ActionUpdate); err != nil {
		return false, err
	}
	if objectKey == "" || originURL == "" {
		return false, errors.New("people: a resolved logo needs both its storage key and the URL it was resolved from")
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return false, err
	}
	written := false
	err = s.tx(ctx, func(tx pgx.Tx) error {
		// The target is a KNOWN row, so row-scope is re-checked here: a leaked
		// organization id buys nothing (existence-hiding 404).
		if err := auth.EnsureVisible(ctx, tx, "organization", id.UUID); err != nil {
			return err
		}
		held, err := logoHeldByHuman(ctx, tx, id)
		if err != nil {
			return err
		}
		if held {
			return nil
		}
		tag, err := tx.Exec(ctx, `
			UPDATE organization SET logo_object_key = $2, logo_origin = $3
			WHERE id = $1 AND archived_at IS NULL`,
			id, objectKey, originURL)
		if err != nil {
			return fmt.Errorf("set organization logo: %w", err)
		}
		if tag.RowsAffected() == 0 {
			// Visible above but not updatable here: the row was archived
			// between the two statements. Nothing to record.
			return apperrors.ErrNotFound
		}
		written = true
		if err := storekit.StampFields(ctx, tx, "organization", id.UUID, companySourceSiteRead, by,
			[]storekit.FieldStamp{{Field: logoFieldName, EvidenceRef: &originURL}}); err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "update", "organization", id.UUID, nil, map[string]any{
			auditKeySource: companySourceSiteRead, auditKeySourceURL: originURL,
			auditKeyFields: map[string]any{logoFieldName: originURL},
		})
		if err != nil {
			return fmt.Errorf("audit organization logo: %w", err)
		}
		if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, crmcontracts.PublicEventOrganizationUpdated{
			ChangedFields: map[string]any{
				eventKeyDelta:  map[string]any{auditKeyFields: map[string]any{logoFieldName: originURL}},
				auditKeySource: companySourceSiteRead, auditKeySourceURL: originURL,
			},
		}); err != nil {
			return fmt.Errorf("emit organization.updated: %w", err)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return written, nil
}

// logoHeldByHuman reports whether a person set this organization's logo. It
// reads the same field_provenance layer the provenance display reads, so
// "a human owns this field" has one answer in the product, not two.
func logoHeldByHuman(ctx context.Context, tx pgx.Tx, id ids.OrganizationID) (bool, error) {
	var human bool
	err := tx.QueryRow(ctx, `
		SELECT captured_by LIKE 'human:%'
		FROM field_provenance
		WHERE object_type = 'organization' AND object_id = $1 AND field_name = $2
		ORDER BY captured_at DESC, id DESC
		LIMIT 1`, id, logoFieldName).Scan(&human)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // no logo provenance yet — nobody holds the field
	}
	if err != nil {
		return false, fmt.Errorf("read organization logo provenance: %w", err)
	}
	return human, nil
}

// OrganizationLogoKey answers where one organization's logo bytes live, for a
// caller that streams them. It returns ErrNotFound both when the organization
// is invisible or absent and when it simply has no logo: to the client those
// are the same answer — draw the monogram — and distinguishing them would leak
// which organizations exist.
func (s *Store) OrganizationLogoKey(ctx context.Context, id ids.OrganizationID) (string, error) {
	// A logo is part of the record, so reading its location is a read of the
	// record and carries the record's gate.
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		return "", err
	}
	var key *string
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "organization", id.UUID); err != nil {
			return err
		}
		return tx.QueryRow(ctx,
			`SELECT logo_object_key FROM organization WHERE id = $1 AND archived_at IS NULL`, id).Scan(&key)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", apperrors.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if key == nil || *key == "" {
		return "", apperrors.ErrNotFound
	}
	return *key, nil
}

// LogoURL renders where a client fetches an organization's logo bytes, or nil
// when the organization has no logo stored. The storage key never reaches the
// wire: it names a bucket path, and a client's business is the endpoint that
// streams the object.
//
// Exported because the account-graph assembly reads organization rows of its
// own and must spell this URL exactly as this module's own reads do — one
// spelling, or a company's face differs between its record and the graph.
func LogoURL(id ids.UUID, objectKey *string) *string {
	if objectKey == nil || *objectKey == "" {
		return nil
	}
	path := "/v1/organizations/" + id.String() + "/logo"
	return &path
}
