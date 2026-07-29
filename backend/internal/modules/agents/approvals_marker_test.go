// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The released marker is proof a human released THIS call, and the seam's
// external-egress backstop acts on it. RedeemAndMark exists so the proof cannot
// be minted without the redemption; this pins the half that matters — a
// redemption that fails marks nothing, so a caller that ignored the error still
// carries an unreleased context.

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// refusingApprovals redeems nothing: the case a caller must not be able to
// turn into a released context by ignoring the error.
type refusingApprovals struct{}

var errRedeemRefused = errors.New("approval already consumed")

func (refusingApprovals) Stage(context.Context, StageRequest) (ids.ApprovalID, error) {
	return ids.ApprovalID{}, errRedeemRefused
}

func (refusingApprovals) Redeem(context.Context, ids.ApprovalID, string, string) (int64, bool, error) {
	return 0, false, errRedeemRefused
}

func TestRedeemAndMarkLeavesTheContextUnreleasedWhenRedemptionFails(t *testing.T) {
	ctx, version, pinned, err := RedeemAndMark(context.Background(), refusingApprovals{},
		ids.New[ids.ApprovalKind](), "update_record", "hash")

	if !errors.Is(err, errRedeemRefused) {
		t.Fatalf("err = %v, want the redemption failure", err)
	}
	if ApprovalRedeemed(ctx) {
		t.Error("a failed redemption returned a released context — the marker would authorize an unapproved write")
	}
	if version != 0 || pinned {
		t.Errorf("version=%d pinned=%v, want the zero pin on failure", version, pinned)
	}
}

func TestRedeemAndMarkReleasesOnlyAfterASuccessfulRedeem(t *testing.T) {
	ctx, _, _, err := RedeemAndMark(context.Background(), &recordingApprovals{},
		ids.New[ids.ApprovalKind](), "update_record", "hash")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ApprovalRedeemed(ctx) {
		t.Error("a successful redemption did not release the context — an approved write would be refused")
	}
}
