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

// PartDrop is one file that did not survive the inbound bounds. It names no
// filename on purpose: it is written to an operational breadcrumb, which
// records the reason and the natural key and nothing a sender wrote.
type PartDrop struct {
	Ordinal int
	Reason  string
}
