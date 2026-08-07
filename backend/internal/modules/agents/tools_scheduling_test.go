// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// What book_meeting stages, and what it refuses to stage. Split from
// tools_comms_test.go alongside the source it covers: a booking anchors on no
// row, so everything here is about the records it ATTACHES to — which is a
// different subject from the mail and channel sends next door.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// A booking with no links is refused, at BOTH doors.
//
// crm.yaml's bookMeeting body requires `links`, and this surface advertises it
// as required with minItems 1 — but an InputSchema on the MCP surface is
// documentation, never validation, so the rule holds only where the code puts
// it. It used to stage instead: an approval with no target, which is a human
// asked to release a meeting attached to nothing, with no record to show them
// and no version to pin it against.
//
// The execute door is covered too, because it is reached with an approval
// already redeemed — a call that never passed staging would otherwise book.
func TestABookingThatNamesNoRecordIsRefusedAtBothDoors(t *testing.T) {
	const noLinks = `{"start":"2026-08-03T09:00:00Z","end":"2026-08-03T09:30:00Z"}`
	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{"staging", sendCtx()},
		{"after an approval was redeemed", withApprovalRedeemed(sendCtx(), 0, false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			approvals := &recordingApprovals{}
			comms := &recordingComms{}
			registry := NewRegistry(approvals, auth.NewGate(fullSeatAuthority{}))
			RegisterCommsTools(registry, comms, &multiLinkProvider{})

			_, err := registry.Invoke(tc.ctx, "book_meeting", json.RawMessage(noLinks))

			var badArgs *BadArgsError
			if !errors.As(err, &badArgs) {
				t.Fatalf("Invoke err = %v, want a BadArgsError naming `links`", err)
			}
			if !strings.Contains(err.Error(), "`links`") {
				t.Errorf("err = %v, want it to name the field", err)
			}
			if len(approvals.staged) != 0 {
				t.Errorf("staged %d approvals, want none — nothing was approvable", len(approvals.staged))
			}
			if comms.booked != nil {
				t.Error("a booking attached to nothing reached the comms seam")
			}
		})
	}
}

// Every link is checked, not just the one the inbox displays. A booking that
// mixes a local deal with a mirrored organization is exactly what a
// first-link-only guard would wave through into an approval nobody could
// release.
func TestABookingRefusesAMirroredLinkBehindALocalOne(t *testing.T) {
	local, mirrored := ids.NewV7(), ids.NewV7()
	p := &multiLinkProvider{heldElsewhere: map[ids.UUID]bool{mirrored: true}}

	_, err := bookMeetingTool{comms: &recordingComms{}, p: p}.StageInfo(context.Background(),
		json.RawMessage(fmt.Sprintf(
			`{"start":"2026-08-03T09:00:00Z","end":"2026-08-03T09:30:00Z","links":[{"entity_type":"deal","entity_id":%q},{"entity_type":"organization","entity_id":%q}]}`,
			local, mirrored)))

	if !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Fatalf("StageInfo err = %v, want ErrUnsupportedBySoR — the second link was never validated", err)
	}
	if len(p.read) != 2 {
		t.Errorf("read %d links, want both", len(p.read))
	}
}

// Each link costs its own row-scoped read in its own transaction, and the array
// is chosen freely by the caller — at the body limit one tools/call could carry
// thousands, spending tens of thousands of queries against a 16-connection pool
// before any human has approved anything, because staging runs on the refusal
// path. Deduplicating matters as much as the cap: the same id repeated is the
// cheapest way to turn one call into N reads.
func TestABookingIsBoundedAndDeduplicatedBeforeItReadsAnything(t *testing.T) {
	one := ids.NewV7()
	slot := `"start":"2026-08-10T09:00:00Z","end":"2026-08-10T09:30:00Z"`

	t.Run("refuses more links than a meeting could mean", func(t *testing.T) {
		links := make([]string, maxBookingLinks+1)
		for i := range links {
			links[i] = fmt.Sprintf(`{"entity_type":"deal","entity_id":%q}`, ids.NewV7())
		}
		p := &multiLinkProvider{}
		_, err := bookMeetingTool{comms: &recordingComms{}, p: p}.StageInfo(context.Background(),
			json.RawMessage(fmt.Sprintf(`{%s,"links":[%s]}`, slot, strings.Join(links, ","))))

		var bad *BadArgsError
		if !errors.As(err, &bad) {
			t.Fatalf("StageInfo err = %v, want a BadArgsError refusing the oversized link array", err)
		}
		if len(p.read) != 0 {
			t.Errorf("read %d records before refusing — the cap must precede the reads it exists to prevent", len(p.read))
		}
	})

	t.Run("reads a repeated link once", func(t *testing.T) {
		repeated := make([]string, 10)
		for i := range repeated {
			repeated[i] = fmt.Sprintf(`{"entity_type":"deal","entity_id":%q}`, one)
		}
		p := &multiLinkProvider{}
		if _, err := (bookMeetingTool{comms: &recordingComms{}, p: p}).StageInfo(context.Background(),
			json.RawMessage(fmt.Sprintf(`{%s,"links":[%s]}`, slot, strings.Join(repeated, ",")))); err != nil {
			t.Fatalf("StageInfo err = %v", err)
		}
		if len(p.read) != 1 {
			t.Errorf("read %d times for one distinct link, want 1", len(p.read))
		}
	})
}
