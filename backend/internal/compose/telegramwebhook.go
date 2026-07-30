// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The Telegram ingress webhook (design §6.1, §6.2): the delivery side of
// Task 6's connect, and the other half of the shared chassis's asymmetry
// gmailpush.go's file comment documents — Telegram's payload is the ONLY
// copy of the message, so it must be durably persisted before this handler
// ever answers 200. It sits on the shared chassis (webhook.go, design
// §6.5), which owns admission and response discipline; this file declares
// only what is genuinely Telegram-specific: a per-connection secret
// resolved by fleet probe rather than one operator token, no second
// verifier (Telegram signs nothing else the way Google signs Gmail's
// push), and a Handle that never returns Accepted without a committed
// transaction behind it.
//
// Tenant resolution (design §6.1): the webhook carries no session, so it
// cannot open a workspace transaction to read channel_connection until it
// already knows the workspace — and finding that out IS the read.
// capture.ResolveChannelConnection solves this exactly as BumpDueByMailbox
// solves the identical problem for Gmail's push: enumerate every workspace
// from the un-scoped `workspace` table, then probe each under its own GUC.
//
// It is deliberately resolved TWICE per request — once by Secret to verify
// the delivery, once by Handle to open the write transaction — rather than
// threading the first result forward through the request. The design
// itself calls this a small, single-organization fleet, and two cheap
// probes read far more plainly than smuggling state between two
// independently-documented WebhookSpec hooks over a shared *http.Request.

package compose

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/capture/telegram"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/platform/ratelimit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// telegramSecretHeader is where Telegram carries the per-connection secret
// registered at setWebhook (design §6.2 step 2) — a header, never the path
// or a query string, so it is never captured by an access log or a
// browser's URL history.
const telegramSecretHeader = "X-Telegram-Bot-Api-Secret-Token" //nolint:gosec // G101 false positive: the header's NAME, not the secret it carries

// TelegramIngestArgs is the durable work request the webhook's one
// transaction enqueues alongside the raw row. Task 9's worker
// re-establishes the workspace context from these args (as CaptureSyncArgs
// already does) and runs Normalize → Sink. RawCaptureID names the exact
// row to normalize, so a redelivery that refreshed an existing raw_capture
// row (rather than minting a new one) still points the job at the right
// payload.
//
// BotID is the bot that RECEIVED this update, pinned here at delivery
// exactly as the raw row's key is, and never resolved from the connection
// row when the job runs. ReplaceToken re-points a live connection at a
// different bot in place, so the row's channel_id is mutable state: a job
// reading it later would build the message's natural key and thread_key
// from whichever bot is current, filing one bot's message into another
// bot's conversation — and because Telegram's message ids restart per chat
// per bot, that re-keyed natural key can equal a real message of the new
// bot's, whereupon the Sink's idempotent upsert merges two different
// customers' messages into one activity. The bot that received an update is
// a fact about the delivery, so it travels with the delivery.
//
// A field constant per raw row does not weaken river's ByArgs dedupe: every
// redelivery of one update resolves to the same raw row and therefore the
// same bot.
type TelegramIngestArgs struct {
	Workspace string `json:"workspace"`
	// ConnectionID names which connection delivered the update. The worker
	// resolves nothing from it — that is the point of BotID — but it is the
	// operational link between a queued job and the connection an operator is
	// looking at, and the key the ingest jobs are queried by.
	ConnectionID string `json:"connection_id"`
	BotID        string `json:"bot_id"`
	RawCaptureID string `json:"raw_capture_id"`
}

// Kind names this job to River.
func (TelegramIngestArgs) Kind() string { return "telegram_ingest" }

// telegramEnqueuer is the slice of *jobs.Runner the webhook needs, narrowed
// so a test can inject a failure INSIDE the real transaction without a real
// river client — mirrors deepReadEnqueuer (deepreadtransport.go).
type telegramEnqueuer interface {
	EnqueueTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) error
}

// WithTelegramWebhook records the job inserter the ingress webhook enqueues
// onto. It must be applied BEFORE WithKeyvault, which is what actually
// builds the handler once both the inserter and the vault it needs to
// unseal a connection's webhook secret are known — the same
// two-options-in-order contract WithChannelWebhookBase already states for
// this feature's connect side, and cmd/api holds both.
func WithTelegramWebhook(inserter *jobs.Runner) Option {
	return func(s *Server, _ *pgxpool.Pool) {
		s.telegramInserter = inserter
	}
}

