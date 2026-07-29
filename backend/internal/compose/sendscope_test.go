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

	"github.com/gradionhq/margince/backend/internal/modules/capture/gmail"
	"github.com/gradionhq/margince/backend/internal/modules/comms"
)

// The scope the authority gate DEMANDS must be the scope the connector
// RE-CHECKS. comms cannot import a capture provider (a module never imports a
// sibling), so SendScopeFor spells the string a second time and this is the
// only place the two can be held against each other.
func TestTheSendScopeCommsDemandsIsTheOneTheGmailConnectorRechecks(t *testing.T) {
	scope, sends := comms.SendScopeFor("gmail")
	if !sends {
		t.Fatal("comms.SendScopeFor(\"gmail\") reports gmail cannot send; every staged Gmail delivery would park")
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
