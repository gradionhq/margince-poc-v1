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

// carriageCase builds the delivery shape under test: mail or channel, a given
// number of staged files, and a body of a given length in characters.
func carriageCase(channel bool, files int, fileBytes int64, bodyLen int) Delivery {
	del := Delivery{ID: ids.NewV7(), Provider: "gmail", Body: strings.Repeat("a", bodyLen)}
	if channel {
		recipient := "77123"
		del.Provider, del.ChannelUserID = "telegram", &recipient
	}
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
// Mail and channel run through THIS gate, not two of them. A channel-only branch
// is the shape that would quietly stop matching the rules the mail path keeps.
func TestAttachmentCarriageGateParksRatherThanStripping(t *testing.T) {
	carrying := connector.Carriage{Carries: true, MaxFiles: 10, MaxBytesPerFile: 1 << 20, MaxBodyWithFiles: 1024}
	for _, c := range []struct {
		name       string
		channel    bool
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
		{name: "channel: carries nothing", channel: true, files: 1, wantPark: true, wantReason: []string{"cannot carry files", "telegram"}},
		{name: "channel: over the file count", channel: true, carriage: connector.Carriage{Carries: true, MaxFiles: 1, MaxBytesPerFile: 1 << 20}, files: 2, wantPark: true, wantReason: []string{"telegram", "at most 1"}},
		{name: "channel: within every bound", channel: true, carriage: carrying, files: 1, bodyLen: 10},
	} {
		t.Run(c.name, func(t *testing.T) {
			d, store := gateHarness(t)
			del := carriageCase(c.channel, c.files, c.fileBytes, c.bodyLen)

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
