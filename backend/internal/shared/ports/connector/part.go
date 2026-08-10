// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package connector

// The files a captured record carried, and the ones it could not.
//
// Their own file because they are their own concept: everything else on the
// seam describes the record — who was on it, when it happened, what it is
// about — and these describe what came WITH it. A connector reports both or
// neither, and reporting neither is the ordinary case.

// Part is one file a captured record carried.
//
// Body is held in memory because every bound that decides whether it may be
// held has already been applied by the time one exists.
type Part struct {
	// Ordinal identifies the part WITHIN its message, counted over every
	// attachment part including dropped ones. Together with the record's source
	// id it is what capture is idempotent on, so it must not shift between
	// pulls of the same message.
	Ordinal int
	// Filename is presentational and already sanitized. Nothing opens a file by
	// it; the object key is generated.
	Filename string
	// ContentType is what the BYTES say. DeclaredType carries the sender's
	// claim only where it disagreed, so the disagreement stays inspectable.
	ContentType  string
	DeclaredType string
	Body         []byte
}

// PartDrop is how many files one bound refused, and why.
//
// A COUNT rather than a row per file, because the count is attacker-chosen: a
// single message can contain hundreds of thousands of empty parts, and one
// breadcrumb each would let a sender decide how many rows our own audit trail
// writes. The reason is the fact worth keeping; the tally is the scale.
//
// It names no filename on purpose — it reaches an operational log, and a sender
// writes the filename.
type PartDrop struct {
	Reason string
	Count  int
}