// newTelegramWebhookHandler composes the whole ingress edge: the throttle in
// front of the shared chassis. It is the ONE spelling of that composition, so
// a test exercising "the Telegram webhook" exercises the same handler
// cmd/api mounts rather than the chassis alone with the brake missing.
func newTelegramWebhookHandler(pool *pgxpool.Pool, vault keyvault.Vault, inserter telegramEnqueuer, limits telegramWebhookLimiters, log *slog.Logger) http.Handler {
	return throttleTelegramWebhook(limits, Webhook(telegramWebhookSpec(pool, vault, inserter, log), log))
}

// telegramWebhookSpec declares Telegram's side of the chassis (design
// §6.2/§6.5): a per-connection secret rather than one deployment-wide
// token, no second verifier, and a Handle that persists the update before
// ever returning Accepted — see the file comment for why that ordering is
// not optional.
func telegramWebhookSpec(pool *pgxpool.Pool, vault keyvault.Vault, inserter telegramEnqueuer, log *slog.Logger) WebhookSpec {
	return WebhookSpec{
		Provider: "telegram",
		MaxBody:  1 << 20,
		Secret:   telegramSecretFunc(pool, vault, log),
		Handle:   handleTelegramWebhook(pool, inserter, log),
		OnAccept: http.StatusOK,
	}
}

// telegramWebhookLimiters brake the one unauthenticated edge in this
// installation that does DATABASE work to decide admission: verifying the
// secret means resolving the connection first, which is a pool query plus a
// probe under every live workspace's GUC. Gmail's side of the same chassis
// compares an in-memory constant and touches nothing, so it needs no brake;
// this one does, for the same reason the two other public edges have theirs
// (publicbooking.go, publicpreferences.go) — an anonymous caller must not be
// able to spend the pool.
//
// Per-IP is the brake that actually holds: a flood comes from one or a few
// hosts, and Telegram's own deliveries arrive from its published ranges.
// Per-connection covers a distributed flood aimed at one connection id.
// Both are set far above any real bot's traffic — a busy official account is
// orders of magnitude below these — so a legitimate delivery is never the
// request that trips them.
type telegramWebhookLimiters struct {
	perIP         *ratelimit.Limiter
	perConnection *ratelimit.Limiter
}

func newTelegramWebhookLimiters() telegramWebhookLimiters {
	return telegramWebhookLimiters{
		perIP:         ratelimit.New(600, time.Minute),
		perConnection: ratelimit.New(300, time.Minute),
	}
}

