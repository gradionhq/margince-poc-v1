// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package telegram

// The seam between the ingest worker's I/O world and Normalize's pure one
// (design §6.3): BuildRawEnvelope joins the bot id the poll pinned onto the update
// with the verbatim update that poll persisted, producing the one byte record
// connector.Connector.Normalize's contract expects. Telegram is a Connector whose
// per-user Sync seam has nothing to pull (the channel poller owns its cursor), and
// deliberately never a Backfiller: the Bot API has no history endpoint, and
// Telegram itself retains unacknowledged updates only ~24h, so there is nothing to
// page backward through even if this package grew one.

import (
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// CapturedByTelegram is the provenance stamp every Telegram-captured record
// carries (connector.NormalizedRecord.CapturedBy) and the workspace-channel
// principal's audit identity (design §6.4) — spelled once so Normalize's
// output and the principal Sink.Upsert checks it against cannot drift apart.
const CapturedByTelegram = "connector:telegram"

// BuildRawEnvelope joins botID with one verbatim Telegram update into the
// connector.RawRecord Normalize maps. The bot id names the natural-key
// namespace (design §6.3: message_id is unique only within a chat, and a
// private chat's id is the Telegram user's own — shared across every bot
// that talks to them) but never rides Telegram's own update JSON, so it
// cannot come from decoding update alone; the worker supplies it here,
// having already resolved it from the connection the job's args name.
//
// update is embedded as a json.RawMessage, not decoded first — Normalize
// reads the full original, so an update kind this package's types do not
// model (my_chat_member, an edited_message) survives to a later Normalize
// extension instead of being silently dropped by a decode/re-encode
// round-trip through a narrower struct.
func BuildRawEnvelope(botID string, update []byte) (connector.RawRecord, error) {
	if botID == "" {
		return nil, fmt.Errorf("telegram: building the normalize envelope: no bot id")
	}
	raw, err := json.Marshal(ingestEnvelope{BotID: botID, Update: update})
	if err != nil {
		return nil, fmt.Errorf("telegram: building the normalize envelope: %w", err)
	}
	return connector.RawRecord(raw), nil
}
