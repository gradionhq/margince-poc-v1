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
	"maps"
	"slices"
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

// Identity values must be strings, a null field the payload omits must be
// refused, and trailing data past the object must be rejected — each edge, if
// admitted, would let a lock key and jsonb containment disagree and leave
// competing live proposals for one logical identity.
func TestCanonicalIdentityRejectsNonStringNullAndTrailingData(t *testing.T) {
	// A numeric identity value is refused: 1, 1.0 and 1e0 hash to different
	// lock keys but jsonb containment sees one number, so a numeric identity
	// could bypass the per-identity lock. Strings have no such spelling gap.
	if _, err := canonicalIdentity(
		json.RawMessage(`{"seq":1}`),
		json.RawMessage(`{"seq":1}`),
	); err == nil || !strings.Contains(err.Error(), "must be a string") {
		t.Fatalf("numeric identity: err = %v, want must-be-a-string refusal", err)
	}
	// A null field the payload omits is refused (not silently passed).
	if _, err := canonicalIdentity(
		json.RawMessage(`{"k":null}`),
		json.RawMessage(`{"other":"v"}`),
	); err == nil {
		t.Fatal("null-vs-omitted identity validated, want refusal")
	}
	// Trailing data after the object is rejected: Identity is ONE object.
	if _, err := canonicalIdentity(
		json.RawMessage(`{"from_currency":"GBP"} {"x":"y"}`),
		json.RawMessage(`{"from_currency":"GBP"}`),
	); err == nil {
		t.Fatal("trailing data after identity validated, want refusal")
	}
	// A string identity carried by the payload validates and canonicalizes.
	canonical, err := canonicalIdentity(
		json.RawMessage(`{"from_currency":"GBP"}`),
		json.RawMessage(`{"from_currency":"GBP","rate":"1.1"}`),
	)
	if err != nil {
		t.Fatalf("valid string identity: %v", err)
	}
	if string(canonical) != `{"from_currency":"GBP"}` {
		t.Fatalf("canonical = %s, want {\"from_currency\":\"GBP\"}", canonical)
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
		// The rate sheets are an upsert behind one endpoint, admitted on
		// create-or-update. The apply runs as the system principal, so this
		// check is the only grant standing between an approver and the row the
		// release replaces — it has to ask what the editor asks.
		{
			name:  "fx_rate_proposal admits an update-only approver",
			a:     row{Kind: "fx_rate_proposal"},
			perms: grants(map[string]principal.ObjectGrant{"fx_rate": {Read: true, Update: true}}),
		},
		{
			name:  "fx_rate_proposal admits a create-only approver",
			a:     row{Kind: "fx_rate_proposal"},
			perms: grants(map[string]principal.ObjectGrant{"fx_rate": {Read: true, Create: true}}),
		},
		{
			name:    "fx_rate_proposal refuses an approver holding only read",
			a:       row{Kind: "fx_rate_proposal"},
			perms:   grants(map[string]principal.ObjectGrant{"fx_rate": {Read: true}}),
			wantErr: true, denied: true,
		},
		{
			name:  "ai_model_rate_proposal admits an update-only approver",
			a:     row{Kind: "ai_model_rate_proposal"},
			perms: grants(map[string]principal.ObjectGrant{"ai_model_rate": {Read: true, Update: true}}),
		},
		{
			name:    "ai_model_rate_proposal refuses an approver holding only read",
			a:       row{Kind: "ai_model_rate_proposal"},
			perms:   grants(map[string]principal.ObjectGrant{"ai_model_rate": {Read: true}}),
			wantErr: true, denied: true,
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
// is never dereferenced) — the sheet's read grant is held throughout, so the
// workspace comparison is what each case measures.
func TestRateProposalDecidableOnlyForOwningWorkspace(t *testing.T) {
	ws := ids.NewV7()
	other := ids.NewV7()
	for _, targetType := range []string{"fx_rate", "ai_model_rate"} {
		tt := targetType
		a := row{TargetType: &tt, TargetID: &ws}
		reader := humanCtx(grants(map[string]principal.ObjectGrant{tt: {Read: true}}))
		cases := []struct {
			name string
			ctx  context.Context
			want bool
		}{
			{"owning workspace", principal.WithWorkspaceID(reader, ws), true},
			{"foreign workspace", principal.WithWorkspaceID(reader, other), false},
			{"no workspace context", reader, false},
		}
		for _, c := range cases {
			t.Run(targetType+"/"+c.name, func(t *testing.T) {
				got, err := targetVisible(c.ctx, nil, a.TargetType, a.TargetID)
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

// The four shapes a staged target can carry, and the reason each answers as it
// does. This is the rule the inbox, the single read and the decision all run on
// (decidable → targetVisible), so a shape answered wrong is either an authority
// object nobody can release or reject, or a record's proposed change disclosed
// to everyone holding the object grant.
//
// No shape here reaches a row probe, so the nil tx is never dereferenced. What a
// both-halves pair against a REAL type then shows is row-scope work and lives in
// the compose integration lane.
//
// The caller holds read on BOTH types named below, so the object-read floor
// admits every case and each answer is the shape rule's own — the floor's
// negatives are TestEveryClassifiedTargetTypeRequiresReadOnItsOwnType.
func TestTargetVisibleAnswersEachStagedShape(t *testing.T) {
	staged := tableProject
	unknown := "chartreuse"
	target := ids.NewV7()
	ctx := humanCtx(grants(map[string]principal.ObjectGrant{
		staged: {Read: true}, unknown: {Read: true},
	}))

	for _, c := range []struct {
		name       string
		targetType *string
		targetID   *ids.UUID
		want       bool
		because    string
	}{
		{
			name: "neither half", want: true,
			because: "a cold-start proposal is about no record yet, so the decision grants are the whole authority",
		},
		{
			name: "a type with no id", targetType: &staged, want: true,
			because: "a staged create has no row whose scope could bound it; its authority is the create grant on the type",
		},
		{
			name: "an id with no type", targetID: &target, want: false,
			because: "a concrete record the probe cannot resolve must not reach everyone holding the object grant",
		},
		{
			name: "both halves, a type with no probe", targetType: &unknown, targetID: &target, want: false,
			because: "a target type with no visibility rule fails closed rather than answering from the nearest primitive",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := targetVisible(ctx, nil, c.targetType, c.targetID)
			if err != nil {
				t.Fatalf("targetVisible: %v", err)
			}
			if got != c.want {
				t.Errorf("targetVisible = %v, want %v — %s", got, c.want, c.because)
			}
			// The composition layer's gate reports on the same rule. None of
			// these shapes reaches a row probe, so "decidable at all" and "visible
			// to this caller" are the same answer here, and a gate that drifted
			// from the predicate a human's inbox runs would read green over it.
			shape := ""
			if c.targetType != nil {
				shape = *c.targetType
			}
			if reported := TargetShapeDecidable(shape, c.targetID != nil); reported != c.want {
				t.Errorf("TargetShapeDecidable(%q, %v) = %v, want %v — the gate must report the rule targetVisible runs",
					shape, c.targetID != nil, reported, c.want)
			}
		})
	}
}

func TestASelfOnlyKindIsUndecidableByAnyoneButItsSubject(t *testing.T) {
	// The inbox is a SHARED surface, and for almost every kind that is the
	// point. A LinkedIn match is the exception: it names third parties out of
	// one member's imported address book, people who never agreed to be in
	// this CRM. Routing it through the shared queue without this predicate
	// handed every admin a readable copy of a colleague's contact list.
	mine := ids.NewV7()
	theirs := ids.NewV7()
	subject := ids.From[ids.UserKind](mine)
	admin := principal.Principal{
		UserID:      theirs,
		Permissions: principal.Permissions{RowScope: principal.RowScopeAll},
	}
	owner := principal.Principal{
		UserID:      mine,
		Permissions: principal.Permissions{RowScope: principal.RowScopeAll},
	}

	staged := row{Kind: "linkedin_match", OnBehalfOf: &subject}
	if requireDecisionGrants(admin, staged) == nil && !selfOnlyKinds[staged.Kind] {
		t.Fatal("the fixture is vacuous: linkedin_match is not registered as self-only")
	}
	if !selfOnlyKinds["linkedin_match"] {
		t.Fatal("linkedin_match is not self-only — a colleague's imported network is readable from the inbox")
	}

	// The predicate itself, exercised the way decidable applies it.
	selfOnly := func(p principal.Principal, a row) bool {
		if !selfOnlyKinds[a.Kind] {
			return true
		}
		return a.OnBehalfOf != nil && p.UserID != ids.Nil && a.OnBehalfOf.UUID == p.UserID
	}
	if selfOnly(admin, staged) {
		t.Error("an all-scope admin can decide a LinkedIn match staged for somebody else")
	}
	if !selfOnly(owner, staged) {
		t.Error("the member whose network produced the match cannot decide it")
	}
	// A proposal with no recorded subject is nobody's to read, not everybody's.
	if selfOnly(owner, row{Kind: "linkedin_match"}) {
		t.Error("a self-only proposal with no subject was treated as decidable")
	}
}

// DecisionGrantObjects is what the composition layer's satisfiability gate reads,
// and it is load-bearing only if it names the SAME objects requireDecisionGrants
// enforces. A gate certifying an object the decision does not demand — or blind to
// one it does — proves nothing about the row a human is asked to decide.
//
// Derived over both grant tables so a kind added to either is covered, and each
// named object is shown NECESSARY: a principal holding every other object outright
// is still refused.
func TestDecisionGrantObjectsNamesWhatTheDecisionEnforces(t *testing.T) {
	kinds := slices.Sorted(maps.Keys(decisionGrants))
	kinds = append(kinds, slices.Sorted(maps.Keys(targetResolvedGrants))...)
	if len(kinds) == 0 {
		t.Fatal("no stageable kinds — the walk covers nothing")
	}
	// A row-scoped record type every target-resolved kind can legitimately name.
	target := tableDeal

	for _, kind := range kinds {
		objects, err := DecisionGrantObjects(kind, target)
		if err != nil {
			t.Errorf("kind %q derives no decision grants: %v", kind, err)
			continue
		}
		if len(objects) == 0 {
			t.Errorf("kind %q demands no object at all — anyone could release its stagings", kind)
			continue
		}
		for _, withheld := range objects {
			perms := principal.Permissions{Objects: map[string]principal.ObjectGrant{}}
			for _, object := range objects {
				if object != withheld {
					perms.Objects[object] = principal.ObjectGrant{Create: true, Read: true, Update: true, Delete: true}
				}
			}
			err := requireDecisionGrants(principal.Principal{Permissions: perms}, row{Kind: kind, TargetType: &target})
			if !errors.Is(err, apperrors.ErrPermissionDenied) {
				t.Errorf("kind %q was decidable with no grant on %q, which DecisionGrantObjects names → %v",
					kind, withheld, err)
			}
		}
	}
}
