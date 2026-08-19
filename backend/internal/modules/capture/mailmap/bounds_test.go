// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package mailmap

import (
	"testing"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// There is ONE number per bound. Mail's four and the published four are the same
// four, so a channel producer and a mail producer cannot disagree about how much
// one message may carry. The aggregate bound is the one that matters most: it is
// what keeps an in-memory Body safe, and publishing only the per-file bounds
// would license ten times what mail admits.
func TestMailBoundsAreThePublishedBounds(t *testing.T) {
	for _, c := range []struct {
		name      string
		mail, pub int
	}{
		{"files kept", maxParts, extension.MaxInboundFiles},
		{"files examined", maxPartsExamined, extension.MaxInboundFilesExamined},
		{"bytes per file", maxPartBytes, extension.MaxInboundFileBytes},
		{"bytes per message", maxMessageBytes, extension.MaxInboundMessageBytes},
	} {
		if c.mail != c.pub {
			t.Errorf("%s: mail says %d, the published surface says %d", c.name, c.mail, c.pub)
		}
	}
}
