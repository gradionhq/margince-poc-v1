// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// The archive verb's half of the overlay provider, split out when
// provider_writes.go reached the file-length cap.
//
// The set below and the three questions under it belong together: what this
// writer archives is the reason two of the three answers differ from the
// native provider's, and a reader who finds one without the other is exactly
// the reader who wrote "the same set the native provider archives" into a
// comment that had stopped being true.

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// archivableTypes are the entity types overlay Archive supports.
//
// This set is NARROWER than the native provider's, and saying so is the point:
// native archives project, relationship and activity as well, and the comment
// here used to claim the two sets were "the same". A stage-time check that
// trusted that claim admitted an archive this writer refuses — the approval
// spent on a call that could never run, which is the one failure the
// confirm-first archive exists to prevent. Nothing here derives the native
// set, and nothing there derives this one: ArchivableTypes below is how a
// caller asks the ROUTED executor instead of assuming either.
var archivableTypes = map[datasource.EntityType]bool{
	datasource.EntityPerson:       true,
	datasource.EntityOrganization: true,
	datasource.EntityDeal:         true,
}

// Archive removes a record from the incumbent (its own archive/delete) after
// the stored-baseline drift check, then purges the mirror row so it stops
// being readable rather than lingering visible until the next sync.
func (p *Provider) Archive(ctx context.Context, r datasource.EntityRef) (datasource.EntityRef, error) {
	return p.ArchiveAt(ctx, datasource.ArchiveInput{Ref: r})
}

// ArchivableTypes is datasource.RecordArchiverV2's: what THIS writer archives,
// which is three of the native provider's six.
func (p *Provider) ArchivableTypes(context.Context) ([]datasource.EntityType, error) {
	return slices.Sorted(maps.Keys(archivableTypes)), nil
}

// RefuseArchive is datasource.RecordArchiverV2's stage-time half: the type
// check, the object grant, and the mirror row's own scope gate — every refusal
// ArchiveAt answers with before it reaches the incumbent, and no write.
//
// The incumbent's drift check is deliberately NOT run here. It compares a
// baseline against the incumbent's live record, and that comparison is only
// meaningful in the transaction that writes: a record that has not drifted
// while a human decides is not a record that will not have drifted when they
// answer.
func (p *Provider) RefuseArchive(ctx context.Context, r datasource.EntityRef) error {
	if err := requireSupportedWrite(WriteArchive, r.Type); err != nil {
		return err
	}
	if err := auth.Require(ctx, string(r.Type), principal.ActionDelete); err != nil {
		return err
	}
	if p.ms == nil {
		return errNoMirrorStore()
	}
	_, err := p.ms.Get(ctx, string(r.Type), uuidToExternalID(r.ID))
	return err
}

// ArchiveAt is datasource.RecordArchiverV2's write half.
//
// A version pin is REFUSED rather than ignored. overlay_mirror carries an
// UpdatedAtBaseline and no `version` column, so there is no number here for a
// native version to be compared against — and accepting the pin to drop it
// would reproduce, one layer down, exactly the defect this seam was widened to
// fix: an approval granted against a version that nothing then checks.
func (p *Provider) ArchiveAt(ctx context.Context, in datasource.ArchiveInput) (datasource.EntityRef, error) {
	if in.IfVersion != nil {
		return datasource.EntityRef{}, fmt.Errorf(
			"this workspace's system of record archives a %s without a version precondition, so an "+
				"archive approved against version %d cannot be carried out as approved: %w",
			in.Ref.Type, *in.IfVersion, apperrors.ErrUnsupportedBySoR)
	}
	return p.archiveThroughIncumbent(ctx, in.Ref)
}

func (p *Provider) archiveThroughIncumbent(ctx context.Context, r datasource.EntityRef) (datasource.EntityRef, error) {
	if err := requireSupportedWrite(WriteArchive, r.Type); err != nil {
		return datasource.EntityRef{}, err
	}
	if err := auth.Require(ctx, string(r.Type), principal.ActionDelete); err != nil {
		return datasource.EntityRef{}, err
	}
	if p.ms == nil {
		return datasource.EntityRef{}, errNoMirrorStore()
	}
	inc, err := p.writeIncumbent(ctx)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	externalID := uuidToExternalID(r.ID)
	// Row-scope gate + drift baseline: a record the actor cannot see is
	// ErrNotFound, never archived on their behalf.
	row, err := p.ms.Get(ctx, string(r.Type), externalID)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	if err := inc.Archive(ctx, string(r.Type), externalID, row.UpdatedAtBaseline); err != nil {
		return datasource.EntityRef{}, err
	}
	// The incumbent has archived the record; the local half runs detached
	// from the caller (afterIncumbentCommit) because a closed tab must not
	// leave a record that no longer exists at the incumbent still listed and
	// still readable here until the next full reconcile sweep.
	//
	// The purge goes through the disconnect fence so a teardown racing the
	// archive cannot leave the row readable, matching the sync path — and the
	// archive's audit_log and event_outbox rows commit in that same
	// transaction, so a record removed from the customer's own CRM can never
	// be missing from the ledger that answers who removed it.
	localCtx, cancel := afterIncumbentCommit(ctx)
	defer cancel()
	if err := p.commitArchiveWriteBack(localCtx, Deletion{ObjectClass: string(r.Type), ExternalID: externalID}, r); err != nil {
		return datasource.EntityRef{}, writePathError(err)
	}
	return r, nil
}
