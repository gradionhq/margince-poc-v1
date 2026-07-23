// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

// The pure half of the ADR-0036 state machine: lazy expiry, the
// human-only decision gate, and the grant mapping a verdict demands.
// The Postgres-backed transitions (Decide on an expired staging, the
// redemption window) are proven in the compose integration lane, where
// timestamps can be backdated through the owner connection.

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

// Identity is only meaningful under JoinPending — without it there is no
// serialized per-identity section, so a supersede could race a plain
// insert. And because the supersede match is JSONB containment (every object
// contains {}), an empty or non-object identity would withdraw EVERY live
// pending proposal of the kind+target — both are refused before any
// transaction is opened.
func TestStageIdentityValidation(t *testing.T) {
	svc := NewService(nil)
	if _, err := svc.Stage(context.Background(), StageInput{
		Kind: "fx_rate_proposal", DiffHash: "h", Identity: json.RawMessage(`{"from_currency":"GBP"}`),
	}); err == nil || !strings.Contains(err.Error(), "JoinPending") {
		t.Fatalf("err = %v, want identity-requires-JoinPending error", err)
	}
	for _, identity := range []string{`{}`, `[]`, `"x"`, `null`} {
		if _, err := svc.Stage(context.Background(), StageInput{
			Kind: "fx_rate_proposal", DiffHash: "h", JoinPending: true, Identity: json.RawMessage(identity),
		}); err == nil || !strings.Contains(err.Error(), "non-empty JSON object") {
			t.Fatalf("identity %s: err = %v, want non-empty-object refusal", identity, err)
		}
	}
	// An identity the payload does not carry could never containment-match a
	// stored proposed_change — supersession would be silently disabled.
	if _, err := svc.Stage(context.Background(), StageInput{
		Kind: "fx_rate_proposal", DiffHash: "h", JoinPending: true,
		ProposedChange: json.RawMessage(`{"from_currency":"USD","rate":"1"}`),
		Identity:       json.RawMessage(`{"from_currency":"GBP"}`),
	}); err == nil || !strings.Contains(err.Error(), "not carried by ProposedChange") {
		t.Fatalf("mismatched identity: err = %v, want not-carried refusal", err)
	}
}

// Two spellings of one identity (key order, spacing) must canonicalize to the
// same bytes: the advisory lock hashes those bytes, so a spelling difference
// would let two stagers of one identity race past the per-identity section.
func TestCanonicalIdentityNormalizesSpelling(t *testing.T) {
	payload := json.RawMessage(`{"provider":"a","model_id":"m","rate":"1"}`)
	a, err := canonicalIdentity(json.RawMessage(`{ "model_id":"m", "provider":"a" }`), payload)
	if err != nil {
		t.Fatalf("canonicalize a: %v", err)
	}
	b, err := canonicalIdentity(json.RawMessage(`{"provider":"a","model_id":"m"}`), payload)
	if err != nil {
		t.Fatalf("canonicalize b: %v", err)
	}
	if string(a) != string(b) {
		t.Fatalf("canonical forms differ: %s vs %s", a, b)
	}
}

// A pending staging past its expiry reads as expired everywhere — there
// is no sweeper, so this fold IS the pending→expired transition; a
// decided row never flips, however stale.
func TestEffectiveStatusFoldsLazyExpiry(t *testing.T) {
	expiry := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		status string
		now    time.Time
		want   string
	}{
		{"pending before expiry stays pending", "pending", expiry.Add(-time.Minute), "pending"},
		{"pending at the exact expiry instant is still pending", "pending", expiry, "pending"},
		{"pending past expiry reads expired", "pending", expiry.Add(time.Nanosecond), "expired"},
		{"approved never expires into pending semantics", "approved", expiry.Add(48 * time.Hour), "approved"},
		{"rejected stays rejected past expiry", "rejected", expiry.Add(48 * time.Hour), "rejected"},
		{"expired stays expired", "expired", expiry.Add(-time.Hour), "expired"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := row{Status: tc.status, ExpiresAt: expiry}
			if got := a.effectiveStatus(tc.now); got != tc.want {
				t.Errorf("effectiveStatus(%s at %s) = %q, want %q", tc.status, tc.now, got, tc.want)
			}
		})
	}
}

// NewService must default the clock: a nil now would panic on the first
// expiry check in production.
func TestNewServiceDefaultsTheClock(t *testing.T) {
	svc := NewService(nil)
	if svc.now == nil {
		t.Fatal("NewService left the clock nil")
	}
	if d := time.Since(svc.now()); d < 0 || d > time.Minute {
		t.Errorf("default clock is not wall time (drift %s)", d)
	}
}

func humanCtx(perms principal.Permissions) context.Context {
	return principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:test", UserID: ids.NewV7(), Permissions: perms,
	})
}

// Deciding is human work: an agent principal — including the one that
// staged the action — is refused, and a context with no actor at all is
// an internal error, not a silent pass.
func TestHumanOnlyRefusesAgentsAndMissingActors(t *testing.T) {
	agent := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:test",
	})
	if err := humanOnly(agent); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("agent principal → %v, want ErrPermissionDenied", err)
	}
	if err := humanOnly(context.Background()); err == nil {
		t.Error("context without an actor passed the human gate")
	}
	if err := humanOnly(humanCtx(principal.Permissions{})); err != nil {
		t.Errorf("human principal refused: %v", err)
	}
}

