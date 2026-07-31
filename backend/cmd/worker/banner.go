// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The worker's boot banner: the one line an operator reads to see which lanes
// this process role actually came up with. It is prose about the wiring, kept
// out of main.go so startJobRunner stays the wiring itself.

import (
	"fmt"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
)

// jobRunnerBanner names, for every lane, the configuration that enabled it or
// the reason it is off.
func jobRunnerBanner(cfg workerConfig, watchCfg compose.GmailWatchConfig, modelPath compose.ModelPath, vault keyvault.Vault) string {
	gmailWired := cfg.gmailAppWired()
	providers := "imap"
	if gmailWired {
		providers += "+gmail"
	}
	if cfg.graphClientID != "" && cfg.graphClientSecret != "" {
		providers += "+graph"
	}
	captureNote := fmt.Sprintf("capture sweep every %s: %s", cfg.gmailSyncInterval, providers)
	switch {
	case gmailWired && watchCfg.Topic != "":
		captureNote = fmt.Sprintf("capture sweep every %s: %s, watch renew every %s", cfg.gmailSyncInterval, providers, cfg.gmailWatchInterval)
	case gmailWired:
		captureNote = fmt.Sprintf("capture sweep every %s: %s (watch off: no pubsub topic)", cfg.gmailSyncInterval, providers)
	}
	overlayNote := "overlay reconcile off (no keyvault configured)"
	if vault != nil {
		overlayNote = fmt.Sprintf("overlay reconcile every %s", cfg.overlayInterval)
	}
	// The Telegram poller is gated on the same vault (it unseals each bot's
	// token), and it must say so by name: a worker booted without the key
	// registers no poller at all, while an api that HAS the key still accepts
	// Connect — the connection then reads `connected` and nothing ever polls.
	// This line is the one place that split-brain is visible.
	channelNote := "telegram poll off (no keyvault: bot tokens cannot be unsealed, connected bots are NOT polled)"
	if vault != nil {
		channelNote = "telegram poll on"
	}
	deepReadNote := "deep read on"
	if modelPath.SiteExtract == nil {
		deepReadNote = "deep read degraded: no model path, queued reads will fail (configure --ai-routing)"
	}
	return fmt.Sprintf("worker running River jobs (close-date every %s, reconcile every %s, time-scan every %s, %s, %s, %s, %s)",
		cfg.closeDateInterval, cfg.reconcileInterval, cfg.timeScanInterval, captureNote, channelNote, overlayNote, deepReadNote)
}
