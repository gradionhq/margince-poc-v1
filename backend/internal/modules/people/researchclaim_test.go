// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import "testing"

// The run path refuses a javascript: source; so must the save path. A client is
// not obliged to send back what the run returned, and a read that refuses while
// a write accepts is how untrusted input lands in the record and waits for a
// renderer to turn it into a sink.
func TestOnlyAnOpenableDocumentCountsAsASource(t *testing.T) {
	for _, refused := range []string{
		"javascript:fetch('//evil')",
		"data:text/html,<script>",
		"ftp://example.com/x",
		"http:", // parses cleanly, points nowhere
		"",
	} {
		if webSourceURL(refused) {
			t.Errorf("%q was accepted as a source; a reader cannot open it", refused)
		}
	}
	for _, allowed := range []string{
		"https://example.com/team",
		"http://example.com/bio",
	} {
		if !webSourceURL(allowed) {
			t.Errorf("%q was refused; it is a document a reader can open", allowed)
		}
	}
}
