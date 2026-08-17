// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package httperr

import (
	"errors"
	"fmt"
	"net/http"
)

// WriteMultipartRefusal answers a failed multipart parse, telling the two
// failures apart.
//
// A body that is not multipart and a body that is too large are different
// problems with different next moves, and one message for both misdirects
// whichever caller it did not describe: a client sending well-formed multipart
// is told its framing might be wrong, and a client sending a small malformed
// body is told about a size limit it never approached. So the oversize case
// answers 413 and NAMES the limit — the one actionable fact — and everything
// else answers 422 about framing only.
//
// `limit` is the ceiling the caller applied, in bytes; pass the same value the
// MaxBytesReader was given, or the sentence describes a limit nothing enforced.
func WriteMultipartRefusal(w http.ResponseWriter, r *http.Request, err error, limit int64) {
	Write(w, r, MultipartRefusal(err, limit))
}

// MultipartRefusal is WriteMultipartRefusal for a caller that returns its
// refusal rather than writing it.
func MultipartRefusal(err error, limit int64) *DetailedError {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return &DetailedError{
			Status: http.StatusRequestEntityTooLarge,
			Code:   "body_too_large",
			Detail: fmt.Sprintf("the upload exceeds the %s limit", Megabytes(limit)),
		}
	}
	return Validation("file", "invalid_multipart",
		"the request must be sent as multipart/form-data")
}

// Megabytes renders a byte ceiling the way the person it just refused will
// measure their own file: in decimal MB.
//
// Two rules, and the second is the one that matters. A whole number prints
// whole — every configured ceiling is a whole number of decimal MB, so this is
// the case that actually ships. Anything else keeps one decimal ROUNDED DOWN,
// never to nearest, because a stated limit must never exceed the enforced one:
// rounding 999,999 up to "1.0 MB" invites a 1,000,000-byte file that is then
// refused by the sentence that welcomed it. Understating by less than 0.1 MB
// costs the reader nothing; overstating by a byte costs them a whole upload.
//
// The version this replaces divided and truncated to whole MB, which read a
// 8,388,608-byte ceiling as "8 MB" — refusing an 8.2 MB file the sentence said
// would fit — and any ceiling below a megabyte as "0 MB".
func Megabytes(limit int64) string {
	if limit%bytesPerMB == 0 {
		return fmt.Sprintf("%d MB", limit/bytesPerMB)
	}
	tenths := limit * 10 / bytesPerMB
	return fmt.Sprintf("%d.%d MB", tenths/10, tenths%10)
}

// bytesPerMB is the decimal megabyte every upload ceiling is denominated in
// (OPS-CFG-12).
const bytesPerMB = 1_000_000
