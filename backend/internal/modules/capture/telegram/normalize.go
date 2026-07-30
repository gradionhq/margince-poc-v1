// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package telegram

// The pure Telegram → activity mapping (design §6.3): no provider handle, no
// I/O — Normalize maps the envelope connector.go's BuildRawEnvelope built
// (the bot id joined with one verbatim Telegram update) onto a
// provenance-stamped NormalizedRecord. This is the test-guarded surface a
// table-driven fixture proves without a live bot or a database, mirroring
// mailmap.ToRecord's role for the mail connectors.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// ActivityFields is the pure Telegram-side mirror of capture.ActivityFields
// (Kind/Body/OccurredAt/Direction) — a duplicate shape, not an oversight:
// package capture already imports capture/telegram (channelconn.go's Bot API
// client for Connect), so this package importing capture back would cycle.
// The ingest worker (compose, which legitimately imports both) converts this
// 1:1 into capture.ActivityFields immediately before handing the record to
// the Sink, which is the one place that type actually has to exist.
type ActivityFields struct {
	Kind       string
	Body       string
	OccurredAt time.Time
	Direction  string
}

// Provider is the source_system / NaturalKey namespace every Telegram record
// carries — capture.ProviderTelegram's value, restated here for the same
// import-cycle reason ActivityFields is: the two packages cannot reference
// one spelling directly, so keep them equal if either ever changes.
const Provider = "telegram"

// telegramUser is the `from` object of a Telegram message: the sender's
// identity as Telegram reports it.
type telegramUser struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

// telegramChat is the `chat` object — only the id matters here: it is half
// of the chat-scoped natural key (design §6.3's load-bearing rule that
// message_id repeats across chats).
type telegramChat struct {
	ID int64 `json:"id"`
}

// telegramMessage is the one update kind this system captures as an
// activity. Date is Telegram's unix-seconds send time.
type telegramMessage struct {
	MessageID int64        `json:"message_id"`
	Chat      telegramChat `json:"chat"`
	From      telegramUser `json:"from"`
	Date      int64        `json:"date"`
	Text      string       `json:"text"`
}

// telegramUpdate is Telegram's own update envelope. Message is a pointer
// because most other update kinds this webhook subscribes to
// (my_chat_member — design §6.6/Task 11's block-unblock signal) carry none;
// a nil Message is how Normalize tells "not a message" from "a message with
// no text".
type telegramUpdate struct {
	UpdateID int64            `json:"update_id"`
	Message  *telegramMessage `json:"message"`
}

// ingestEnvelope is the raw record Normalize actually maps: the connected
// bot's numeric id alongside Telegram's own update JSON, verbatim. Update
// stays a json.RawMessage rather than a decoded telegramUpdate so an update
// kind this package does not parse into a typed field (my_chat_member's own
// payload, an edited_message) survives untouched for a later Normalize
// extension to read — decoding and re-encoding here would silently drop it.
type ingestEnvelope struct {
	BotID  string          `json:"bot_id"`
	Update json.RawMessage `json:"update"`
}

// Normalize maps one raw envelope (connector.go's BuildRawEnvelope) to the
// activity record it captures. A my_chat_member (or any other non-message)
// update is a deliberate exclusion — ErrSkip, never an error — because
// Task 11 owns that signal and this pass has nothing to do with it yet.
func Normalize(_ context.Context, raw connector.RawRecord) ([]connector.NormalizedRecord, error) {
	var env ingestEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("telegram: decoding the ingest envelope: %w", err)
	}
	if env.BotID == "" {
		return nil, fmt.Errorf("telegram: normalize envelope carries no bot id")
	}

	var update telegramUpdate
	if err := json.Unmarshal(env.Update, &update); err != nil {
		return nil, fmt.Errorf("telegram: decoding the update: %w", err)
	}
	if update.Message == nil {
		return nil, fmt.Errorf("telegram: update %d carries no message: %w", update.UpdateID, connector.ErrSkip)
	}

	msg := update.Message
	chatID := fmt.Sprintf("%d", msg.Chat.ID)
	// The natural key is chat-scoped and that is load-bearing (design §6.3):
	// Telegram's message_id is unique only WITHIN a chat, and a private
	// chat's id is the user's own id — shared across every bot that human
	// talks to — so omitting the bot would collide two different customers'
	// (or two different bots') conversations into one activity.
	naturalID := fmt.Sprintf("%s:%s:%d", env.BotID, chatID, msg.MessageID)

	return []connector.NormalizedRecord{{
		EntityType: datasource.EntityActivity,
		NaturalKey: connector.NaturalKey{SourceSystem: Provider, SourceID: naturalID},
		Fields: ActivityFields{
			Kind:       Provider,
			Body:       msg.Text,
			OccurredAt: time.Unix(msg.Date, 0).UTC(),
			Direction:  connector.DirectionInbound,
		},
		Source:     Provider + ":" + naturalID,
		CapturedBy: CapturedByTelegram,
		Raw:        env.Update,
		Counterparty: connector.Counterparty{
			// No outbound echo (design §6.4): a bot has no companion app a
			// human types into, so every update this webhook ever delivers
			// originates with the human, never with us.
			Direction:   connector.DirectionInbound,
			DisplayName: telegramDisplayName(msg.From),
			ChannelIdentity: connector.ChannelIdentity{
				Provider:      Provider,
				ChannelUserID: fmt.Sprintf("%d", msg.From.ID),
				Username:      msg.From.Username,
			},
		},
		// The conversation IS the chat for a channel (connector.go's amended
		// ThreadKey comment): design §6.3's thread_key spelling.
		ThreadKey: fmt.Sprintf("%s:%s:%s", Provider, env.BotID, chatID),
	}}, nil
}

// telegramDisplayName renders the sender's name from Telegram's separate
// first/last name fields, falling back to the @username Telegram guarantees
// every account has. Untrusted text, exactly like a mail header's display
// name — a human typed it, and nothing here sanitizes it.
func telegramDisplayName(u telegramUser) string {
	name := strings.TrimSpace(strings.TrimSpace(u.FirstName) + " " + strings.TrimSpace(u.LastName))
	if name != "" {
		return name
	}
	return u.Username
}
