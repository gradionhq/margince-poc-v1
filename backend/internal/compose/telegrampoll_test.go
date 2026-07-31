// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The poller's decisions that hold without a database: which of a batch's
// updates may be stored at all, and the two timings that would otherwise make
// polling silently fruitless.
//
// The classification is deliberately a pure function so it can be asserted here,
// because WHERE it happens is the point: an update refused after the insert has
// already left a verbatim payload nothing can reach. What that transaction
// actually commits is a claim only a real one can support — see
// integration/telegrampoll_integration_test.go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/capture/telegram"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// quietTestLogger discards the poller's own log lines: these cases assert on the
// disposition it returned and on what it asked the provider, never on log output.
func quietTestLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// polledBatch renders a batch as GetUpdates hands one over: raw envelopes,
// nothing pre-decoded.
func polledBatch(updates ...string) []json.RawMessage {
	batch := make([]json.RawMessage, 0, len(updates))
	for _, u := range updates {
		batch = append(batch, json.RawMessage(u))
	}
	return batch
}

// privateMessageUpdate is the shape the poller stores: a private chat, a named
// sender, a positive update_id.
const privateMessageUpdate = `{"update_id":11,"message":{"message_id":1,` +
	`"chat":{"id":880001,"type":"private"},"from":{"id":880001,"username":"buyer"},` +
	`"date":1785000000,"text":"hello"}}`

// groupMessageUpdate is a message in a group chat — out of scope by design §1,
// and the case whose refusal MUST happen before any row is written.
const groupMessageUpdate = `{"update_id":12,"message":{"message_id":2,` +
	`"chat":{"id":-100880002,"type":"group"},"from":{"id":880003,"username":"member"},` +
	`"date":1785000001,"text":"group chatter"}}`

// poisonUpdate is well-formed JSON that Telegram's own schema does not describe:
// `chat` is a string where an object belongs, so the subject read fails while the
// update_id stays perfectly legible. That combination is what makes it poison
// rather than garbage — it can be numbered, and therefore acknowledged.
const poisonUpdate = `{"update_id":13,"message":{"message_id":3,"chat":"not-an-object"}}`

// unnumberedUpdate carries no update_id, so nothing this side could ever
// acknowledge it by.
const unnumberedUpdate = `{"message":{"message_id":4,"chat":{"id":880004,"type":"private"},"from":{"id":880004}}}`

// A group chat is refused BEFORE the transaction opens. Refusing it later — in
// the worker that normalizes the payload — leaves behind a verbatim raw_capture
// row holding the sender's id, handle, names and message text that no Person,
// erasure, SAR or retention lane can reach, because every one of them drives off
// person_channel_identity and a refused record creates none. That is strictly
// worse than not filtering at all, which is why this classification is a pure
// function taken before any row is written.
func TestAPollRefusesAGroupChatBeforeAnythingIsPersisted(t *testing.T) {
	keep, refused := classifyTelegramBatch(polledBatch(groupMessageUpdate))

	if len(keep) != 0 {
		t.Fatalf("kept %d updates from a group-chat batch, want 0 — a stored group update is an orphan no lifecycle lane can reach", len(keep))
	}
	if len(refused) != 1 || refused[0].updateID != 12 {
		t.Fatalf("refusals = %+v, want exactly one naming update 12", refused)
	}
}

// A poison update is dropped and the rest of the batch is kept. The cursor still
// advances past it (Work uses the batch's highest number regardless), which is
// the deliberate trade: one update lost, rather than every future update for this
// bot blocked behind a cursor that can never move.
func TestAPollDropsAPoisonUpdateAndKeepsTheRestOfTheBatch(t *testing.T) {
	keep, refused := classifyTelegramBatch(polledBatch(privateMessageUpdate, poisonUpdate))

	if len(keep) != 1 || keep[0].updateID != 11 {
		t.Fatalf("kept %+v, want only update 11 — a poison update must not take the batch around it down", keep)
	}
	if len(refused) != 1 || refused[0].updateID != 13 {
		t.Fatalf("refusals = %+v, want exactly one naming update 13", refused)
	}
	if refused[0].err == nil {
		t.Error("the refusal carries no error, so the only record of WHY an update was dropped says nothing diagnosable")
	}
}

// An update carrying no number is refused too, and refused without inventing one:
// acknowledging a number nobody read would tell Telegram to forget an update this
// installation never saw.
func TestAPollRefusesAnUpdateItCannotNumber(t *testing.T) {
	keep, refused := classifyTelegramBatch(polledBatch(unnumberedUpdate))

	if len(keep) != 0 {
		t.Fatalf("kept %d unnumbered updates, want 0", len(keep))
	}
	if len(refused) != 1 || refused[0].updateID != 0 {
		t.Fatalf("refusals = %+v, want one refusal claiming no update number at all", refused)
	}
}

// The kept updates carry the accounts the transaction must lock, and the lock is
// taken for the WHOLE batch as its first statement — so the account set has to be
// derivable before the loop that writes anything.
func TestAKeptUpdateCarriesTheAccountTheLockMustCover(t *testing.T) {
	keep, _ := classifyTelegramBatch(polledBatch(privateMessageUpdate))

	accounts := polledBatchAccounts(keep)
	if len(accounts) != 1 || accounts[0] != "880001" {
		t.Fatalf("accounts = %v, want the sender's own id — the erasure locks and probes by exactly this value", accounts)
	}
}

