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
// is a normal outcome and not an error — and hands back the key the row named
// BEFORE this write, so the caller can reclaim bytes nothing references any
// more.
//
// The bytes must already be stored: this store is blob-free, the same division
// the offer PDF's asset ref keeps, so a caller writes the object first and the
// reference second.
//
// The key a caller passes must be unique to its own attempt, and the returned
// one is how the superseded object gets collected. A key derived from the
// organization alone would make two concurrent resolves write the SAME object:
// each would overwrite the other's bytes while the row recorded whichever
// transaction committed last, leaving the stored image and the origin URL
// describing different pictures.
func (s *Store) SetOrganizationLogo(ctx context.Context, id ids.OrganizationID, objectKey, originURL string) (written bool, supersededKey *string, err error) {
	if err := auth.Require(ctx, "organization", principal.ActionUpdate); err != nil {
		return false, nil, err
	}
	if objectKey == "" || originURL == "" {
		return false, nil, errors.New("people: a resolved logo needs both its storage key and the URL it was resolved from")
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return false, nil, err
	}
	err = s.tx(ctx, func(tx pgx.Tx) error {
		// The target is a KNOWN row, so row-scope is re-checked here: a leaked
		// organization id buys nothing (existence-hiding 404).
		if err := auth.EnsureVisible(ctx, tx, "organization", id.UUID); err != nil {
			return err
		}
		// Lock the row before reading who holds the field. The guard is a read
		// followed by a write, so without the lock a person's upload landing
		// between the two would be read as absent and then overwritten — the
		// precedence rule would hold on every run except the one where it
		// matters. Any writer of this organization takes the same lock, so the
		// two serialize instead of racing.
		// Live rows only, matching every other mutation and OrganizationLogoKey:
		// a lock that admitted tombstones would let an archived organization
		// reach the human-precedence return and answer "no change" where the
		// rest of the module answers not-found.
		var locked ids.UUID
		err := tx.QueryRow(ctx,
			`SELECT id FROM organization WHERE id = $1 AND archived_at IS NULL FOR UPDATE`, id).Scan(&locked)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock organization for the logo write: %w", err)
		}
		held, err := logoHeldByHuman(ctx, tx, id)
		if err != nil {
			return err
		}
		if held {
			return nil
		}
		// RETURNING the pre-write key: this transaction holds the row lock, so
		// it is the one place that can hand back the object its own write
		// supersedes. Reading it separately afterwards would name whatever the
		// NEXT resolve had since put there.
		var previous *string
		err = tx.QueryRow(ctx, `
			UPDATE organization SET logo_object_key = $2, logo_origin = $3
			WHERE id = $1 AND archived_at IS NULL
			RETURNING (SELECT o.logo_object_key FROM organization o WHERE o.id = $1)`,
			id, objectKey, originURL).Scan(&previous)
		if errors.Is(err, pgx.ErrNoRows) {
			// Visible above but not updatable here: the row was archived
			// between the two statements. Nothing to record.
			return apperrors.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("set organization logo: %w", err)
		}
		written = true
		supersededKey = supersededObject(previous, objectKey)
		return recordLogoWrite(ctx, tx, id, originURL, by)
	})
	if err != nil {
		return false, nil, err
	}
	return written, supersededKey, nil
}

// RecordSiteReadLogo parks a resolved mark on the dossier that found it, for a
// read whose subject does not exist yet. An onboarding read is unbound by
// construction — it reads the site to propose a company a human has not
// confirmed into being — and the seed page's declarations are in hand only
// while the crawl is, so a mark that is not parked here is a mark nothing can
// resolve later.
//
// It reports whether the dossier took the reference — a read already bound or
// confirmed has moved on without it — and hands back the key the row named
// before, so the caller can reclaim bytes nothing references any more. Same
// contract as SetOrganizationLogo, for the same reason: each attempt writes its
// own key, so two resolves of one read can never write the same object.
//
// No auth.Require, the same rationale as BeginSiteRead: the worker is not a
// human principal, the human's authority was checked when the read was started,
// and the workspace-bound transaction still scopes the write to the job's
// tenant. The reference on the dossier is operational, like every other worker
// write to this row; the audited write on the RECORD happens when a
// confirmation binds it.
func (s *Store) RecordSiteReadLogo(ctx context.Context, readID ids.UUID, objectKey, originURL string) (recorded bool, supersededKey *string, err error) {
	if objectKey == "" || originURL == "" {
		return false, nil, errors.New("people: a resolved logo needs both its storage key and the URL it was resolved from")
	}
	err = s.tx(ctx, func(tx pgx.Tx) error {
		// RETURNING the pre-write key, exactly as SetOrganizationLogo does: the
		// sub-select reads the statement's own snapshot, so it names the object
		// this write supersedes rather than the one it just stored.
		var previous *string
		err := tx.QueryRow(ctx, `
			UPDATE site_read SET logo_object_key = $2, logo_origin = $3, updated_at = now()
			WHERE id = $1 AND organization_id IS NULL AND confirmed_at IS NULL
			RETURNING (SELECT sr.logo_object_key FROM site_read sr WHERE sr.id = $1)`,
			readID, objectKey, originURL).Scan(&previous)
		if errors.Is(err, pgx.ErrNoRows) {
			// The read is bound or confirmed already. Its company was decided
			// without this mark, and overwriting the reference now would name
			// bytes no record will ever adopt.
			return nil
		}
		if err != nil {
			return fmt.Errorf("record the website read's logo: %w", err)
		}
		recorded = true
		supersededKey = supersededObject(previous, objectKey)
		return nil
	})
	if err != nil {
		return false, nil, err
	}
	return recorded, supersededKey, nil
}

