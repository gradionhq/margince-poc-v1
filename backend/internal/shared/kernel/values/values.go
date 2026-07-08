// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package values holds the domain value objects: parse-don't-validate
// types for the formats that would otherwise travel as bare strings
// with their normalization rules scattered across call sites (email
// lowercasing, E.164 phones, host-only domains, the money pair). Each
// type parses and normalizes ONCE at the seam where input enters a
// store; downstream code cannot hold a malformed value because one is
// unrepresentable. Tier-0: stdlib only, like the rest of shared/kernel.
package values

// ParseError is the client-fault carrier every constructor returns: the
// transport maps it onto the 422 validation shape (the frozen apperrors
// registry defines no validation sentinel; typed errors mapped at the
// edge are the house pattern — storekit.MalformedCursorError).
type ParseError struct {
	Field   string
	Code    string
	Message string
}

func (e *ParseError) Error() string { return e.Field + ": " + e.Message }
