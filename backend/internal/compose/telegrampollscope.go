// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Which polled updates may be stored at all, and under what key and lock. It is
// the admission half of telegrampoll.go, split out because it is one concept and
// because it is the half with no database in it: the classification is a pure
// function precisely so that WHERE it happens — before any transaction opens — is
// assertable rather than a comment.

package compose

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/capture/telegram"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
)

// telegramPolledUpdate is one update the poller has classified in memory and will
// persist: its Telegram number, its verbatim bytes, and the accounts it is about.
type telegramPolledUpdate struct {
	updateID int64
	raw      json.RawMessage
	accounts []string
}

// telegramPollRefusal names one update the poller will not persist, and why. err
// carries the decode fault when there was one, so a refusal is diagnosable
// without the payload being logged.
type telegramPollRefusal struct {
	updateID int64
	because  string
	err      error
}

// classifyTelegramBatch decides, for every update in a batch and BEFORE any
// transaction opens, which of them may be stored at all.
//
// Classifying HERE rather than in the worker that normalizes the payload later is
// load-bearing, not tidiness. A group chat refused downstream has already left a
// verbatim raw_capture row holding the sender's numeric id, handle, names and
// full message text — and no Person, erasure, SAR or retention lane can reach it,
// because every one of them drives off person_channel_identity, which only a
// captured record ever creates. Refusing before the insert is the only point at
// which that data can be kept out.
//
// A refusal never stops the batch, and the cursor still advances past any refused
// update it could NUMBER. That is a deliberate trade (design v2 §6): one poison
// update that blocked the cursor would silently stop ALL further ingress for that
// bot, which is a far larger loss than the one update dropped here. An update
// carrying no number is the one exception, and it is not a choice — there is no
// value to acknowledge it with, so it is refused and logged on every poll until
// Telegram's own retention drops it.
func classifyTelegramBatch(batch []json.RawMessage) ([]telegramPolledUpdate, []telegramPollRefusal) {
	keep := make([]telegramPolledUpdate, 0, len(batch))
	var refused []telegramPollRefusal
	for _, raw := range batch {
		updateID, numbered := telegram.UpdateIDOf(raw)
		if !numbered {
			refused = append(refused, telegramPollRefusal{
				because: "the update carries no usable update_id, so it could never be acknowledged",
			})
			continue
		}
		accounts, err := telegram.InScopeSubjects(raw)
		if err != nil {
			refused = append(refused, telegramPollRefusal{
				updateID: updateID, because: "the update could not be read", err: err,
			})
			continue
		}
		if len(accounts) == 0 {
			refused = append(refused, telegramPollRefusal{
				updateID: updateID,
				because:  "no account this connector captures: a group chat, an anonymous sender, or an update kind outside the subscription",
			})
			continue
		}
		keep = append(keep, telegramPolledUpdate{updateID: updateID, raw: raw, accounts: accounts})
	}
	return keep, refused
}

// polledBatchAccounts is every account the kept updates are about — the key set
// the identity lock must cover for the whole transaction.
func polledBatchAccounts(updates []telegramPolledUpdate) []string {
	accounts := make([]string, 0, len(updates))
	for _, update := range updates {
		accounts = append(accounts, update.accounts...)
	}
	return accounts
}

// telegramAnySubjectSuppressed reports whether any account this update is about
// carries an erasure suppression entry. It answers UNDER the identity lock its
// caller took, never on its own: a bare probe-then-insert would let a whole
// erasure commit between the two statements, and the row written afterwards would
// hold the erased human's id, handle, names and message text with nothing left to
// reach it by — the suppression guarantees person_channel_identity is never
// recreated, and every lane that could find that row drives off exactly those
// rows.
func telegramAnySubjectSuppressed(ctx context.Context, tx pgx.Tx, accounts []string) (bool, error) {
	for _, account := range accounts {
		suppressed, err := storekit.ChannelIdentitySuppressed(ctx, tx, telegram.Provider, account)
		if err != nil {
			return false, fmt.Errorf("telegram_poll: probing the erasure suppression list: %w", err)
		}
		if suppressed {
			return true, nil
		}
	}
	return false, nil
}

// telegramRawSourceID is raw_capture's dedupe key for one polled update.
// update_id is a PER-BOT sequence, so the key MUST carry the bot:
// raw_capture_source_unique is (workspace_id, source_system, source_id) and
// InsertRawCaptureTx's ON CONFLICT overwrites the stored payload, so a bare
// update_id would let the bot a workspace was REPLACED onto land on the outgoing
// bot's row and destroy the only copy of a message already captured —
// unrecoverable, because Telegram has no history API to re-fetch it from.
//
// The bot id and not the connection id: the counter being namespaced is the BOT's,
// so a bot disconnected and reconnected under a fresh connection id still dedupes
// its own re-deliveries against what it delivered before.
func telegramRawSourceID(botID string, updateID int64) string {
	return fmt.Sprintf("%s:%d", botID, updateID)
}

// telegramLockKeys names an update's subjects as the eraser names them, so the two
// sides of the race take the identical locks.
func telegramLockKeys(accounts []string) []storekit.ChannelIdentityKey {
	keys := make([]storekit.ChannelIdentityKey, 0, len(accounts))
	for _, account := range accounts {
		keys = append(keys, storekit.ChannelIdentityKey{Provider: telegram.Provider, ChannelUserID: account})
	}
	return keys
}
