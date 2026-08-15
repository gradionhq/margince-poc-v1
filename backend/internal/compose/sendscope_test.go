// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The Gmail send scope is asked for, demanded, and re-checked in three
// different packages, and only compose imports all of them. Drift between them
// fails SILENTLY: every send parks with "this mailbox connection was not
// granted the send scope", which reads as a user who declined consent rather
// than as a typo, and no amount of reconnecting repairs it.

import (
	"slices"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/capture/gmail"
	"github.com/gradionhq/margince/backend/internal/modules/capture/telegram"
	"github.com/gradionhq/margince/backend/internal/modules/comms"
)

// The scope the authority gate DEMANDS must be the scope the connector
// RE-CHECKS. comms cannot import a capture provider (a module never imports a
// sibling), so SendScopeFor spells the string a second time and this is the
// only place the two can be held against each other.
func TestTheSendScopeCommsDemandsIsTheOneTheGmailConnectorRechecks(t *testing.T) {
	scope, capability := comms.SendScopeFor("gmail")
	if capability != comms.SendsWithScope {
		t.Fatalf("comms.SendScopeFor(\"gmail\") = %v, want SendsWithScope; a Gmail grant that is never scope-checked, or a delivery that always parks", capability)
	}
	if scope != gmail.SendScope {
		t.Errorf("comms demands %q, the gmail connector re-checks %q — every send would park as ungranted", scope, gmail.SendScope)
	}
}

// The OAuth consent must REQUEST what the gate demands. Google will not add a
// scope to an existing refresh token, so a connection consented without this
// one can never be repaired short of reconnecting the mailbox.
func TestTheGmailConsentRequestsTheScopeTheSendPathDemands(t *testing.T) {
	scope, _ := comms.SendScopeFor("gmail")
	if !slices.Contains(gmailScopes, scope) {
		t.Errorf("the Gmail consent requests %v, which does not include the send scope %q the dispatcher demands", gmailScopes, scope)
	}
}

// The channel provider comms answers for must be the one capture connects. The
// same silent-drift argument as the scope above, in the other direction: comms
// spells "telegram" a second time because it may not import capture, and a
// misspelling here reads a live bot as capture-only — every reply parks with
// "provider cannot send messages", which is a connector limitation that does not
// exist.
func TestTheChannelProviderCommsCanSendForIsTheOneCaptureConnects(t *testing.T) {
	scope, capability := comms.SendScopeFor(capture.ProviderTelegram)
	if capability != comms.SendsWithoutScope {
		t.Fatalf("comms.SendScopeFor(%q) = %v, want SendsWithoutScope", capture.ProviderTelegram, capability)
	}
	if scope != "" {
		t.Errorf("a bot token has no OAuth grant to intersect, yet comms demands scope %q of it", scope)
	}
}

// The THIRD spelling of the same provider name, and the same silent drift —
// restated across the axis split (ADR-0107/A158). Capture files a Telegram
// update under kind=message with channel_provider=telegram, and the reply
// operation reads BOTH: the kind to know it is a conversation, the provider to
// know what can carry the answer. Drift in either half refuses every Telegram
// reply with a 422 about the wrong record — nothing parks, nothing logs, and no
// mail test notices.
func TestTheChannelKindTheReplyPathAnswersIsTheOneCaptureFilesUnder(t *testing.T) {
	if !activities.IsChannelKind(activities.KindMessage) {
		t.Errorf("activities does not recognise %q as a channel conversation; every channel reply would be refused as the wrong kind of anchor",
			activities.KindMessage)
	}
	// The transport half: capture's provider name has to be one the send path
	// will act on, which is the drift this test has always been about.
	if !activities.CanSendOnProvider(capture.ProviderTelegram) {
		t.Errorf("activities cannot send on %q, the provider capture files Telegram messages under; every Telegram reply would be refused as uncarriable",
			capture.ProviderTelegram)
	}
	// The FOURTH spelling, and the one the narrowing created: capture's
	// connector restates the message kind rather than importing it (a capture
	// connector must not reach across to a sibling module), so the two literals
	// are held against each other here. Drift files every captured message under
	// a kind the reply path does not answer — and, since the kind FKs into
	// activity_kind, would fail the write outright.
	if telegram.KindMessage != activities.KindMessage {
		t.Errorf("capture files channel messages under kind %q and the reply path answers %q; every captured message would be unrepliable",
			telegram.KindMessage, activities.KindMessage)
	}
	// And it is not a blanket yes: mail has its own send path, and admitting it
	// here would route a mail reply through a transport with no address to send
	// to.
	if activities.IsChannelKind("email") {
		t.Error("activities treats mail as a messaging channel; a mail anchor would resolve a channel recipient it has none of")
	}
}
