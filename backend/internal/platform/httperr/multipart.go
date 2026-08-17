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
// `limit` is the ceiling the caller applied, in bytes, and is rendered as whole
// megabytes; pass the same constant the MaxBytesReader was given, or the
// sentence describes a limit nothing enforced.
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
			Detail: fmt.Sprintf("the upload exceeds the %d MB limit", limit/1_000_000),
		}
	}
	return Validation("file", "invalid_multipart",
		"the request must be sent as multipart/form-data")
}
