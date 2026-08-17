// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package httperr_test

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// Two failures, two answers. One message for both misdirects whichever caller
// it did not describe: a client sending well-formed multipart is told its
// framing might be wrong, and a client sending a small malformed body is told
// about a size limit it never approached.

func TestAnOversizeUploadIsRefusedByItsSize(t *testing.T) {
	const limit = 25_000_000
	refusal := httperr.MultipartRefusal(
		fmt.Errorf("parsing form: %w", &http.MaxBytesError{Limit: limit}), limit)

	if refusal.Status != http.StatusRequestEntityTooLarge {
		t.Errorf("status %d, want 413 — an oversize body is not a framing mistake",
			refusal.Status)
	}
	// The limit is the one actionable fact. Without it the reader's next move is
	// to guess at a smaller file.
	if !strings.Contains(refusal.Detail, "25 MB") {
		t.Errorf("detail %q does not name the limit", refusal.Detail)
	}
}

func TestTheSizeIsReadThroughAWrappedError(t *testing.T) {
	// ParseMultipartForm does not hand back the reader's error bare, so a check
	// that compared types directly would send every oversize upload down the
	// framing branch — which is what makes this worth its own test.
	const limit = 25_000_000
	wrapped := fmt.Errorf("outer: %w",
		fmt.Errorf("inner: %w", &http.MaxBytesError{Limit: limit}))

	if got := httperr.MultipartRefusal(wrapped, limit).Status; got != http.StatusRequestEntityTooLarge {
		t.Errorf("a wrapped MaxBytesError answered %d, want 413", got)
	}
}

// The refusal names a number the reader will compare against their own file, so
// it has to be the number actually enforced. The version this replaces divided
// by a million and truncated, which read an 8,388,608-byte ceiling as "8 MB" —
// refusing an 8.2 MB file the sentence said would fit — and read anything below
// a megabyte as "0 MB", which describes nothing at all.
func TestTheRefusalNamesTheLimitItActuallyEnforced(t *testing.T) {
	for _, tc := range []struct {
		limit int64
		want  string
	}{
		{25_000_000, "25 MB"},   // the shipped default: whole, prints whole
		{8 << 20, "8.4 MB"},     // a binary constant is not a whole decimal MB
		{10 << 20, "10.5 MB"},   // and neither is this one
		{12_500_000, "12.5 MB"}, // an operator may configure a half
		{900_000, "0.9 MB"},     // below a megabyte still describes itself
	} {
		if got := httperr.Megabytes(tc.limit); got != tc.want {
			t.Errorf("a %d-byte ceiling reads as %q, want %q", tc.limit, got, tc.want)
		}
	}
}

func TestAMalformedBodyIsRefusedByItsFraming(t *testing.T) {
	const limit = 25_000_000
	refusal := httperr.MultipartRefusal(errors.New("no multipart boundary param"), limit)

	if refusal.Status != http.StatusUnprocessableEntity {
		t.Errorf("status %d, want 422", refusal.Status)
	}
	// It must NOT mention a size: this body never approached one, and saying so
	// sends the reader off to shrink a file that was never too big.
	if strings.Contains(refusal.Detail, "MB") {
		t.Errorf("detail %q blames a size limit for a framing failure", refusal.Detail)
	}
}