// bindSiteReadLogo gives the company a confirmation creates the mark its own
// website read already resolved — the step that makes the anchor's face arrive
// on the same terms as every other company's (A55). It runs inside the
// confirmation's transaction, so the company and its logo commit together.
//
// Fill-empty, like every site-read field the confirmation applies: a mark the
// record already wears — a person's own, or one an earlier read landed — stays,
// and the parked object is simply not adopted. That is also what keeps this
// write from stranding bytes nothing could collect afterwards: this module owns
// no object store, so it may adopt an object but must never drop the last
// reference to one.
func bindSiteReadLogo(ctx context.Context, tx pgx.Tx, readID ids.UUID, orgID ids.OrganizationID) error {
	var objectKey, originURL *string
	if err := tx.QueryRow(ctx,
		`SELECT logo_object_key, logo_origin FROM site_read WHERE id = $1`, readID).
		Scan(&objectKey, &originURL); err != nil {
		return fmt.Errorf("read the website read's logo: %w", err)
	}
	if objectKey == nil || *objectKey == "" || originURL == nil || *originURL == "" {
		// The read resolved nothing usable — an air-gapped install, a site that
		// declares no icon, an asset that would not decode. The record draws its
		// deterministic monogram, which is a face rather than a gap.
		return nil
	}
	held, err := logoHeldByHuman(ctx, tx, orgID)
	if err != nil {
		return err
	}
	if held {
		return nil
	}
	tag, err := tx.Exec(ctx, `
		UPDATE organization SET logo_object_key = $2, logo_origin = $3
		WHERE id = $1 AND archived_at IS NULL AND logo_object_key IS NULL`,
		orgID, *objectKey, *originURL)
	if err != nil {
		return fmt.Errorf("bind the website read's logo: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	// The site read is what captured this, never the human who confirmed the
	// draft: provenance is written once and never re-derived, and a machine mark
	// recorded under a person's name would make the human-precedence guard
	// refuse every later resolve for a logo nobody chose.
	return recordLogoWrite(ctx, tx, orgID, *originURL, companySiteReadCapturedBy)
}

// supersededObject names the object this write orphaned, or nil when it
// orphaned none — the organization had no logo, or the caller re-recorded the
// key already on the row.
func supersededObject(previous *string, objectKey string) *string {
	if previous == nil || *previous == "" || *previous == objectKey {
		return nil
	}
	return previous
}

// recordLogoWrite completes the logo write's shape: the field's provenance,
// the audit row, and the organization.updated event that links both into the
// trace. It runs inside the caller's transaction, so the mark and the record
// of who set it commit together or not at all.
func recordLogoWrite(ctx context.Context, tx pgx.Tx, id ids.OrganizationID, originURL, by string) error {
	if err := storekit.StampFields(ctx, tx, "organization", id.UUID, companySourceSiteRead, by,
		[]storekit.FieldStamp{{Field: logoFieldName, EvidenceRef: &originURL}}); err != nil {
		return err
	}
	delta := map[string]any{logoFieldName: originURL}
	auditID, err := storekit.Audit(ctx, tx, "update", "organization", id.UUID, nil, map[string]any{
		auditKeySource: companySourceSiteRead, auditKeySourceURL: originURL,
		auditKeyFields: delta,
	})
	if err != nil {
		return fmt.Errorf("audit organization logo: %w", err)
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, crmcontracts.PublicEventOrganizationUpdated{
		ChangedFields: map[string]any{
			eventKeyDelta:  map[string]any{auditKeyFields: delta},
			auditKeySource: companySourceSiteRead, auditKeySourceURL: originURL,
		},
	}); err != nil {
		return fmt.Errorf("emit organization.updated: %w", err)
	}
	return nil
}

// LogoHeldByHuman answers whether a person set this organization's logo, for a
// caller that must know BEFORE it does expensive or irreversible work — the
// site read asks first so it neither fetches a logo it may not use nor
// overwrites the object a person's own logo already occupies. It carries the
// record's read gate, so an organization the caller cannot see is not found.
//
// The write applies the same rule again under a row lock: this read is an
// optimization and a byte-safety check, never the authority.
func (s *Store) LogoHeldByHuman(ctx context.Context, id ids.OrganizationID) (bool, error) {
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		return false, err
	}
	var held bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "organization", id.UUID); err != nil {
			return err
		}
		var err error
		held, err = logoHeldByHuman(ctx, tx, id)
		return err
	})
	return held, err
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
