// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package telegram is the Telegram Bot API boundary the workspace-level
// channel connection is built on (telegram-oa design v2): the two connect-path
// calls (getMe, deleteWebhook), the ingress long poll (getUpdates) and the one
// send call, hand-rolled over net/http so capture takes on no new dependency.
//
// The surface is an interface (API) so the connect ordering and the poll are
// unit-tested against a fake rather than a live bot, and every non-2xx maps to one
// of this package's sentinels. Telegram's own `description` text never reaches a
// client: it rides the wrapped error, which is logged server-side, while the
// transport writes a fixed message per sentinel.
package telegram

import (
	"errors"
	"fmt"
	"strings"
)

// The package sentinels. Each names one class of outcome a caller has to tell
// apart, because each has a different answer for the operator: fix the token,
// wait and retry, reach the customer another way, or read the refusal.

// ErrTokenRejected marks a bot token Telegram refused (401, or the 404 the API
// answers for a malformed token in the path). The transport maps it to a 400
// naming the token, never echoing Telegram's text.
//
// 403 is deliberately NOT here — see ErrRecipientUnreachable.
var ErrTokenRejected = errors.New("telegram: the bot token was rejected")

// ErrRecipientUnreachable marks a chat Telegram will not deliver to: "bot was
// blocked by the user", "user is deactivated", a bot removed from a group. Every
// one of them answers 403, and every one of them is about the RECIPIENT — the
// token is live and Telegram is up.
//
// Keeping it apart from ErrTokenRejected is the whole point. A blocked bot is the
// most common send failure a channel has, and folded into the credential class it
// would tell an operator to rotate a token that works while the customer who
// blocked the bot stays unreachable either way.
var ErrRecipientUnreachable = errors.New("telegram: the recipient cannot be reached on this channel")

// ErrUnreachable marks a transport-level failure or a Telegram 5xx (DNS, TCP,
// TLS, timeout, outage) — us failing to reach TELEGRAM, which is a different
// fact from Telegram refusing to reach a recipient. The transport maps it to a
// 502; connect wrote nothing, so an operator simply retries.
var ErrUnreachable = errors.New("telegram: could not reach Telegram")

// ErrRequestRejected marks a request Telegram understood and refused on its
// own terms — a webhook URL it will not accept, a chat that blocked the bot, a
// rate limit. The token is fine and Telegram is up, so neither of the other
// two sentinels would be honest.
var ErrRequestRejected = errors.New("telegram: the request was rejected")

// ErrWebhookActive marks Telegram's 409 on getUpdates: something else already
// holds this bot's updates. Usually a registered webhook — the two ingress
// modes are mutually exclusive per bot — but Telegram answers the same status
// when a second getUpdates consumer is polling the same bot, and the response
// text is the only thing that tells the two apart. Neither is a fault: both are
// configuration facts, and both take the same remedy from the poller's side —
// clear the registration this installation can clear, poll again, and report
// the connection as broken if it repeats.
var ErrWebhookActive = errors.New("telegram: something else already holds this bot's updates, so getUpdates is refused")

// ValidateToken rejects a value that cannot be a BotFather token before it is
// spent on a network call. A token is `<bot id>:<secret>` — the numeric id is
// what makes the shape checkable, and a caller who pasted a bot *username*, a
// webhook URL, or an empty box is told so immediately instead of waiting for a
// round trip to say the same thing less clearly.
//
// It is a shape check, not an authorization: only getMe can say whether the
// token is live, which is why connect calls that first and trusts nothing here.
func ValidateToken(token string) error {
	id, secret, found := strings.Cut(strings.TrimSpace(token), ":")
	if !found || id == "" || secret == "" {
		return fmt.Errorf("a bot token looks like `<bot id>:<secret>` from BotFather: %w", ErrTokenRejected)
	}
	if strings.TrimLeft(id, "0123456789") != "" {
		return fmt.Errorf("the part before the colon must be the numeric bot id BotFather issued: %w", ErrTokenRejected)
	}
	return nil
}
