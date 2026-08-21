// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package relayprobe

// The two provider calls a REPLY makes: resolve the conversation with a party,
// then post into it.
//
// Split from client.go — which holds the connection, the egress guard and the
// transport plumbing every call shares — because these two are where the
// difference between what is STORED and what is ADDRESSED is decided, and that
// argument reads better beside the send than buried among the poll's reads.

import (
	"context"
	"fmt"
)

// sendMessage transmits one message into a channel and returns the provider's
// own id for it.
//
// The recipient is a CHANNEL, resolved from the party's account id by
// dmChannelWith — see there for why the account id is what the binding stores.
func (c *client) sendMessage(ctx context.Context, channelSlug, body string) (string, error) {
	var sent struct {
		ID string `json:"id"`
	}
	if err := c.post(ctx, "/api/channels/"+channelSlug+"/messages",
		map[string]string{"content": body}, &sent); err != nil {
		return "", err
	}
	// An accepted send that returns no id is not a failure: the message is
	// gone either way, and reporting an error would have the core retry a
	// delivery the recipient has already received. What is lost is the anchor
	// for a later reply, which the caller records as absent rather than faked.
	return sent.ID, nil
}

// dmChannelWith resolves the 1:1 conversation with one account, and it exists
// because WHAT IS STORED and WHAT IS ADDRESSED are different things.
//
// The core binds a party by `(provider, channel_user_id)` — one row per human,
// and that row is what an erasure suppresses and what a reply resolves. So the
// account id is what the ingress records. But this provider addresses a message
// by CHANNEL, so the send resolves the conversation from the account here,
// against the provider's own answer, rather than storing a conversation id in
// the place a person's identity belongs. Storing the slug instead would key the
// person on a conversation: the same colleague messaging two members would
// become two people, and an erasure armed for one of them would leave the other
// capturable.
//
// It returns the DM and nothing else. The endpoint is documented not to return
// group DMs, and an empty answer is an error rather than a silent no-op: a send
// with nowhere to go must refuse, not succeed quietly.
func (c *client) dmChannelWith(ctx context.Context, account string) (string, error) {
	var answer struct {
		Channels []struct {
			Slug string `json:"slug"`
			Type string `json:"type"`
		} `json:"channels"`
	}
	if err := c.post(ctx, "/api/dms/with-users",
		map[string][]string{"user_ids": {account}}, &answer); err != nil {
		return "", err
	}
	for _, channel := range answer.Channels {
		if channel.Type == "dm" && channel.Slug != "" {
			return channel.Slug, nil
		}
	}
	return "", fmt.Errorf("%w: no direct conversation with that account", errProvider)
}
