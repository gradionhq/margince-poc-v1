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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
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
type TelegramIngestArgs struct {
	Workspace    string `json:"workspace"`
	ConnectionID string `json:"connection_id"`
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
// update_id, the redelivery key raw_capture's unique index dedupes on —
// never the bot:chat:message natural key, which belongs to Task 9's domain
// row, not here.
type telegramUpdateEnvelope struct {
	UpdateID int64 `json:"update_id"`
}

// errTelegramConnectionVanished marks the one honest race this handler can
// hit: Secret resolved the connection a moment earlier in the same
// request, so reaching Handle with none means a disconnect landed in the
// tiny window between the two. The update itself is fine — only this
// delivery's timing was unlucky — so this is Transient, never Poison:
// dropping a good message silently is the one outcome design §6.2 forbids.
var errTelegramConnectionVanished = errors.New("telegram webhook: connection resolved during admission is gone")

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
func handleTelegramWebhook(pool *pgxpool.Pool, inserter telegramEnqueuer, log *slog.Logger) func(context.Context, *http.Request, []byte) (Disposition, error) {
	return func(ctx context.Context, r *http.Request, body []byte) (Disposition, error) {
		var update telegramUpdateEnvelope
		if err := json.Unmarshal(body, &update); err != nil {
			return Poison, fmt.Errorf("telegram webhook: decoding the update envelope: %w", err)
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
			rawID, err := capture.InsertRawCaptureTx(ctx, tx, capture.RawRecord{
				SourceSystem: "telegram",
				SourceID:     fmt.Sprintf("%d", update.UpdateID),
				Payload:      body,
			})
			if err != nil {
				return err
			}
			return inserter.EnqueueTx(ctx, tx, TelegramIngestArgs{
				Workspace:    conn.WorkspaceID.String(),
				ConnectionID: id.String(),
				RawCaptureID: rawID.String(),
			}, &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true}})
		})
		if err != nil {
			return Transient, fmt.Errorf("telegram webhook: persisting the raw update and its enqueue: %w", err)
		}
		return Accepted, nil
	}
}
