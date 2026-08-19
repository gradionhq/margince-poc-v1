// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package extension

// The files a captured record carried, the ones it could not, and the files an
// outbound message carries.
//
// They live on the PUBLISHED surface rather than in the core's connector port
// because a unit and a core connector must hand the file keeper the same type.
// The alternative — a mirrored type on each side kept honest by a parity test —
// is what ports/jurisdiction already rejected for the pack contract: "aliased so
// a pack registered by an extension and one registered by a core module are the
// same type". A second spelling of a file is a second set of bounds that can
// disagree about how large a file may be.

// InboundFile is one file a captured record carried.
//
// Body is held in memory because every bound that decides whether it may be held
// has already been applied by the time one exists — see MaxInboundMessageBytes,
// which is the bound that makes that safe rather than merely true.
type InboundFile struct {
	// Ordinal identifies the file WITHIN its message, counted over every
	// attachment part including dropped ones. Together with the record's source
	// id it is what capture is idempotent on, so it must not shift between pulls
	// of the same message.
	Ordinal int
	// Filename is presentational and already sanitized (SafeFilename). Nothing
	// opens a file by it; the object key is generated.
	Filename string
	// ContentType is what the BYTES say. DeclaredType carries the sender's claim
	// only where it disagreed, so the disagreement stays inspectable.
	ContentType  string
	DeclaredType string
	Body         []byte
}

// FileDrop is how many files one bound refused, and why.
//
// A COUNT rather than a row per file, because the count is attacker-chosen: a
// single message can contain hundreds of thousands of empty parts, and one
// breadcrumb each would let a sender decide how many rows our own audit trail
// writes. The reason is the fact worth keeping; the tally is the scale.
//
// It names no filename on purpose — it reaches an operational log, and a sender
// writes the filename.
type FileDrop struct {
	Reason string
	Count  int
}

// OutboundFile is one file to transmit, in provider-neutral form. The connector
// owns the wire encoding, exactly as it does for the body.
//
// The identifying fields travel WITH the bytes rather than being looked up at
// send time, because the outbound record snapshots them: archiving or superseding
// a document later must not rewrite the history of what was attached to a message
// that already went out.
type OutboundFile struct {
	AttachmentID string
	Filename     string
	ContentType  string
	ByteSize     int64
	Checksum     string
	Body         []byte
}
