// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The capture path, assembled: the one Sink over the pool (with the
// approvals engine as its merge-stager — dedupe collisions become 🟡
// proposals, never auto-merges), the connector registry with identity
// as the live-authority resolver — composed here so capture never
// imports identity or approvals (ADR-0054 §9).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/capture/gmail"
	"github.com/gradionhq/margince/backend/internal/modules/capture/imap"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// gmailReadonlyScope is the single Google scope the read-only capture
// connector requests (mail read; no send, no modify).
const gmailReadonlyScope = "https://www.googleapis.com/auth/gmail.readonly"

// NewCaptureRegistry builds the connector registry; process roles register
// their compiled-in connectors on it and drive SyncOnce. The vault seals and
// resolves each connection's credential (nil is valid for a role that only
// runs the transient one-shot pull, which persists no credential).
func NewCaptureRegistry(pool *pgxpool.Pool, vault keyvault.Vault) *capture.Registry {
	r := capture.NewRegistry(pool, newCaptureSink(pool), identity.NewService(pool), vault)
	// The standing IMAP connector needs no deployment config — credentials
	// are per-connection, vault-sealed — so every capture-capable role
	// carries it.
	r.Register(imap.NewStanding())
	return r
}

// newCaptureSink assembles the ONE fully-guarded Sink over the pool — the
// merge-stager and the exclusion gate attached. Every capture path shares
// this spelling: the connector registry above, and the site_lead accept
// effect (siteleadaccept.go), which captures through the Sink directly
// without needing a registry.
func newCaptureSink(pool *pgxpool.Pool) *capture.Sink {
	return capture.NewSink(pool).
		WithStager(mergeStager{svc: approvals.NewService(pool)}).
		// The RC-2 personal-mail exclusion gate runs in the ONE Sink before
		// any write, so it covers EVERY connector (imap one-shot, gmail
		// sync) uniformly (capture.md CAP-DDL-3, AC1.3).
		WithExclusions(capture.NewExclusions(pool))
}

// GmailConfig is the composed Gmail OAuth app for a deployment (RC-8): one app
// per deployment, supplied by whoever operates it (EP05.8 — per-workspace apps
// are a follow-up). ClientID+ClientSecret enable the background sync (token
// refresh); StateKey+PublicBaseURL additionally enable the connect/callback
// transport (the signed state and the redirect target).
type GmailConfig struct {
	ClientID     string
	ClientSecret string
	StateKey     string
	// PublicBaseURL is the canonical public/front origin (the SPA): the
	// post-consent landing, and the default callback base for a same-origin
	// deployment.
	PublicBaseURL string
	// APIBaseURL is the api's externally-reachable base, used only for the
	// callback redirect_uri. Empty for a same-origin deployment (the callback
	// rides PublicBaseURL); a split dev stack sets it to the api URL.
	APIBaseURL string
}

// canSync reports whether the connector can be registered + polled (token
// refresh needs the client id/secret).
func (c GmailConfig) canSync() bool { return c.ClientID != "" && c.ClientSecret != "" }

// minStateKeyLen is the floor for the OAuth state-signing HMAC key; a shorter
// key would make the signed state cheaply forgeable.
const minStateKeyLen = 32

// canConnect reports whether the human-facing connect/callback transport can
// run: it needs the sync creds plus a callback URL and a state key of at least
// minStateKeyLen bytes (a weak key is refused, not silently accepted).
func (c GmailConfig) canConnect() bool {
	return c.canSync() && len(c.StateKey) >= minStateKeyLen && c.PublicBaseURL != ""
}

// Enabled reports whether the connect/callback transport is fully configured —
// the same condition WithGmailCapture gates on, exported so a caller (cmd) can
// log accurately rather than guessing from the client id alone.
func (c GmailConfig) Enabled() bool { return c.canConnect() }

