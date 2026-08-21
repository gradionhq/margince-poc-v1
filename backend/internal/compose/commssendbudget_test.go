// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The aggregate send budget: the one bound on what a message's files may total,
// and the one thing that decides whether exceeding it parks or retries.
//
// It is tested here rather than through ReadForSend deliberately. That read
// needs a pool, a blobstore and twenty megabytes of fixture to reach the
// comparison, and the property under test is not the reading — it is the
// CLASSIFICATION, because getting that wrong is the difference between a person
// being told what happened and a delivery spending its whole retry ladder
// re-reading the same files before parking under a reason that names no cause.

import (
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

func TestTheAggregateSendBudgetParksInsteadOfRetrying(t *testing.T) {
	for _, tc := range []struct {
		name    string
		total   int64
		refused bool
	}{
		{"an empty message", 0, false},
		{"exactly at the bound", maxSendBytes, false},
		{"one byte over", maxSendBytes + 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := overSendBudget(tc.total)
			if !tc.refused {
				if err != nil {
					t.Fatalf("a total of %d bytes was refused: %v", tc.total, err)
				}
				return
			}
			// The sentinel is the whole point: connector.ErrFilesNotCarried is what
			// the dispatcher parks on. Any other error — including a bare one — puts
			// this on the retry ladder, where a deterministic refusal re-reads every
			// object once per rung and then parks naming nothing.
			if !errors.Is(err, connector.ErrFilesNotCarried) {
				t.Fatalf("a total of %d bytes → %v, want ErrFilesNotCarried so the delivery parks", tc.total, err)
			}
			// And it names the bound, because a refusal a person cannot act on is
			// the failure this reason exists to avoid.
			if !strings.Contains(err.Error(), "20 MiB") {
				t.Errorf("refusal %q does not name the bound a sender has to get under", err)
			}
		})
	}
}
