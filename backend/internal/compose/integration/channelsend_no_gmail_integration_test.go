// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Closes the wiring gap channelsend_integration_test.go cannot see: that suite
// always boots WITH compose.WithGmailCapture, purely to make the connect
// registry — and with it the send pre-flight — exist. On an install with no
// Google OAuth app configured at all, which is exactly a Telegram-only
// deployment and the likeliest shape for hand-testing this feature, that
// registry used to never get built and the pre-flight was absent entirely: a
// reply on a channel the workspace bound no bot for was accepted at request
// time and parked later, instead of refused with the actionable 422 the
// pre-flight exists to produce.
//
// This file composes the server with compose.WithKeyvault ALONE — no
// WithGmailCapture, no WithGraphCapture — and proves both branches of the
// channel pre-flight are live anyway: refuse with no bot bound, admit with
// one. Reverting the fix under test (moving the pre-flight wiring back inside
// WithGmailCapture alone) makes TestSendMessageRefusesWithNoGoogleAppConfiguredAndNoBotBound
// fail, because the reply would then be accepted with no live pre-flight to
// refuse it.

import (
	"context"
	"crypto/rand"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
)

// setupChannelSendNoGmail lays down the same fixture setupChannelSend
// (channelsend_integration_test.go) does — a person, their channel identity,
// the inbound activity, consent — but boots the composition without any
// Google app configured, so the ONLY thing wiring the connect registry (and
// the pre-flight over it) is compose.WithKeyvault.
func setupChannelSendNoGmail(t *testing.T) *channelSendEnv {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("generating a test root key: %v", err)
	}
	vault, err := keyvault.New(keyvault.Config{RootKey: key, Pool: preflightAppPool(t)})
	if err != nil {
		t.Fatalf("building the local vault: %v", err)
	}
	e := setupWithOptions(t, compose.WithKeyvault(vault))
	e.slug = "channel-send-no-gmail-e2e"
	bootstrapWorkspaceSession(t, e, "Channel Send No-Gmail E2E", "rep@fable.test", "Admin")

	c := &channelSendEnv{env: e}
	var person struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/people", anyMap{"full_name": "Telegram Buyer"}, nil, &person); status != http.StatusCreated {
		t.Fatalf("create person → %d", status)
	}
	c.personID = person.ID
	if err := e.inWorkspace(t, e.slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT workspace_id, id FROM app_user WHERE email = $1`, "rep@fable.test").Scan(&c.ws, &c.user)
	}); err != nil {
		t.Fatalf("resolving the acting human: %v", err)
	}
	c.bindIdentity(t)
	c.seedInboundMessage(t)
	c.grantConsent(t, "transactional")
	return c
}

// This is the test that fails if the fix regresses: no Google app configured
// (no WithGmailCapture, no WithGraphCapture) and no bot bound for telegram, so
// the reply must be refused at request time with the same actionable 422 the
// Gmail-configured suite gets — not accepted and left to park at transmission
// where only an operator would notice.
func TestSendMessageRefusesWithNoGoogleAppConfiguredAndNoBotBound(t *testing.T) {
	c := setupChannelSendNoGmail(t)
	// No connectBot call: this workspace has bound no bot for telegram.

	status, code, detail := c.sendReply(t, "transactional", nil)

	if status != http.StatusUnprocessableEntity || code != "channel_not_send_capable" {
		t.Fatalf("reply with no Google app configured and no bot bound → %d %q, want 422 channel_not_send_capable", status, code)
	}
	if !strings.Contains(detail, "admin") {
		t.Fatalf("refusal detail %q does not say who has to fix it", detail)
	}
	if n := c.stagedChannelDeliveries(t); n != 0 {
		t.Fatalf("%d deliveries staged behind a refused reply with no Google app configured, want 0", n)
	}
}

// The pre-flight's other branch on the same Gmail-less composition: a bot IS
// bound, so the reply is admitted. Without this case the refusal above would
// prove nothing — it could just as well be the whole send path failing with no
// Google app configured, not the pre-flight correctly answering "no bot".
func TestSendMessageAcceptsWithNoGoogleAppConfiguredButABotBound(t *testing.T) {
	c := setupChannelSendNoGmail(t)
	c.connectBot(t)

	status, code, detail := c.sendReply(t, "transactional", nil)

	if status != http.StatusAccepted {
		t.Fatalf("reply with a bot bound and no Google app configured → %d %q (%s), want 202", status, code, detail)
	}
	if n := c.stagedChannelDeliveries(t); n != 1 {
		t.Fatalf("%d channel deliveries staged behind an accepted reply, want 1", n)
	}
}