//nolint:ireturn // returns the gmail.OAuth seam by design (a fakeable interface)
func newGmailOAuth(c GmailConfig) gmail.OAuth {
	return gmail.NewOAuth(gmail.OAuthConfig{
		ClientID:     c.ClientID,
		ClientSecret: c.ClientSecret,
		Scopes:       []string{gmailReadonlyScope},
	})
}

// NewCaptureRegistryWithGmail builds the capture registry and, when the Gmail
// OAuth app is configured, registers the read-only Gmail connector so
// Registry.Connect (api) and SyncOnce (worker poller) can resolve it by name.
// A deployment without the app configured gets a plain registry (Gmail absent
// by omission). Returns nil only if pool is nil.
func NewCaptureRegistryWithGmail(pool *pgxpool.Pool, vault keyvault.Vault, c GmailConfig) *capture.Registry {
	reg := NewCaptureRegistry(pool, vault)
	if c.canSync() {
		reg.Register(gmail.New(newGmailOAuth(c), gmail.NewAPI(nil, "")))
	}
	return reg
}

// GmailPollRegistry returns a Gmail-registered capture registry for the
// worker's background poller, or nil when the Gmail app is not configured —
// nil tells NewJobRunner to skip the poll entirely (no connector, no job).
func GmailPollRegistry(pool *pgxpool.Pool, vault keyvault.Vault, c GmailConfig) *capture.Registry {
	if !c.canSync() {
		return nil
	}
	return NewCaptureRegistryWithGmail(pool, vault, c)
}

// CaptureSyncRegistry is the worker's sweep registry: always non-nil —
// the standing IMAP connector needs no deployment config — with the gmail
// connector added when its OAuth app is configured. A provider nobody
// registered simply never appears in the dispatcher's provider list.
func CaptureSyncRegistry(pool *pgxpool.Pool, vault keyvault.Vault, c GmailConfig) *capture.Registry {
	if c.canSync() {
		return NewCaptureRegistryWithGmail(pool, vault, c)
	}
	return NewCaptureRegistry(pool, vault)
}

// WithGmailCapture wires the Gmail OAuth connect/callback/disconnect/list
// transport (api role). It requires the vault (so WithKeyvault must precede it
// in the option list) and a fully-configured app; absent any of those the
// connector surface keeps its declared-but-unimplemented 501 by omission.
func WithGmailCapture(c GmailConfig) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		// Without a vault the connect flow can't seal the refresh token, so
		// mounting the endpoints would only fail at the callback — leave the
		// surface its declared 501 instead. (WithKeyvault must precede this.)
		if !c.canConnect() || s.vault == nil {
			return
		}
		s.connectorHandlers = connectorHandlers{
			registry:      NewCaptureRegistryWithGmail(pool, s.vault, c),
			oauth:         newGmailOAuth(c),
			gmailAPI:      gmail.NewAPI(nil, ""),
			signer:        newStateSigner([]byte(c.StateKey)),
			publicBaseURL: c.PublicBaseURL,
			apiBaseURL:    c.APIBaseURL,
		}
	}
}

// mergeStager adapts the approvals engine to capture's dedupe seam.
type mergeStager struct {
	svc *approvals.Service
}

func (m mergeStager) StageMerge(ctx context.Context, in capture.MergeProposal) (ids.UUID, error) {
	digest := sha256.Sum256(in.ProposedChange)
	// A connector re-syncing the same upstream record hits the same
	// collision every cycle; an identical pending proposal must absorb
	// the repeat, not multiply in the inbox.
	pending, err := m.svc.HasPendingFor(ctx, "merge_records", in.TargetID, hex.EncodeToString(digest[:]))
	if err != nil {
		return ids.Nil, err
	}
	if pending {
		return ids.Nil, nil
	}
	id, err := m.svc.Stage(ctx, approvals.StageInput{
		Kind:           "merge_records",
		ProposedChange: in.ProposedChange,
		DiffHash:       hex.EncodeToString(digest[:]),
		TargetType:     in.TargetType,
		TargetID:       in.TargetID,
		Summary:        in.Summary,
	})
	return id.UUID, err
}
