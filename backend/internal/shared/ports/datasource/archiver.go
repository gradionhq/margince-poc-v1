// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package datasource

import "context"

// The post-v1 archive surface lives here.
//
// SystemOfRecordProvider.Archive takes a ref and nothing else, and the v1
// method set is frozen (datasource.go, TestSystemOfRecordProviderV1MethodSetIsFrozen)
// — so the three questions a confirm-first archive has to ask BEFORE it spends
// a human's approval had nowhere to be asked. Each was answered instead by an
// assumption the caller made about the executor, and each assumption was wrong
// in a different way:
//
//   - WHAT does the routed executor archive? The tool held a hard-coded list of
//     what the NATIVE provider archives, so an overlay installation staged
//     project, relationship and activity archives its own writer refuses.
//   - MAY this caller archive THIS row? Staging refused only an archive whose
//     target the caller could not READ, while every executor also requires
//     write authority — so a rep who may read a colleague's record staged an
//     archive that was refused after a human released it.
//   - WHICH VERSION was approved? The approval pins the target's version and
//     the redemption verifies it, then the archive runs in a LATER transaction
//     with no version clause at all.
//
// The freeze's own escape hatch is a new interface plus a runtime capability
// probe, and the type below is that interface.

// RecordArchiverV2 is the archive surface a provider answers when it can carry
// what the v1 verb has no field for. A provider that does not implement it
// keeps the v1 behaviour, which is why every caller states what it assumes in
// that case rather than assuming silently.
type RecordArchiverV2 interface {
	// ArchivableTypes names the entity types this provider archives for the
	// caller in ctx. It is a question about the ROUTED executor rather than
	// about the seam vocabulary: the two disagree the moment an installation
	// runs in a mode whose writer is narrower than the native one.
	ArchivableTypes(ctx context.Context) ([]EntityType, error)

	// RefuseArchive answers every refusal ArchiveAt would answer with, and
	// performs no write. It is what makes "refuse before staging what the
	// executor refuses afterwards" a property of the seam rather than a habit
	// each caller has to remember.
	//
	// It runs the AUTHORITY checks, not the concurrency ones: a version that
	// is right now can be wrong by the time a human answers, so a pin is the
	// write's business and never a reason to refuse a staging.
	RefuseArchive(ctx context.Context, ref EntityRef) error

	// ArchiveAt is Archive conditioned on the version the caller's authority
	// was granted against. On skew it returns apperrors.ErrVersionSkew and
	// changes nothing, exactly as UpdateInput.IfVersion does — the two are the
	// same guarantee, and the archive verb simply had no field to carry it.
	//
	// A nil IfVersion is the ordinary unapproved write and takes the row lock
	// instead, so this is never LESS guarded than the v1 verb it replaces.
	ArchiveAt(ctx context.Context, in ArchiveInput) (EntityRef, error)
}

// ArchiveInput is one archive plus the version it was authorized against.
//
// IfVersion carries the caller's If-Match value on the REST door and the
// version the APPROVAL was granted against on the agent door. Those are the
// same fact — "the record as the decider saw it" — which is why one field
// serves both rather than a second member naming where it came from.
type ArchiveInput struct {
	Ref       EntityRef
	IfVersion *int64
}
