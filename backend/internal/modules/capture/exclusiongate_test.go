// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/capture/exclusion"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// fakeRules is a stand-in exclusion loader: it records which user its
// rules were requested for and returns a fixed set.
type fakeRules struct {
	rules       []exclusion.Rule
	askedForUID ids.UUID
	calls       int
}

func (f *fakeRules) RulesFor(_ context.Context, userID ids.UUID) ([]exclusion.Rule, error) {
	f.calls++
	f.askedForUID = userID
	return f.rules, nil
}

func connectorCtxOnBehalfOf(user ids.UUID) context.Context {
	return principal.WithActor(context.Background(), principal.Principal{
		Type:       principal.PrincipalConnector,
		ID:         "connector:gmail",
		OnBehalfOf: user,
		UserID:     user,
	})
}

func mailRecord(attrs connector.ExclusionAttrs) connector.NormalizedRecord {
	return connector.NormalizedRecord{
		EntityType: "activity",
		NaturalKey: connector.NaturalKey{SourceSystem: "gmail", SourceID: "msg-9"},
		Fields:     ActivityFields{Kind: "email"},
		CapturedBy: "connector:gmail",
		Match:      attrs,
	}
}

func TestExcluded_matchesAgainstTheCallersRules(t *testing.T) {
	uid := ids.NewV7()
	fake := &fakeRules{rules: []exclusion.Rule{{Kind: "sender_domain", Value: "personal-family.example"}}}
	sink := NewSink(nil).WithExclusions(fake)

	rule, excluded, err := sink.excluded(connectorCtxOnBehalfOf(uid),
		mailRecord(connector.ExclusionAttrs{SenderDomain: "personal-family.example"}))
	if err != nil {
		t.Fatalf("excluded: %v", err)
	}
	if !excluded {
		t.Fatal("a record whose sender domain matches a rule must be excluded")
	}
	if rule.Kind != "sender_domain" {
		t.Errorf("matched rule = %+v, want sender_domain", rule)
	}
	if fake.askedForUID != uid {
		t.Errorf("rules loaded for %v, want the on-behalf-of user %v", fake.askedForUID, uid)
	}
}

func TestExcluded_nonMatchingRecordPasses(t *testing.T) {
	fake := &fakeRules{rules: []exclusion.Rule{{Kind: "sender_domain", Value: "personal-family.example"}}}
	sink := NewSink(nil).WithExclusions(fake)
	if _, excluded, err := sink.excluded(connectorCtxOnBehalfOf(ids.NewV7()),
		mailRecord(connector.ExclusionAttrs{SenderDomain: "work.example"})); err != nil || excluded {
		t.Fatalf("a non-matching record must pass: excluded=%v err=%v", excluded, err)
	}
}

func TestExcluded_noAttrsSkipsTheLoadEntirely(t *testing.T) {
	// A lead / non-mail record carries no match attributes; the gate must
	// not even query the rule set for it.
	fake := &fakeRules{rules: []exclusion.Rule{{Kind: "sender_domain", Value: "x.example"}}}
	sink := NewSink(nil).WithExclusions(fake)
	rec := connector.NormalizedRecord{
		EntityType: "lead",
		NaturalKey: connector.NaturalKey{SourceSystem: "apollo", SourceID: "a-1"},
		Fields:     LeadFields{Email: "someone@x.example"},
	}
	if _, excluded, err := sink.excluded(connectorCtxOnBehalfOf(ids.NewV7()), rec); err != nil || excluded {
		t.Fatalf("no-attrs record must pass: excluded=%v err=%v", excluded, err)
	}
	if fake.calls != 0 {
		t.Errorf("rule set was queried %d times for a no-attrs record, want 0", fake.calls)
	}
}

func TestExcluded_failsClosedWhenNoCapturingUser(t *testing.T) {
	// A capture connector always acts for a granting human (RC-8). If the
	// principal carries no effective user, the gate must refuse rather than
	// load rules for the nil user and let personal mail through unexcluded.
	fake := &fakeRules{rules: []exclusion.Rule{{Kind: "sender_domain", Value: "personal-family.example"}}}
	sink := NewSink(nil).WithExclusions(fake)
	ctx := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalConnector, ID: "connector:gmail",
	})
	if _, _, err := sink.excluded(ctx, mailRecord(connector.ExclusionAttrs{SenderDomain: "personal-family.example"})); err == nil {
		t.Fatal("gate must fail closed when the connector has no capturing user")
	}
	if fake.calls != 0 {
		t.Errorf("rules loaded for a nil user (%d calls); must fail before RulesFor", fake.calls)
	}
}

func TestExcludedWithoutLoaderIsANoOp(t *testing.T) {
	// A Sink with no exclusions loader wired (e.g. a role that never
	// configured it) must never block a write.
	sink := NewSink(nil)
	if _, excluded, err := sink.excluded(connectorCtxOnBehalfOf(ids.NewV7()),
		mailRecord(connector.ExclusionAttrs{SenderDomain: "personal-family.example"})); err != nil || excluded {
		t.Fatalf("no loader must be a no-op: excluded=%v err=%v", excluded, err)
	}
}