// A poll job's timeout must outlast the long poll it runs, INCLUDING the headroom
// the client adds for Telegram's answer to travel. Cancelled by its own timeout, a
// poll returns nothing, so its offset never advances and River retries it
// forever without the connection ever making progress — a bot that looks
// connected and silently receives nothing.
func TestTheTelegramPollJobTimeoutExceedsItsLongPoll(t *testing.T) {
	budget := telegram.LongPollBudget(telegramPollTimeoutSeconds)
	if telegramPollJobTimeout <= budget {
		t.Fatalf("the poll job times out after %s but its request budget is %s — every poll would be cancelled before Telegram answered",
			telegramPollJobTimeout, budget)
	}
}

// clearRecordingAPI is the provider boundary for the failure arms: it records
// every webhook clear and answers a bare re-ask, because these cases are about
// what the poller DOES with a refusal, not about a batch.
type clearRecordingAPI struct {
	telegram.API
	cleared []string
	// reaskTimeouts is the interval each getUpdates was asked to hold for. The
	// re-ask after a clear must name ZERO: a second long poll would spend the job's
	// remaining budget learning what an immediate ask answers.
	reaskTimeouts []int
}

func (a *clearRecordingAPI) DeleteWebhook(_ context.Context, token string) error {
	a.cleared = append(a.cleared, token)
	return nil
}

func (a *clearRecordingAPI) GetUpdates(_ context.Context, _ string, _ int64, timeoutSeconds int, _ []string) ([]json.RawMessage, int64, error) {
	a.reaskTimeouts = append(a.reaskTimeouts, timeoutSeconds)
	return nil, 0, nil
}

// A 409 is answered by clearing the one cause this installation can clear and then
// ESTABLISHING whether that was it — an immediate re-ask, not a guess from a retry
// counter. Returning nil instead would report a bot nobody is polling as healthy.
func TestAConflictClearsTheWebhookThenRe_asksToEstablishTheCause(t *testing.T) {
	api := &clearRecordingAPI{}
	w := newTelegramPollWorker(nil, nil, api, nil, quietTestLogger())

	err := w.answerPollFailure(context.Background(), capture.ChannelPollTarget{ID: ids.NewV7()},
		"1:x", fmt.Errorf("telegram: getUpdates: %w", telegram.ErrWebhookActive))

	if err == nil {
		t.Fatal("the conflict was swallowed — a bot nothing is polling would read as healthy")
	}
	if !errors.Is(err, telegram.ErrWebhookActive) {
		t.Errorf("got %v, want the conflict to survive into the returned error so the retry is diagnosable", err)
	}
	if len(api.cleared) != 1 || api.cleared[0] != "1:x" {
		t.Fatalf("deleteWebhook calls %v, want exactly one for this bot's token — clearing it is the only thing that repairs a 409", api.cleared)
	}
	if len(api.reaskTimeouts) != 1 || api.reaskTimeouts[0] != 0 {
		t.Fatalf("the re-ask asked Telegram to hold for %v, want a single ask of 0 — a second long poll would spend the job's whole budget on it", api.reaskTimeouts)
	}
}

// A 429 waits exactly as long as Telegram asked. Substituting a ladder of our own
// on top of the interval it named earns a harder limit, and the cursor has not
// moved, so waiting costs nothing.
func TestAThrottledPollWaitsTheIntervalTelegramNamed(t *testing.T) {
	api := &clearRecordingAPI{}
	w := newTelegramPollWorker(nil, nil, api, nil, quietTestLogger())
	const retryAfter = 42 * time.Second

	err := w.answerPollFailure(context.Background(), capture.ChannelPollTarget{ID: ids.NewV7()}, "1:x",
		errors.Join(telegram.ErrRequestRejected, &connector.RateLimitedError{RetryAfter: retryAfter}))

	var snooze *river.JobSnoozeError
	if !errors.As(err, &snooze) {
		t.Fatalf("a throttled poll returned %v, want a River snooze — anything else spends a retry rung on a request Telegram already refused", err)
	}
	if snooze.Duration != retryAfter {
		t.Errorf("snoozed for %s, want the %s Telegram named", snooze.Duration, retryAfter)
	}
	if len(api.cleared) != 0 {
		t.Errorf("a throttle cleared %d webhook(s) — a rate limit says nothing about a registration", len(api.cleared))
	}
}

// Telegram permits exactly ONE getUpdates consumer per bot. That rule is enforced
// by uniqueness on the ARGS TYPE — not at the one call site that enqueues today —
// so no future inserter can drop it by omission. Two in-flight polls for one
// connection would steal each other's batches, each acknowledging updates the
// other was about to store.
func TestOnlyOnePollPerConnectionCanBeInFlight(t *testing.T) {
	opts := TelegramPollArgs{}.InsertOpts()

	if !opts.UniqueOpts.ByArgs {
		t.Fatal("TelegramPollArgs does not deduplicate by args — two polls of one bot would steal each other's updates")
	}
	if len(opts.UniqueOpts.ByState) == 0 {
		t.Fatal("TelegramPollArgs names no state window, so River's default includes completed and ingress would run once per process")
	}
	for _, state := range opts.UniqueOpts.ByState {
		if state == "completed" {
			t.Fatal("the uniqueness window includes completed jobs, so a finished poll would block every later one")
		}
	}
	// The dispatcher enqueues with no per-insert opts, so the args-borne policy is
	// the only thing standing between a bot and two concurrent consumers.
	var _ river.JobArgsWithInsertOpts = TelegramPollArgs{}
}
