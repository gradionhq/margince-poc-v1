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
	"time"

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
// Only the attempt that currently HOLDS the read takes the reference: the read
// must still be running AND still carry the lease the caller was handed at
// BeginSiteRead (SiteReadClaim.ClaimedAt). Running alone is not enough, because
// a reclaim puts a running row back into running under a NEW attempt: a stalled
// worker resuming afterwards would overwrite the mark the current attempt just
// parked and be handed that attempt's object back as superseded, so its caller
// would delete the very bytes the dossier had adopted.
//
// Past the claim the row has also already answered for its mark: a read that
// ended without a company had its parked object collected on the way to
// terminal, and a read that ended with a report handed a human a draft whose
// mark must not change while they review it. A park after either is a reference
// nothing adopts and nothing collects, which is the one orphan this lane can no
// longer find.
//
// It reports whether the dossier took the reference and hands back the key the
// row named before, so the caller can reclaim bytes nothing references any more.
// A refused park hands back none — the object stored for it is the caller's to
// collect. Same contract as SetOrganizationLogo, for the same reason: each
// attempt writes its own key, so two resolves of one read can never write the
// same object.
//
// No auth.Require, the same rationale as BeginSiteRead: the worker is not a
// human principal, the human's authority was checked when the read was started,
// and the workspace-bound transaction still scopes the write to the job's
// tenant. The reference on the dossier is operational, like every other worker
// write to this row; the audited write on the RECORD happens when a
// confirmation binds it.
func (s *Store) RecordSiteReadLogo(ctx context.Context, readID ids.UUID, claimedAt time.Time, objectKey, originURL string) (recorded bool, supersededKey *string, err error) {
	if objectKey == "" || originURL == "" {
		return false, nil, errors.New("people: a resolved logo needs both its storage key and the URL it was resolved from")
	}
	if claimedAt.IsZero() {
		return false, nil, errors.New("people: parking a website read's mark needs the lease BeginSiteRead handed the attempt that resolved it")
	}
	err = s.tx(ctx, func(tx pgx.Tx) error {
		// RETURNING the pre-write key, exactly as SetOrganizationLogo does: the
		// sub-select reads the statement's own snapshot, so it names the object
		// this write supersedes rather than the one it just stored.
		var previous *string
		err := tx.QueryRow(ctx, `
			UPDATE site_read SET logo_object_key = $2, logo_origin = $3, updated_at = now()
			WHERE id = $1 AND status = 'running' AND started_at = $4
			  AND organization_id IS NULL AND confirmed_at IS NULL
			RETURNING (SELECT sr.logo_object_key FROM site_read sr WHERE sr.id = $1)`,
			readID, objectKey, originURL, claimedAt.UTC()).Scan(&previous)
		if errors.Is(err, pgx.ErrNoRows) {
			// Bound, confirmed, no longer running, or running for somebody else:
			// the read has answered for its mark without this one, and recording
			// it now would name bytes no record adopts and no collection reaches.
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

// logoUnwornByAnyOrganization is the safety proof every drop of a parked
// reference carries, in one spelling: the reference goes only while no
// organization names the same key, so an object a company wears is never
// reported as collectable. Keys carry their workspace prefix and RLS scopes the
// check to that same tenant, so a foreign record's bytes cannot be described
// here at all. It reads the `sr` alias its statements give site_read.
const logoUnwornByAnyOrganization = `NOT EXISTS (
		SELECT 1 FROM organization o WHERE o.logo_object_key = sr.logo_object_key)`

// DiscardSiteReadLogo drops the mark parked on a read that can never hand it
// over, and answers the storage key it dropped so the caller collects the
// bytes — this module owns no object store, so reclaiming is something it can
// only ever REPORT.
//
// A confirmation is the only thing that adopts a parked mark, and it accepts
// none but a done or partial read. A read that ended without a dossier —
// failed, or cancelled because the operator withdrew the setting that queued it
// — therefore holds bytes no record will ever wear, and the reference on the
// dossier is the only thing left that can still find them.
//
// No auth.Require, the same rationale as RecordSiteReadLogo: the worker is not
// a human principal, and the parked reference is operational state on the
// dossier row rather than a fact on a record.
func (s *Store) DiscardSiteReadLogo(ctx context.Context, readID ids.UUID) (*string, error) {
	var discarded *string
	err := s.tx(ctx, func(tx pgx.Tx) error {
		// RETURNING the pre-write key, exactly as the two writes above do: the
		// sub-select reads the statement's own snapshot, so it names the object
		// this UPDATE just unreferenced.
		err := tx.QueryRow(ctx, `
			UPDATE site_read sr SET logo_object_key = NULL, logo_origin = NULL, updated_at = now()
			WHERE sr.id = $1 AND sr.confirmed_at IS NULL AND sr.logo_object_key IS NOT NULL
			  AND sr.status IN ('failed', 'cancelled')
			  AND `+logoUnwornByAnyOrganization+`
			RETURNING (SELECT parked.logo_object_key FROM site_read parked WHERE parked.id = $1)`,
			readID).Scan(&discarded)
		if errors.Is(err, pgx.ErrNoRows) {
			// Nothing parked, a read that may still be confirmed, or bytes a
			// record already wears: all three keep the object.
			return nil
		}
		if err != nil {
			return fmt.Errorf("discard the website read's logo: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return discarded, nil
}

// bindSiteReadLogo gives the company a confirmation creates the mark its own
// website read already resolved — the step that makes the anchor's face arrive
// on the same terms as every other company's (A55). It runs inside the
// confirmation's transaction, so the company and its logo commit together.
//
// Fill-empty, like every site-read field the confirmation applies: a mark the
// record already wears — a person's own, or one an earlier read landed — stays,
// and the parked object is not adopted. Adoption MOVES the dossier's reference
// onto the record; a decline hands that reference back to the caller as a key
// to collect, in the same transaction that declined, so the row and the report
// can never disagree about who holds those bytes.
//
// It answers the unadopted key, never a deleted object: this module owns no
// object store, so reclaiming is something it can only ever REPORT.
func bindSiteReadLogo(ctx context.Context, tx pgx.Tx, readID ids.UUID, orgID ids.OrganizationID, reclaim bool) (*string, error) {
	// unadopted names what the confirmation leaves behind for its caller. It
	// stays nil on every path that leaves nothing: no mark was parked, or the
	// anchor took the one that was.
	var unadopted *string
	var objectKey, originURL *string
	if err := tx.QueryRow(ctx,
		`SELECT logo_object_key, logo_origin FROM site_read WHERE id = $1`, readID).
		Scan(&objectKey, &originURL); err != nil {
		return nil, fmt.Errorf("read the website read's logo: %w", err)
	}
	if objectKey == nil || *objectKey == "" || originURL == nil || *originURL == "" {
		// The read resolved nothing usable — an air-gapped install, a site that
		// declares no icon, an asset that would not decode. The record draws its
		// deterministic monogram, which is a face rather than a gap.
		return unadopted, nil
	}
	held, err := logoHeldByHuman(ctx, tx, orgID)
	if err != nil {
		return nil, err
	}
	if held {
		return releaseParkedSiteReadLogo(ctx, tx, readID, reclaim)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE organization SET logo_object_key = $2, logo_origin = $3
		WHERE id = $1 AND archived_at IS NULL AND logo_object_key IS NULL`,
		orgID, *objectKey, *originURL)
	if err != nil {
		return nil, fmt.Errorf("bind the website read's logo: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// The record already wears a mark, or was archived under this
		// confirmation: the same decline as a human-held field, and the same
		// parked object left over.
		return releaseParkedSiteReadLogo(ctx, tx, readID, reclaim)
	}
	// Handed over, not shared: the record is the object's one reference now.
	// Two rows naming one key would let a later resolve of this organization
	// supersede it, collect the bytes, and leave the dossier pointing at an
	// object nothing can serve. The dossier keeps its reference only while
	// NOTHING else holds it — that is what makes an unadopted mark findable,
	// and the reason this clears only on the run that actually adopted.
	if _, err := tx.Exec(ctx, `
		UPDATE site_read SET logo_object_key = NULL, logo_origin = NULL, updated_at = now()
		WHERE id = $1`, readID); err != nil {
		return nil, fmt.Errorf("hand the website read's logo to the company: %w", err)
	}
	// The site read is what captured this, never the human who confirmed the
	// draft: provenance is written once and never re-derived, and a machine mark
	// recorded under a person's name would make the human-precedence guard
	// refuse every later resolve for a logo nobody chose.
	if err := recordLogoWrite(ctx, tx, orgID, *originURL, companySiteReadCapturedBy); err != nil {
		return nil, err
	}
	return unadopted, nil
}

// releaseParkedSiteReadLogo drops the reference a confirmation declined to
// adopt and answers the key it dropped, so the caller collects bytes no record
// wears. The confirmation holds this dossier's row lock, so the drop and the
// answer describe one state.
//
// A caller that owns no object store asks for no release and the reference
// STAYS. The parked key embeds a per-attempt uuid nothing else recorded — it is
// the object's last handle — so clearing it for a caller that cannot delete
// would turn a findable orphan into an unfindable one. It is the confirm path's
// spelling of the guard the worker keeps in front of DiscardSiteReadLogo.
func releaseParkedSiteReadLogo(ctx context.Context, tx pgx.Tx, readID ids.UUID, reclaim bool) (*string, error) {
	// released stays nil for the two outcomes that free nothing: a caller that
	// cannot collect, and bytes a record already wears — the statement matches no
	// row then, and the reference stays with the record.
	var released *string
	if !reclaim {
		return released, nil
	}
	// RETURNING the pre-write key, exactly as the writes above do: the
	// sub-select reads the statement's own snapshot, so it names the object this
	// UPDATE just unreferenced.
	err := tx.QueryRow(ctx, `
		UPDATE site_read sr SET logo_object_key = NULL, logo_origin = NULL, updated_at = now()
		WHERE sr.id = $1 AND sr.logo_object_key IS NOT NULL
		  AND `+logoUnwornByAnyOrganization+`
		RETURNING (SELECT parked.logo_object_key FROM site_read parked WHERE parked.id = $1)`,
		readID).Scan(&released)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("release the website read's unadopted logo: %w", err)
	}
	return released, nil
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
