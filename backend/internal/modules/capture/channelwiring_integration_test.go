// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package capture_test

// What the channel surface does on a deployment that is missing a piece of its
// own wiring. Kept apart from the connect suite because the subject is not the
// connect ORDER at all: it is whether an operator is told which deployment fact
// to fix, or handed an opaque fault and a server log they may not have.

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
)

// A deployment with no keyvault cannot seal a bot token or destroy one, and
// that is a DEPLOYMENT FACT the operator can act on — not an internal fault. It
// has to reach them by name on all three lifecycle paths, the same way the
// missing public address does: an opaque 500 sends whoever is looking at the
// screen to read a server log they may not have.
func TestAVaultLessDeploymentRefusesConnectByName(t *testing.T) {
	api := newFakeTelegram()
	token, _ := api.withNewBot("vaultless_bot")
	replacement, _ := api.withNewBot("vaultless_replacement")
	f := newChannelFixture(t, api)

	// Connected while a vault was still configured, so the lifecycle paths have
	// a live row to reach and the refusal is about the wiring, not the row.
	conn, err := f.store.Connect(f.ctx, capture.ConnectRequest{
		Provider: capture.ProviderTelegram, BotToken: token,
	})
	if err != nil {
		t.Fatalf("seeding a live connection: %v", err)
	}
	f.withoutVault(t)

	for name, call := range map[string]func() error{
		"connect": func() error {
			_, err := f.store.Connect(f.ctx, capture.ConnectRequest{
				Provider: capture.ProviderTelegram, BotToken: replacement,
			})
			return err
		},
		"rotate":     func() error { return f.store.ReplaceToken(f.ctx, conn.ID, replacement) },
		"disconnect": func() error { return f.store.Disconnect(f.ctx, conn.ID) },
	} {
		t.Run(name, func(t *testing.T) {
			err := call()
			if !errors.Is(err, capture.ErrChannelWiringIncomplete) {
				t.Fatalf("%s on a vault-less deployment: got %v, want ErrChannelWiringIncomplete", name, err)
			}
		})
	}

	// The wire is where this matters: the sentence naming the missing keyvault
	// must reach the caller, not only the server log.
	rec, req := f.connectRequest(
		`{"provider":"telegram","botToken":"` + replacement + `"}`)
	f.handlers.ConnectChannel(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "channel_credentials_not_configured") {
		t.Errorf("the 503 does not carry the actionable code: %s", rec.Body.String())
	}
	// And it must not leak how the credential store is built.
	if strings.Contains(rec.Body.String(), "keyvault") {
		t.Errorf("the refusal names an internal component to the caller: %s", rec.Body.String())
	}
}
