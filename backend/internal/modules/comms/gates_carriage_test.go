// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package comms

// Every bound the carriage gate checks, for a mail delivery and a channel
// delivery alike.

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// gateHarness is a dispatcher whose only wired collaborator is the fake store,
// because a gate called directly touches nothing else — and the store is what
// holds the park reason a person has to be able to act on.
func gateHarness(t *testing.T) (*Dispatcher, *fakeStore) {
	t.Helper()
	store := &fakeStore{}
	return NewDispatcher(store, fakeResolver{}, liveSeat(), nil, &stubConsent{}, nil,
		func() time.Time { return testNow }, time.Hour, 3), store
}

// carriageCase builds the delivery shape under test: a given number of staged
// files, each of a given size, and a body of a given length in characters.
//
// It builds ONE shape rather than a mail and a channel variant, because the gate
// reads no field that distinguishes them — see the test's own comment.
func carriageCase(files int, fileBytes int64, bodyLen int) Delivery {
	del := Delivery{ID: ids.NewV7(), Provider: "gmail", Body: strings.Repeat("a", bodyLen)}
	for i := range files {
		del.Attachments = append(del.Attachments, OutboundFile{
			AttachmentID: ids.NewV7(),
			Filename:     "file-" + strconv.Itoa(i) + ".pdf",
			ByteSize:     fileBytes,
		})
	}
	return del
}

// It PARKS. It does not strip, it does not convert files to links, and it does
// not transmit the covering text alone — for every reason the gate's own doc
// gives: the sender's timeline records what was STAGED, so a recipient seeing
// fewer files than the record claims is a permanently wrong record nobody is
// told about.
//
// The gate is TRANSPORT-BLIND by construction — it reads the provider name, the
// staged set and the body, and nothing that says which seam resolved them — so
// every case here is stated once rather than twice. That one gate serves both
// transports is a claim about which seam the dispatcher resolves, and it is
// proved where that happens: TestAChannelReplyHandsItsFilesToTheProvider in
// dispatcher_channel_integration_test.go.
func TestAttachmentCarriageGateParksRatherThanStripping(t *testing.T) {
	carrying := connector.Carriage{Carries: true, MaxFiles: 10, MaxBytesPerFile: 1 << 20, MaxBodyWithFiles: 1024}
	for _, c := range []struct {
		name       string
		carriage   connector.Carriage
		files      int
		fileBytes  int64
		bodyLen    int
		wantPark   bool
		wantReason []string
	}{
		{name: "no files always passes", files: 0},
		{name: "carries nothing, files staged", files: 1, wantPark: true, wantReason: []string{"cannot carry files", "gmail", "file-0.pdf"}},
		{name: "over the file count", carriage: connector.Carriage{Carries: true, MaxFiles: 1, MaxBytesPerFile: 1 << 20}, files: 2, wantPark: true, wantReason: []string{"at most 1"}},
		{name: "over the per-file size", carriage: connector.Carriage{Carries: true, MaxFiles: 10, MaxBytesPerFile: 1 << 20}, files: 1, fileBytes: 2 << 20, wantPark: true, wantReason: []string{"larger than", "file-0.pdf"}},
		{name: "over the body-with-files bound", carriage: connector.Carriage{Carries: true, MaxFiles: 10, MaxBytesPerFile: 1 << 20, MaxBodyWithFiles: 8}, files: 1, bodyLen: 9, wantPark: true, wantReason: []string{"caption", "8"}},
		{name: "within every bound", carriage: carrying, files: 2, bodyLen: 10},
		{name: "a long body with NO files is not the caption case", carriage: connector.Carriage{Carries: true, MaxBodyWithFiles: 8}, files: 0, bodyLen: 4096},
		{name: "a sub-MiB bound is reported as itself, not as 0 MiB", carriage: connector.Carriage{Carries: true, MaxBytesPerFile: 900 << 10}, files: 1, fileBytes: 1 << 20, wantPark: true, wantReason: []string{"900.0 KiB"}},
		{name: "a bound that is not a whole MiB is not rounded up", carriage: connector.Carriage{Carries: true, MaxBytesPerFile: 10_000_000}, files: 1, fileBytes: 20 << 20, wantPark: true, wantReason: []string{"9.5 MiB"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			d, store := gateHarness(t)
			del := carriageCase(c.files, c.fileBytes, c.bodyLen)

			outcome, _, err := d.gateAttachmentCarriage(context.Background(), del, sendSeam{carriage: c.carriage})
			if err != nil {
				t.Fatalf("the gate returned an error rather than a disposition: %v", err)
			}
			parked := outcome == OutcomeParked
			if parked != c.wantPark {
				t.Fatalf("parked=%v, want %v (outcome %q)", parked, c.wantPark, outcome)
			}
			if !c.wantPark {
				if outcome != outcomeUndecided {
					t.Fatalf("outcome %q, want undecided — this message is within every bound", outcome)
				}
				return
			}
			for _, want := range c.wantReason {
				if !strings.Contains(store.parked, want) {
					t.Errorf("park reason %q does not say %q — a person reading it cannot tell what to fix", store.parked, want)
				}
			}
		})
	}
}
