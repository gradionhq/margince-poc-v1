// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

// An approval nobody has answered yet is not a bad token, and the difference is
// the whole 🟡 loop: an agent told its token is INVALID discards it and stages
// the same question again, which is how one action collects four approvals and
// none of them is ever spent.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// A retry that lands before the human clicks answers "still needs the approval
// you already hold" — the retryable sentinel, naming the same id — and never the
// token-invalid one a caller can only respond to by asking again.
func TestRedeemingAnUndecidedApprovalIsRetryableAndNotAnInvalidToken(t *testing.T) {
	now := time.Date(2026, 8, 18, 5, 7, 53, 0, time.UTC)
	pending := row{
		ID: ids.ApprovalID{UUID: ids.NewV7()}, Kind: "enrich", Status: statusPending,
		DiffHash: "c276f789", ExpiresAt: now.Add(72 * time.Hour),
	}
	err := validateRedemption(pending, principal.Principal{}, "enrich", "c276f789", now)
	if !errors.Is(err, apperrors.ErrRequiresApproval) {
		t.Fatalf("redeeming an undecided approval → %v, want ErrRequiresApproval", err)
	}
	if errors.Is(err, apperrors.ErrApprovalTokenInvalid) {
		t.Fatalf("redeeming an undecided approval → %v, must not read as an invalid token", err)
	}
	// The message has to leave the agent holding the SAME id: without it the
	// only recovery it can infer is to stage the question again.
	if !strings.Contains(err.Error(), pending.ID.String()) {
		t.Fatalf("refusal %q does not name the approval still awaiting a decision", err)
	}
	// "Retry this exact call" is only true advice for a caller whose call IS the
	// staged one. A mismatched tool or hash against a pending row is a bad token,
	// not a call to retry — telling it to wait would send it back for a decision
	// that could never release the call it is actually making.
	for name, mismatch := range map[string]struct{ tool, hash string }{
		"another tool":   {"deepread", "c276f789"},
		"another change": {"enrich", "20be9e19"},
	} {
		err := validateRedemption(pending, principal.Principal{}, mismatch.tool, mismatch.hash, now)
		if !errors.Is(err, apperrors.ErrApprovalTokenInvalid) {
			t.Fatalf("%s against a pending approval → %v, want ErrApprovalTokenInvalid", name, err)
		}
		if errors.Is(err, apperrors.ErrRequiresApproval) {
			t.Fatalf("%s against a pending approval → %v, must not invite a retry", name, err)
		}
	}
}

// Every OTHER refusal stays token-invalid. A pending row past its expiry is one
// of them: nothing will decide it, so telling the caller to wait would be a
// promise the sweep has already broken.
func TestEveryRedemptionRefusalOtherThanUndecidedReadsAsAnInvalidToken(t *testing.T) {
	now := time.Date(2026, 8, 18, 5, 7, 53, 0, time.UTC)
	decided := now.Add(-time.Minute)
	consumed := now.Add(-30 * time.Second)
	approved := row{
		ID: ids.ApprovalID{UUID: ids.NewV7()}, Kind: "enrich", Status: approvalStatusApproved,
		DiffHash: "c276f789", ExpiresAt: now.Add(72 * time.Hour), DecidedAt: &decided,
	}
	passport := ids.NewV7()
	other := ids.From[ids.PassportKind](ids.NewV7())
	withPassport := principal.Principal{PassportID: passport}

	staleDecision := approved
	staleDecision.DecidedAt = ptr(now.Add(-RedemptionWindow - time.Minute))
	spent := approved
	spent.ConsumedAt = &consumed
	rejected := approved
	rejected.Status = approvalStatusRejected
	lapsed := approved
	lapsed.Status = statusPending
	lapsed.ExpiresAt = now.Add(-time.Hour)
	foreign := approved
	foreign.PassportID = &other

	for name, tc := range map[string]struct {
		a    row
		p    principal.Principal
		tool string
		hash string
	}{
		"rejected":            {rejected, principal.Principal{}, "enrich", "c276f789"},
		"pending past expiry": {lapsed, principal.Principal{}, "enrich", "c276f789"},
		"already redeemed":    {spent, principal.Principal{}, "enrich", "c276f789"},
		"decision too old":    {staleDecision, principal.Principal{}, "enrich", "c276f789"},
		"another tool":        {approved, principal.Principal{}, "deepread", "c276f789"},
		"another change":      {approved, principal.Principal{}, "enrich", "20be9e19"},
		"unbound passport":    {approved, withPassport, "enrich", "c276f789"},
		"another passport":    {foreign, withPassport, "enrich", "c276f789"},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateRedemption(tc.a, tc.p, tc.tool, tc.hash, now)
			if !errors.Is(err, apperrors.ErrApprovalTokenInvalid) {
				t.Fatalf("%s → %v, want ErrApprovalTokenInvalid", name, err)
			}
			if errors.Is(err, apperrors.ErrRequiresApproval) {
				t.Fatalf("%s → %v, must not invite a retry of the same token", name, err)
			}
		})
	}
}

// An approved token bound to this exact call is redeemable, which is what makes
// the refusals above refusals of something.
func TestRedeemingTheApprovedCallItWasStagedForPasses(t *testing.T) {
	now := time.Date(2026, 8, 18, 5, 7, 53, 0, time.UTC)
	decided := now.Add(-time.Minute)
	passport := ids.NewV7()
	bound := ids.From[ids.PassportKind](passport)
	approved := row{
		ID: ids.ApprovalID{UUID: ids.NewV7()}, Kind: "enrich", Status: approvalStatusApproved,
		DiffHash: "c276f789", ExpiresAt: now.Add(72 * time.Hour), DecidedAt: &decided,
		PassportID: &bound,
	}
	if err := validateRedemption(approved, principal.Principal{PassportID: passport}, "enrich", "c276f789", now); err != nil {
		t.Fatalf("redeeming the approved call → %v, want it admitted", err)
	}
}

// A gate staging carries no logical identity — the call's identity is its diff
// hash — and a caller that supplies one is told so rather than having it dropped
// on the floor, which would leave it believing supersession was happening.
func TestStagingAnAgentCallRefusesALogicalIdentityAndAnUnboundActor(t *testing.T) {
	svc := NewService(nil)
	_, _, err := svc.StageAgentCall(context.Background(), StageInput{
		Kind: "enrich", DiffHash: "c276f789", Identity: json.RawMessage(`{"url":"https://stainzer.at"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "takes no Identity") {
		t.Fatalf("staging an agent call with an Identity → %v, want it refused", err)
	}
	// No actor at all: there is nobody for the staging to record as proposer and
	// nobody whose credential the probe could scope to.
	_, _, err = svc.StageAgentCall(context.Background(), StageInput{Kind: "enrich", DiffHash: "c276f789"})
	if err == nil || !strings.Contains(err.Error(), "no actor") {
		t.Fatalf("staging an agent call with no actor → %v, want it refused", err)
	}
}
