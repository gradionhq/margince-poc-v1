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
