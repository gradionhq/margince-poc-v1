// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package connector

// ChannelIdentity is one messaging-channel identity as the provider reports
// it: the resolution key a channel connector hands the CRM so an inbound
// message finds the human behind it.
//
// Provider and ChannelUserID together ARE the key. The bot (channel) id is
// deliberately absent because Telegram user ids are global rather than
// bot-scoped, so a workspace that rotates its bot keeps every binding it had.
//
// Username is display only. A Telegram handle can be released and re-claimed
// by a different human, so nothing may route, authorize or deduplicate on it.
type ChannelIdentity struct {
	Provider      string
	ChannelUserID string
	Username      string
}
