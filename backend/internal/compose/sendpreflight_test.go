// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The pre-flight's branches, one table. Each row decides whether a sender is
// handed an actionable 422 or a 202 for a message that can only park — and which
// TABLE was consulted to decide it, which is the half a mail-only suite cannot
// see going wrong.

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/comms"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

type stubGrants struct {
	granted []string
	err     error
	calls   int
	// channelBound is what the WORKSPACE lookup answers, and channelCalls counts
	// it separately: the two lookups read different tables, and a pre-flight
	// that asked the wrong one is exactly the defect that refused every channel
	// reply while every mail test still passed.
	channelBound bool
	channelErr   error
	channelCalls int
}

func (s *stubGrants) GrantedScopesFor(context.Context, ids.UserID, string) ([]string, error) {
	s.calls++
	return s.granted, s.err
}

func (s *stubGrants) ChannelSendCapable(context.Context, string) (bool, error) {
	s.channelCalls++
	return s.channelBound, s.channelErr
}

// The pre-flight's four branches. Two of them decide whether a user is handed
// an actionable 422 or a 202 for a message that can only park; the other two
// are the honest hard cases — a principal with no mailbox to ask about, and a
// lookup that could not answer at all.
func TestMailboxAuthoritySendCapableBranches(t *testing.T) {
	const sendScope = "https://www.googleapis.com/auth/gmail.send"
	lookupDown := errors.New("connection reset by peer")
	human := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:x", UserID: ids.NewV7(),
	})

	for _, tc := range []struct {
		name     string
		ctx      context.Context
		provider string
		grants   *stubGrants
		want     bool
		wantErr  error
		asks     int
	}{
		{
			"the grant holds the send scope", human, "gmail",
			&stubGrants{granted: []string{"https://www.googleapis.com/auth/gmail.readonly", sendScope}}, true, nil, 1,
		},
		{
			"a read-only grant cannot send", human, "gmail",
			&stubGrants{granted: []string{"https://www.googleapis.com/auth/gmail.readonly"}}, false, nil, 1,
		},
		{
			"no connection is a fact, not a fault", human, "gmail",
			&stubGrants{err: capture.ErrNoConnection}, false, nil, 1,
		},
		{
			"a lookup that cannot answer must not answer", human, "gmail",
			&stubGrants{err: lookupDown}, false, lookupDown, 1,
		},
		// Sending is a human act, so a principal with no app_user identity has
		// no mailbox to pre-flight — and the lookup is never even asked.
		{"a principal with no user identity", context.Background(), "gmail", &stubGrants{}, false, nil, 0},
		// A provider that cannot transmit at all has no send scope to hold;
		// asking capture about it would be asking the wrong question.
		{
			"a provider that cannot send at all", human, "imap",
			&stubGrants{granted: []string{sendScope}}, false, nil, 0,
		},
		// A bot token carries no OAuth scope, so the per-user grant lookup has
		// nothing to say about it: the workspace binding is the whole answer,
		// and asking the mailbox table is what would refuse every reply.
		{
			"a bound bot sends without any scope", human, "telegram",
			&stubGrants{channelBound: true}, true, nil, 0,
		},
		{
			"no bot bound is a fact, not a fault", human, "telegram",
			&stubGrants{}, false, nil, 0,
		},
		{
			"a channel lookup that cannot answer must not answer", human, "telegram",
			&stubGrants{channelErr: lookupDown}, false, lookupDown, 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authority := mailboxAuthority{grants: tc.grants}

			got, err := authority.SendCapable(tc.ctx, tc.provider)

			if got != tc.want {
				t.Fatalf("SendCapable = %v, want %v", got, tc.want)
			}
			switch {
			case tc.wantErr != nil && !errors.Is(err, tc.wantErr):
				t.Fatalf("SendCapable error = %v, want it to match %v", err, tc.wantErr)
			case tc.wantErr == nil && err != nil:
				t.Fatalf("SendCapable error = %v, want none", err)
			}
			assertLookupsAsked(t, tc.grants, tc.provider, tc.asks, tc.ctx == human)
		})
	}
}

// assertLookupsAsked pins WHICH table the pre-flight read. The two lookups are
// mutually exclusive — a scoped provider asks the per-user grant, a bot asks the
// workspace binding — and an answer produced from the other one is the defect
// that refuses every channel reply while every mail case still passes.
//
// The channel expectation is DERIVED from the capability class rather than
// listed per case, so a provider whose class changes cannot leave a stale
// hand-written count agreeing with the wrong table.
func assertLookupsAsked(t *testing.T, grants *stubGrants, provider string, wantGrantCalls int, actorPresent bool) {
	t.Helper()
	if grants.calls != wantGrantCalls {
		t.Fatalf("the grant lookup was asked %d time(s), want %d", grants.calls, wantGrantCalls)
	}
	_, capability := comms.SendScopeFor(provider)
	wantChannelCalls := 0
	if capability == comms.SendsWithoutScope && actorPresent {
		wantChannelCalls = 1
	}
	if grants.channelCalls != wantChannelCalls {
		t.Fatalf("the channel-binding lookup was asked %d time(s), want %d", grants.channelCalls, wantChannelCalls)
	}
}
