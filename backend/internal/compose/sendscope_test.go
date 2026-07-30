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

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/capture/gmail"
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
