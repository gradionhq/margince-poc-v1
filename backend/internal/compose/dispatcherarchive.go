// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The archive verb's half of the Dispatcher, split out when dispatcher.go
// reached the file-length cap.
//
// It is more than one method because the archive is more than one question:
// what the routed executor archives, what it would refuse, and the write
// itself. All three route by the SAME mode read, and keeping them together is
// what makes that visible — an installation whose mode says overlay must not
// have one of the three answered by the native provider.

import (
	"context"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// Archive dispatches to the overlay mirror or the native SoR modules
// per ctx's workspace.x_sor_mode; see Create's doc on overlay's write
// gap.
func (d *Dispatcher) Archive(ctx context.Context, ref datasource.EntityRef) (datasource.EntityRef, error) {
	return d.ArchiveAt(ctx, datasource.ArchiveInput{Ref: ref})
}

// archiverInMode is the ROUTED archiver for a caller that already resolved the
// mode this request — see updateInMode.
//
// Both arms answer datasource.RecordArchiverV2, which is what lets every
// archive question — what is archivable, what would be refused, and the write
// itself — reach the executor that will actually answer it. A caller that
// asked the native provider while the installation runs in overlay mode got
// three right answers about the wrong writer.
//
//nolint:ireturn // the routing IS the return: naming either concrete provider here would be the assumption about the executor this seam exists to remove
func (d *Dispatcher) archiverInMode(ov bool) datasource.RecordArchiverV2 {
	if ov {
		return d.overlay
	}
	return d.native
}

// ArchivableTypes is datasource.RecordArchiverV2's, resolved per request: an
// overlay installation archives three types and a native one six, so the
// answer is a fact about THIS workspace's mode rather than about the seam.
func (d *Dispatcher) ArchivableTypes(ctx context.Context) ([]datasource.EntityType, error) {
	ov, err := d.isOverlayUncached(ctx)
	if err != nil {
		return nil, err
	}
	return d.archiverInMode(ov).ArchivableTypes(ctx)
}

// RefuseArchive is datasource.RecordArchiverV2's stage-time half: the routed
// executor's own authority refusals, and nothing else.
//
// The egress backstop is deliberately NOT repeated here. It guards the WRITE,
// and its own doc places it on updateInMode/archiveInMode precisely so the one
// path that dispatches directly cannot slip underneath it; a third call site
// would be a third thing to keep in step.
//
// What is NOT claimed, because it is not true: that a staging against an
// overlay-served record is always refused earlier by refuseStagingElsewhere.
// That holds only while the seam read and this refusal resolve the SAME mode,
// and they do not — Dispatcher.Read answers from the cached mode (5s TTL, and
// Invalidate reaches only the process that committed a flip) while this asks
// isOverlayUncached. Inside that window, on a replica the invalidation never
// reached, the read can route native and pass while this routes overlay. The
// backstop on the write is what still catches it, which is the reason it stays
// where its own doc puts it rather than being duplicated here.
func (d *Dispatcher) RefuseArchive(ctx context.Context, ref datasource.EntityRef) error {
	ov, err := d.isOverlayUncached(ctx)
	if err != nil {
		return err
	}
	return d.archiverInMode(ov).RefuseArchive(ctx, ref)
}

// ArchiveAt is Archive carrying the version the caller's authority named.
func (d *Dispatcher) ArchiveAt(ctx context.Context, in datasource.ArchiveInput) (datasource.EntityRef, error) {
	ov, err := d.isOverlayUncached(ctx)
	if err != nil {
		return datasource.EntityRef{}, err
	}
	return d.archiveInMode(ctx, ov, in)
}

// archiveInMode is ArchiveAt for a caller that already resolved the mode this
// request — see updateInMode.
func (d *Dispatcher) archiveInMode(ctx context.Context, ov bool, in datasource.ArchiveInput) (datasource.EntityRef, error) {
	if ov {
		if err := refuseUngovernedAgentEgress(ctx, overlay.WriteArchive, in.Ref.Type); err != nil {
			return datasource.EntityRef{}, err
		}
	}
	return d.archiverInMode(ov).ArchiveAt(ctx, in)
}