func grants(objects map[string]principal.ObjectGrant) principal.Permissions {
	return principal.Permissions{Objects: objects}
}

// requireDecisionGrants is the fail-closed half of decidable: an unknown
// kind has no mapping and is never decidable; a known kind demands every
// grant the staged effect itself would need, with archive/share/merge
// resolving the grant from the target's entity type.
func TestRequireDecisionGrants(t *testing.T) {
	deal := "deal"
	cases := []struct {
		name    string
		a       row
		perms   principal.Permissions
		wantErr bool
		denied  bool
	}{
		{
			name:    "unknown kind fails closed",
			a:       row{Kind: "summon_demon"},
			perms:   grants(map[string]principal.ObjectGrant{"deal": {Update: true}}),
			wantErr: true,
		},
		{
			name:  "advance_deal with deal.update passes",
			a:     row{Kind: "advance_deal"},
			perms: grants(map[string]principal.ObjectGrant{"deal": {Update: true}}),
		},
		{
			name:    "advance_deal without deal.update is denied",
			a:       row{Kind: "advance_deal"},
			perms:   grants(map[string]principal.ObjectGrant{"deal": {Read: true}}),
			wantErr: true, denied: true,
		},
		{
			name:    "promote_lead needs BOTH lead.update and person.create",
			a:       row{Kind: "promote_lead"},
			perms:   grants(map[string]principal.ObjectGrant{"lead": {Update: true}}),
			wantErr: true, denied: true,
		},
		{
			name:  "archive_record resolves delete from the target type",
			a:     row{Kind: "archive_record", TargetType: &deal},
			perms: grants(map[string]principal.ObjectGrant{"deal": {Delete: true}}),
		},
		{
			name:    "archive_record without the target delete grant is denied",
			a:       row{Kind: "archive_record", TargetType: &deal},
			perms:   grants(map[string]principal.ObjectGrant{"deal": {Update: true}}),
			wantErr: true, denied: true,
		},
		{
			name:    "archive_record staged without a target type is undecidable",
			a:       row{Kind: "archive_record"},
			perms:   grants(map[string]principal.ObjectGrant{"deal": {Delete: true}}),
			wantErr: true,
		},
		{
			name:  "share_record resolves update from the target type",
			a:     row{Kind: "share_record", TargetType: &deal},
			perms: grants(map[string]principal.ObjectGrant{"deal": {Update: true}}),
		},
		{
			name:    "merge_records without a target type is undecidable",
			a:       row{Kind: "merge_records"},
			perms:   grants(map[string]principal.ObjectGrant{"deal": {Update: true}}),
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := requireDecisionGrants(principal.Principal{Permissions: tc.perms}, tc.a)
			if tc.wantErr && err == nil {
				t.Fatal("grant check passed, want refusal")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("grant check refused: %v", err)
			}
			if tc.denied && !errors.Is(err, apperrors.ErrPermissionDenied) {
				t.Errorf("missing grant → %v, want ErrPermissionDenied", err)
			}
		})
	}
}

// Every stageable kind the gate can mint must be decidable by SOMEONE:
// the grant map is consulted fail-closed, so a kind that loses its entry
// strands stagings in a queue no inbox shows.
func TestKindHasDecisionGrantsMatchesTheMap(t *testing.T) {
	if !KindHasDecisionGrants("advance_deal") {
		t.Error("advance_deal lost its decision-grant mapping")
	}
	if KindHasDecisionGrants("summon_demon") {
		t.Error("an unknown kind must not report a mapping")
	}
}

// A rate-refresh proposal targets the workspace itself and is decidable ONLY
// in the workspace it names: a foreign or absent workspace context must not see
// or decide it. This is the tenant-isolation floor for the fx_rate /
// ai_model_rate branch of targetVisible, which touches no tx (the nil tx here
// is never dereferenced) — the switch decides on the context workspace alone.
func TestRateProposalDecidableOnlyForOwningWorkspace(t *testing.T) {
	ws := ids.NewV7()
	other := ids.NewV7()
	for _, targetType := range []string{"fx_rate", "ai_model_rate"} {
		tt := targetType
		a := row{TargetType: &tt, TargetID: &ws}
		cases := []struct {
			name string
			ctx  context.Context
			want bool
		}{
			{"owning workspace", principal.WithWorkspaceID(context.Background(), ws), true},
			{"foreign workspace", principal.WithWorkspaceID(context.Background(), other), false},
			{"no workspace context", context.Background(), false},
		}
		for _, c := range cases {
			t.Run(targetType+"/"+c.name, func(t *testing.T) {
				got, err := targetVisible(c.ctx, nil, a)
				if err != nil {
					t.Fatalf("targetVisible: %v", err)
				}
				if got != c.want {
					t.Errorf("decidable = %v, want %v", got, c.want)
				}
			})
		}
	}
}