// throttleTelegramWebhook refuses over-budget deliveries before the chassis
// reads a body or the secret function touches the pool.
//
// 429 rather than a 4xx that ends the delivery: Telegram treats anything that
// is not a 2xx as "try again later", and that is exactly right here — a
// throttled delivery was never inspected, so the update is not poison and
// must not be dropped (there is no history API to re-fetch it from). The
// response carries no body, like every other refusal on this edge.
func throttleTelegramWebhook(limits telegramWebhookLimiters, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limits.perIP.Allow(publicClientIP(r)) || !limits.perConnection.Allow(r.PathValue("connection_id")) {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// telegramSecretFunc resolves the connection named in the path and unseals
// its webhook secret to compare against Telegram's header. An unknown
// connection id, a non-`connected` status, or a resolve/unseal fault all
// answer the same way: a freshly minted value the header can never equal,
// so the chassis's constant-time compare refuses with a bare 401 — an
// attacker learns nothing about which failure occurred, or which
// connection ids exist (design §6.2 step 2's "mismatch or unknown → bare
// 401, no body detail").
func telegramSecretFunc(pool *pgxpool.Pool, vault keyvault.Vault, log *slog.Logger) func(*http.Request) (want, got string) {
	return func(r *http.Request) (want, got string) {
		got = r.Header.Get(telegramSecretHeader)
		if got == "" {
			// Answered before any database work. setWebhook always registers a
			// secret, so a genuine delivery always carries this header; a
			// request without one cannot be admitted whatever the connection
			// row says, and resolving that row first would let an anonymous
			// caller spend a pool connection per live workspace to learn
			// nothing.
			return telegramUnmatchableSecret(), got
		}
		id, err := ids.Parse(r.PathValue("connection_id"))
		if err != nil {
			return telegramUnmatchableSecret(), got
		}
		conn, ok, err := capture.ResolveChannelConnection(r.Context(), pool, id)
		if err != nil {
			log.ErrorContext(r.Context(), "telegram webhook: resolving the connection for secret verification", "err", err)
			return telegramUnmatchableSecret(), got
		}
		if !ok {
			return telegramUnmatchableSecret(), got
		}
		secret, err := vault.Get(r.Context(), ids.From[ids.WorkspaceKind](conn.WorkspaceID), conn.WebhookSecretRef)
		if err != nil {
			log.ErrorContext(r.Context(), "telegram webhook: unsealing a connection's webhook secret",
				"connection", id.String(), "err", err)
			return telegramUnmatchableSecret(), got
		}
		return string(secret), got
	}
}

// telegramUnmatchableSecret mints a fresh value nothing an attacker sends
// could ever equal, so a resolution failure fails the comparison rather
// than risking a fixed sentinel (an empty string, a constant) that an
// absent or empty header could accidentally match. ids.NewV7 already
// documents the crypto/rand precondition this relies on: a failure there
// means the process cannot mint identity and must not continue, so this
// deliberately does not add a second, weaker fallback path.
func telegramUnmatchableSecret() string {
	return ids.NewV7().String()
}

// telegramUpdateEnvelope reads only the one field this handler needs
// before deciding whether the payload is even storable: Telegram's
// update_id, the redelivery counter telegramRawSourceID builds the raw
// row's key from — never the bot:chat:message natural key, which belongs
// to Task 9's domain row, not here.
type telegramUpdateEnvelope struct {
	UpdateID int64 `json:"update_id"`
}

// telegramRawSourceID is raw_capture's redelivery key for one delivered
// update. update_id is a PER-BOT sequence, so the key MUST carry the bot:
// raw_capture_source_unique is (workspace_id, source_system, source_id) and
// InsertRawCaptureTx's ON CONFLICT overwrites the stored payload, so a bare
// update_id would let a second bot in the same workspace (uq_channel_connection_ws
// permits several) land on the first bot's row and destroy the only copy of a
// message this handler had already answered 200 for — unrecoverable, because
// Telegram has no history API to re-fetch it from.
//
// The bot id and not the connection id, though the same index makes the two
// interchangeable for live deliveries: the counter being namespaced is the
// BOT's, so a bot disconnected and reconnected under a fresh connection id
// still dedupes its own redeliveries against what it delivered before.
func telegramRawSourceID(botID string, updateID int64) string {
	return fmt.Sprintf("%s:%d", botID, updateID)
}

// errTelegramConnectionVanished marks the one honest race this handler can
// hit: Secret resolved the connection a moment earlier in the same
// request, so reaching Handle with none means a disconnect landed in the
// tiny window between the two. The update itself is fine — only this
// delivery's timing was unlucky — so this is Transient, never Poison:
// dropping a good message silently is the one outcome design §6.2 forbids.
var errTelegramConnectionVanished = errors.New("telegram webhook: connection resolved during admission is gone")

// errTelegramSubjectErased unwinds the write transaction for an update whose
// sender this installation has erased under Art. 17. It is a control signal,
// not a fault: the delivery was understood and answered, there is simply
// nothing this installation is still permitted to keep about the human who
// sent it.
var errTelegramSubjectErased = errors.New("telegram webhook: the update's subject has been erased")

// telegramSubjectErased reports whether any account this update is about
// carries an erasure suppression entry. The probe runs inside the write
// transaction and BEFORE the raw insert, because the raw row is the whole
// problem: the erasure suppression list stops the Person from being recreated,
// but a verbatim update persisted anyway holds the sender's numeric id,
// handle, first and last name and the full message text — and no later erasure
// can reach it, because both the raw purge and the suppression are driven off
// person_channel_identity rows that the first erasure deleted and the
// suppression guarantees are never recreated. Refusing to persist is
// therefore the only point at which this data can be kept out.
//
// Nothing is preserved for the operator to inspect. That is deliberate: an
// installation told to stop profiling a human does not get to keep their words
// in a quarantine table.
func telegramSubjectErased(ctx context.Context, tx pgx.Tx, accounts []string) (bool, error) {
	for _, account := range accounts {
		suppressed, err := storekit.ChannelIdentitySuppressed(ctx, tx, telegram.Provider, account)
		if err != nil {
			return false, fmt.Errorf("telegram webhook: probing the erasure suppression list: %w", err)
		}
		if suppressed {
			return true, nil
		}
	}
	return false, nil
}

// handleTelegramWebhook persists the update and enqueues its normalize job
// in ONE transaction, then reports Accepted — never the reverse order (see
// the file comment: Telegram has no history API, so an update acknowledged
// before it is durable is gone the instant a crash lands between the two).
//
// A body that fails to decode is Poison: the same bytes would fail
// identically on redelivery, so refusing to retry (a 2xx) is correct.
// Anything that goes wrong resolving the connection or running the
// transaction is Transient: redelivery is exactly the recovery path, and
// it is the ONLY recovery path, because there is no history API to
// re-fetch this update from if it is dropped here.
//
// An update from an erased subject is the one case that is Accepted with
// NOTHING committed — see telegramSubjectErased for why storing it would put
// the subject's identifiers and words beyond the reach of any later erasure.
func handleTelegramWebhook(pool *pgxpool.Pool, inserter telegramEnqueuer, log *slog.Logger) func(context.Context, *http.Request, []byte) (Disposition, error) {
	return func(ctx context.Context, r *http.Request, body []byte) (Disposition, error) {
		var update telegramUpdateEnvelope
		if err := json.Unmarshal(body, &update); err != nil {
			return Poison, fmt.Errorf("telegram webhook: decoding the update envelope: %w", err)
		}
		accounts, err := telegram.SubjectAccountIDs(body)
		if err != nil {
			return Poison, fmt.Errorf("telegram webhook: reading the update's subject: %w", err)
		}

		id, err := ids.Parse(r.PathValue("connection_id"))
		if err != nil {
			// Secret already cleared admission for this exact path value,
			// so a path the router could not have matched is not a real
			// branch — Transient rather than a panic because r.PathValue
			// is still, formally, untrusted input.
			return Transient, fmt.Errorf("telegram webhook: path connection id: %w", err)
		}
		conn, ok, err := capture.ResolveChannelConnection(ctx, pool, id)
		if err != nil {
			return Transient, fmt.Errorf("telegram webhook: resolving the connection to persist: %w", err)
		}
		if !ok {
			return Transient, errTelegramConnectionVanished
		}

		wsCtx := principal.WithWorkspaceID(ctx, conn.WorkspaceID)
		err = database.WithWorkspaceTx(wsCtx, pool, func(tx pgx.Tx) error {
			erased, err := telegramSubjectErased(wsCtx, tx, accounts)
			if err != nil {
				return err
			}
			if erased {
				return errTelegramSubjectErased
			}
			rawID, err := capture.InsertRawCaptureTx(wsCtx, tx, capture.RawRecord{
				SourceSystem: "telegram",
				SourceID:     telegramRawSourceID(conn.ChannelID, update.UpdateID),
				Payload:      body,
			})
			if err != nil {
				return err
			}
			return inserter.EnqueueTx(wsCtx, tx, TelegramIngestArgs{
				Workspace:    conn.WorkspaceID.String(),
				ConnectionID: id.String(),
				BotID:        conn.ChannelID,
				RawCaptureID: rawID.String(),
			}, &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true}})
		})
		if errors.Is(err, errTelegramSubjectErased) {
			// Accepted, and indistinguishable from an ordinary accept on the
			// wire: a status Telegram could tell apart would let anyone holding
			// the connection URL and secret test whether a given account has
			// been erased. The log names no account for the same reason the
			// row was not written.
			log.InfoContext(ctx, "telegram webhook: dropped an update whose subject has been erased",
				"connection", id.String())
			return Accepted, nil
		}
		if err != nil {
			return Transient, fmt.Errorf("telegram webhook: persisting the raw update and its enqueue: %w", err)
		}
		return Accepted, nil
	}
}
