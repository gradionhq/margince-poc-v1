// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"io"
	"testing"
)

// readBody reads a captured request body inside an httptest handler, failing
// the test loudly on a read error rather than swallowing it. The handler runs
// on its own goroutine, so it reports via t.Errorf (t.Fatal's FailNow is only
// legal on the test's own goroutine).
func readBody(t *testing.T, r io.Reader) []byte {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Errorf("reading request body: %v", err)
	}
	return b
}
