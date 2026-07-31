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

// chatTypePrivate is the ONLY chat type this connector captures: design §1
// puts group chats out of scope, and the exclusion is load-bearing twice
// over. A bot in a group runs in Telegram's default privacy mode and is shown
// only commands, so a group conversation arrives as fragments of itself. And a
// reply resolves its recipient through the sender's channel identity — which
// for Telegram is the sender's PRIVATE chat — so a message filed under a group
// thread would be answered somewhere else entirely, or refused outright by a
// user who never started the bot. Telegram answers group messages under the
// same bare `message` update this connector subscribes to, so the refusal has to
// happen here.
const chatTypePrivate = "private"

// telegramChat is the `chat` object: the id is half of the chat-scoped natural
// key (design §6.3's load-bearing rule that message_id repeats across chats),
// Type is the scope gate above, and Username is the counterpart's handle —
// present because for a private chat the chat IS that user, which is what lets
// a membership update read an identity straight out of it (membership.go).
type telegramChat struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Username string `json:"username"`
}

// isPrivate reports whether this chat is the 1:1 bot conversation. A missing
// or unrecognized type is NOT private: the scope exclusion fails closed, so an
// update shape this package cannot place is skipped rather than captured
// against a conversation nobody can reply to.
//
// The id has to agree with the label, because the label is only the payload's
// claim about itself. Telegram numbers a private chat POSITIVE and a
// supergroup or channel negative, so a negative id under a `private` type is a
// group — precisely the chat this connector excludes, and the one a reply must
// never be published into.
//
// Zero is no chat at all, and it matters most on the membership path, which
// reads the customer's account OUT of the chat: a zero id there would be
// written as a reachability state against "telegram:0", an account every such
// update would share. Refusing it here is why that path needs no guard of its
// own.
func (c telegramChat) isPrivate() bool { return c.Type == chatTypePrivate && c.ID > 0 }

// telegramMedia is the attachment set Telegram delivers in place of `text`.
// Every field is decoded for PRESENCE only, never for its schema: naming the
// kind is all a wordless message's body needs, and modelling file ids would be
// the media-download path this feature excludes.
type telegramMedia struct {
	Animation json.RawMessage `json:"animation"`
	Photo     json.RawMessage `json:"photo"`
	Sticker   json.RawMessage `json:"sticker"`
	Voice     json.RawMessage `json:"voice"`
	VideoNote json.RawMessage `json:"video_note"`
	Video     json.RawMessage `json:"video"`
	Audio     json.RawMessage `json:"audio"`
	Document  json.RawMessage `json:"document"`
	Location  json.RawMessage `json:"location"`
	Contact   json.RawMessage `json:"contact"`
}

// telegramMessage is the one update kind this system captures as an
// activity. Date is Telegram's unix-seconds send time; Caption is where
// Telegram puts the words of a message whose payload is media, embedded
// alongside the media set so one struct decodes the whole message.
type telegramMessage struct {
	MessageID int64        `json:"message_id"`
	Chat      telegramChat `json:"chat"`
	From      telegramUser `json:"from"`
	Date      int64        `json:"date"`
	Text      string       `json:"text"`
	Caption   string       `json:"caption"`
	telegramMedia
}

// telegramUpdate is Telegram's own update envelope. Message is a pointer
// because the other update kind this connector subscribes to (my_chat_member,
// the block/unblock signal) carries none; a nil Message is how Normalize tells
// "not a message" from "a message with no text".
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
// activity record it captures. The ingest worker classifies a my_chat_member
// update via ParseMembership and handles it directly (design §4.2 D9) before
// Normalize ever sees it, so a nil Message reaching here means an update kind
// neither function parses (an edited_message, say) — a deliberate exclusion,
// ErrSkip, never an error.
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
	if !msg.Chat.isPrivate() {
		return nil, fmt.Errorf("telegram: update %d is in a %q chat, not a private one: %w",
			update.UpdateID, msg.Chat.Type, connector.ErrSkip)
	}
	// A Telegram account id is POSITIVE. A message Telegram delivers with no
	// `from` at all — an anonymous group admin posts under `sender_chat`
	// instead — decodes to sender id 0, which is not an account: it is a valid,
	// non-empty key that every such sender shares, so they would collapse onto
	// ONE Person, one identity row, and one conversation reading as reachable at
	// chat 0. A negative id is not an account either — that is how Telegram
	// numbers chats, not users. The identity is minted a few lines below, so the
	// rule about what counts as an account belongs here — the chat-scope gate
	// above narrows which update shapes arrive, it does not decide whether the
	// sender is addressable.
	if msg.From.ID <= 0 {
		return nil, fmt.Errorf("telegram: update %d carries no addressable sender: %w",
			update.UpdateID, connector.ErrSkip)
	}
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
			Body:       messageBody(msg),
			OccurredAt: time.Unix(msg.Date, 0).UTC(),
			Direction:  connector.DirectionInbound,
		},
		Source:     Provider + ":" + naturalID,
		CapturedBy: CapturedByTelegram,
		// Raw is deliberately EMPTY, unlike every mail connector's record. The poll
		// that read this update already persisted it as the only-copy evidence row,
		// in the same transaction that acknowledged it, keyed on the per-bot
		// update_id; handing the same bytes to the Sink would store the message a
		// SECOND time under a chat-scoped key with the opposite conflict rule — an
		// append-once evidence table half of whose Telegram rows are rewritable, an
		// unbounded second copy of the largest column, and a subject access request
		// that hands the human every message twice. The erasure purge and the SAR
		// section both match raw_capture by JSONB path, so they reach the poll's row
		// without this one.
		Counterparty: connector.Counterparty{
			// No outbound echo (design §6.4): a bot has no companion app a
			// human types into, so every update this connector ever reads
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

// messageBody is what the timeline shows for one message. Telegram carries a
// media message's words in `caption`, never in `text`, so reading `text` alone
// files a photo-with-a-caption as an activity with an empty body — the rep sees
// the Person and a blank line where the customer's sentence was.
//
// A message with no words at all reads as a bracketed placeholder naming what
// arrived, deliberately NOT ErrSkip: the customer did reach out, and skipping
// leaves a timeline that says nothing arrived while the reply box offers to
// answer it — the same silent gap an empty body is. The words are all this
// connector can show (fetching the media itself is out of scope). The
// verbatim update is not carried here: the raw_capture row the poll wrote is its
// single owner.
func messageBody(msg *telegramMessage) string {
	if msg.Text != "" {
		return msg.Text
	}
	if msg.Caption != "" {
		return msg.Caption
	}
	return "[" + msg.mediaKind() + "]"
}

// mediaKind names the attachment a wordless message carries. Animation is
// tested first because Telegram sends a GIF as an animation AND a document,
// and the animation is the truer description of the two.
func (m telegramMedia) mediaKind() string {
	switch {
	case m.Animation != nil:
		return "animation"
	case m.Photo != nil:
		return "photo"
	case m.Sticker != nil:
		return "sticker"
	case m.Voice != nil:
		return "voice message"
	case m.VideoNote != nil:
		return "video note"
	case m.Video != nil:
		return "video"
	case m.Audio != nil:
		return "audio"
	case m.Document != nil:
		return "document"
	case m.Location != nil:
		return "location"
	case m.Contact != nil:
		return "contact"
	}
	// Telegram keeps adding message kinds (a poll, a venue, a shared story). One
	// this package cannot name is still a customer reaching out, so it reads as
	// an unnamed attachment rather than as an empty activity.
	return "attachment"
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
