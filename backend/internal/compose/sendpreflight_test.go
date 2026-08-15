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
	"github.com/gradionhq/margince/backend/pkg/extension"
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

// sendCapableCase is one pre-flight question and the answer it must give.
type sendCapableCase struct {
	name     string
	ctx      context.Context
	provider string
	grants   *stubGrants
	want     bool
	wantErr  error
	asks     int
	// appMissing is the DEPLOYMENT fact: this installation configured no
	// mail app for the provider, whatever the user's own grant says.
	appMissing bool
}

// sendCapableCases is every branch the pre-flight has. Some decide whether a
// user is handed an actionable 422 or a 202 for a message that can only park;
// the rest are the honest hard cases — a principal with no mailbox to ask
// about, a lookup that could not answer, and a deployment with no app to
// transmit through. human is the acting principal the cases that need one share.
func sendCapableCases(human context.Context) []sendCapableCase {
	const sendScope = "https://www.googleapis.com/auth/gmail.send"
	lookupDown := errors.New("connection reset by peer")
	return []sendCapableCase{
		{
			"the grant holds the send scope", human, "gmail",
			&stubGrants{granted: []string{"https://www.googleapis.com/auth/gmail.readonly", sendScope}}, true, nil, 1, false,
		},
		{
			"a read-only grant cannot send", human, "gmail",
			&stubGrants{granted: []string{"https://www.googleapis.com/auth/gmail.readonly"}}, false, nil, 1, false,
		},
		{
			"no connection is a fact, not a fault", human, "gmail",
			&stubGrants{err: capture.ErrNoConnection}, false, nil, 1, false,
		},
		{
			"a lookup that cannot answer must not answer", human, "gmail",
			&stubGrants{err: lookupDown}, false, lookupDown, 1, false,
		},
		// Sending is a human act, so a principal with no app_user identity has
		// no mailbox to pre-flight — and the lookup is never even asked.
		{"a principal with no user identity", context.Background(), "gmail", &stubGrants{}, false, nil, 0, false},
		// A provider that cannot transmit at all has no send scope to hold;
		// asking capture about it would be asking the wrong question.
		{
			"a provider that cannot send at all", human, "imap",
			&stubGrants{granted: []string{sendScope}}, false, nil, 0, false,
		},
		// A bot token carries no OAuth scope, so the per-user grant lookup has
		// nothing to say about it: the workspace binding is the whole answer,
		// and asking the mailbox table is what would refuse every reply.
		{
			"a bound bot sends without any scope", human, "telegram",
			&stubGrants{channelBound: true}, true, nil, 0, false,
		},
		{
			"no bot bound is a fact, not a fault", human, "telegram",
			&stubGrants{}, false, nil, 0, false,
		},
		{
			"a channel lookup that cannot answer must not answer", human, "telegram",
			&stubGrants{channelErr: lookupDown}, false, lookupDown, 0, false,
		},
		// The grant is the user's; the app that transmits it is the
		// deployment's. A mailbox connected before the app was removed — or
		// against an app this role's operator never configured — still reports
		// the send scope, and every send under it can only park. The grant is
		// not even asked: no answer it could give would change this one.
		{
			"a granted scope with no configured app cannot send", human, "gmail",
			&stubGrants{granted: []string{sendScope}}, false, nil, 0, true,
		},
		// The channel half must not be caught by the mail app's absence: a bot
		// is bound in a different table by a different flow, and a Telegram-only
		// deployment configures no mail app at all.
		{
			"a bound bot sends on a deployment with no mail app", human, "telegram",
			&stubGrants{channelBound: true}, true, nil, 0, true,
		},
	}
}

func TestMailboxAuthoritySendCapableBranches(t *testing.T) {
	human := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:x", UserID: ids.NewV7(),
	})

	for _, tc := range sendCapableCases(human) {
		t.Run(tc.name, func(t *testing.T) {
			authority := mailboxAuthority{
				grants:            tc.grants,
				mailAppConfigured: func(string) bool { return !tc.appMissing },
			}

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

// The UNIT arm, which is a different table question again — or rather, no table
// question at all.
//
// This is the defect DESIGN-SP5 §8.1 warns about and the slice-2 plan missed: a
// unit-supplied transport falling through to the channel arm asks
// ChannelSendCapable, which reads channel_connection — the workspace-bot binding
// a unit never writes. Every unit reply was then refused at the composer, and
// the refusal a rep reads is "ask an admin to bind a bot", about a bot that has
// nothing to do with the transport they are replying on.
//
// So the assertion that matters most here is the NEGATIVE one: the workspace
// lookup must not be asked at all.
func TestSendCapableAsksTheUnitRatherThanTheWorkspaceBotTable(t *testing.T) {
	human := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:x", UserID: ids.NewV7(),
	})
	unreachable := errors.New("the provider timed out")

	for name, tc := range map[string]struct {
		channel extension.Channel
		want    bool
		wantErr error
	}{
		"a live connection can send": {
			channel: extension.Channel{Provider: "mine_chat", Send: (&capturedSend{}).send, Live: answersLive(true, nil)},
			want:    true,
		},
		"a member who disconnected cannot": {
			channel: extension.Channel{Provider: "mine_chat", Send: (&capturedSend{}).send, Live: answersLive(false, nil)},
		},
		"a capture-only transport cannot": {
			channel: extension.Channel{Provider: "mine_chat"},
		},
		// A pre-flight that cannot ask must not answer — the mail arm's own
		// rule, applied here so a rep is never told at the composer something
		// the transmission will contradict.
		"a unit that could not answer reports the fault": {
			channel: extension.Channel{Provider: "mine_chat", Send: (&capturedSend{}).send, Live: answersLive(false, unreachable)},
			wantErr: unreachable,
		},
	} {
		t.Run(name, func(t *testing.T) {
			declaresTransport(t, "mine", tc.channel)
			grants := &stubGrants{channelBound: true}
			authority := mailboxAuthority{grants: grants, mailAppConfigured: func(string) bool { return false }}

			got, err := authority.SendCapable(human, "mine_chat")

			if got != tc.want {
				t.Errorf("SendCapable = %v, want %v", got, tc.want)
			}
			switch {
			case tc.wantErr != nil && !errors.Is(err, tc.wantErr):
				t.Errorf("SendCapable error = %v, want it to match %v", err, tc.wantErr)
			case tc.wantErr == nil && err != nil:
				t.Errorf("SendCapable error = %v, want none", err)
			}
			// The stub is wired to answer "a bot IS bound", so a fall-through
			// would make even the refusal cases pass for the wrong reason.
			if grants.channelCalls != 0 {
				t.Errorf("the workspace bot table was read %d time(s) for a unit transport; a unit never writes it, and asking it is what refused every unit reply", grants.channelCalls)
			}
		})
	}
}
