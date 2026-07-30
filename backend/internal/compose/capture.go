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
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/capture/gcal"
	"github.com/gradionhq/margince/backend/internal/modules/capture/gmail"
	"github.com/gradionhq/margince/backend/internal/modules/capture/graph"
	"github.com/gradionhq/margince/backend/internal/modules/capture/imap"
	"github.com/gradionhq/margince/backend/internal/modules/capture/telegram"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// gmailScopes are the Google scopes a Gmail connection requests: mail read for
// capture, and send for the governed outbound path. They ride ONE consent
// because Google will not add a scope to an existing refresh token — a second
// grant would mean a second connection for the same mailbox.
//
// The send scope permits transmission only; it cannot read, modify, or delete.
// The pair is still least-privilege: no gmail.modify, no settings, no delete.
// The calendar connector owns its own calendar-read scope inside the gcal
// package.
//
// The send entry is the connector's OWN constant, not a copy of its text: what
// the consent requests and what the connector re-checks are then one string by
// construction. The second literal — the scope comms demands at the authority
// gate — cannot be imported by either (comms must not reach into a capture
// provider), so a fitness test binds it here instead: sendscope_test.go.
var gmailScopes = []string{
	"https://www.googleapis.com/auth/gmail.readonly",
	gmail.SendScope,
}

// graphScopes are the Microsoft identity platform scopes the read-only Graph
// capture connector requests: mail read + the signed-in user's profile (the
// mailbox owner lookup) + offline_access (the refresh token). No send, no
// modify.
var graphScopes = []string{"offline_access", "User.Read", "Mail.Read"}

// CaptureConfig is the deployment's capture list-config, threaded from
// margince.yaml's `capture:` block into the Sink's suppression gates: the
// CAP-PARAM-5 free-mail additions and the CAP-PARAM-6 transactional/ESP
// additions plus its allowlist (ADR-0072) — plus the process logger the Sink's
// post-commit steps report through. The zero value is the pinned baselines with
// no deployment additions and the default logger.
type CaptureConfig struct {
	FreemailExtra      []string // capture.freemail_extra (CAP-PARAM-5)
	TransactionalExtra []string // capture.transactional_extra (CAP-PARAM-6 infra eSLDs)
	TransactionalNever []string // capture.transactional_never (CAP-PARAM-6 allowlist)
	// Logger carries the process logger to the post-commit steps the Sink
	// drives, where a fault is reported rather than returned (nothing may fail
	// a capture). Nil falls back to the default logger — the site_lead accept
	// path composes a Sink without a deployment config at all.
	Logger *slog.Logger
}

// logger is the configured logger, or the process default.
func (c CaptureConfig) logger() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}

// WithCaptureConfig records the deployment's capture suppression-list config on
// the Server so EVERY registry construction — the Gmail one, the vault-rebuilt
// IMAP/fallback one (WithKeyvault), and the graph-only one (WithGraphCapture) —
// applies the transactional/free-mail additions, not only the Gmail path. Apply
// it before WithKeyvault/WithGraphCapture in the option list; omitting it keeps
// the pinned baselines.
func WithCaptureConfig(cfg CaptureConfig) Option {
	return func(s *Server, _ *pgxpool.Pool) { s.captureConfig = cfg }
}

// CaptureConfigFromDeploy maps the deployment's `capture:` block onto the
// compose suppression config the Sink gates read (CAP-PARAM-5/6, ADR-0072).
func CaptureConfigFromDeploy(c deployconfig.Capture, log *slog.Logger) CaptureConfig {
	return CaptureConfig{
		FreemailExtra:      c.FreemailExtra,
		TransactionalExtra: c.TransactionalExtra,
		TransactionalNever: c.TransactionalNever,
		Logger:             log,
	}
}

// NewCaptureRegistry builds the connector registry; process roles register
// their compiled-in connectors on it and drive SyncOnce. The vault seals and
// resolves each connection's credential (nil is valid for a role that only
// runs the transient one-shot pull, which persists no credential). cfg carries
// the deployment's suppression-list additions and the logger; the zero value is
// the baselines and the default logger.
func NewCaptureRegistry(pool *pgxpool.Pool, vault keyvault.Vault, cfg CaptureConfig) *capture.Registry {
	r := capture.NewRegistry(pool, newCaptureSink(pool, cfg), identity.NewService(pool), vault)
	// The standing IMAP connector needs no deployment config — credentials
	// are per-connection, vault-sealed — so every capture-capable role
	// carries it.
	r.Register(imap.NewStanding())
	// Telegram is registered on the same terms and for the same reason: a bot
	// binding's token is per-connection and vault-sealed, so there is no
	// deployment-wide app to configure. It is the registration that lets the send
	// path resolve the workspace's bot at all — Registry.ChannelSenderFor
	// type-asserts the message seam off this map, so an unregistered connector
	// reads as "this installation has no Telegram integration" and parks every
	// reply a rep writes.
	r.Register(telegram.New(telegram.NewAPI(nil, "")))
	return r
}

// newCaptureSink assembles the ONE fully-guarded Sink over the pool — the
// merge-stager, the exclusion gate, and the counterparty auto-create
// resolver attached. Every capture path shares this spelling: the connector
// registry above, and the site_lead accept effect (siteleadaccept.go),
// which captures through the Sink directly without needing a registry.
func newCaptureSink(pool *pgxpool.Pool, cfg CaptureConfig) *capture.Sink {
	ensurer := peopleEnsurer{store: people.NewStore(pool), enrich: newAutoEnrichTrigger(pool, cfg.logger()), log: cfg.logger()}
	return capture.NewSink(pool).
		WithStager(mergeStager{svc: approvals.NewService(pool)}).
		// The RC-2 personal-mail exclusion gate runs in the ONE Sink before
		// any write, so it covers EVERY connector (imap one-shot, gmail
		// sync) uniformly (capture.md CAP-DDL-3, AC1.3).
		WithExclusions(capture.NewExclusions(pool)).
		// The ADR-0063 auto-create pipeline: every captured mail ensures
		// its counterparty exists, through the people module's ONE dedupe
		// chokepoint — composed here so capture never imports people. The
		// free-mail (CAP-PARAM-5) and transactional/ESP (CAP-PARAM-6, ADR-0072)
		// gates decide which senders derive no company / no counterparty.
		WithEnsurer(ensurer,
			capture.NewFreemailList(cfg.FreemailExtra),
			capture.NewTransactionalList(cfg.TransactionalExtra, cfg.TransactionalNever)).
		// The channel twin of the line above (telegram-oa design §6.4): an
		// inbound channel message reaches the SAME module through its own
		// contract — one adapter serving two seams, so the two ensures cannot
		// drift onto different dedupe implementations.
		WithChannelEnsurer(ensurer)
}

// peopleEnsurer adapts the people module's auto-create engine onto
// capture's resolver seams — the mail one and the channel one. Both land in the
// same module because both must resolve through the same dedupe chokepoint; the
// contracts differ because a mail counterparty is named by an address and a
// channel counterparty by a provider identity.
type peopleEnsurer struct {
	store *people.Store
	// enrich queues the web dossier for a company this ensure just minted. It
	// lives HERE rather than in capture because capture must not know that
	// website reads exist — the seam it owns says "make the counterparty real",
	// and what a new company is then worth is the composition's business.
	enrich *autoEnrichTrigger
	// log reports a failed identity-review enqueue (raiseIdentityConflict) —
	// the one fault on this path that must never become a returned error,
	// because that would fail the capture that found it.
	log *slog.Logger
}

func (p peopleEnsurer) EnsureCounterparty(ctx context.Context, in capture.EnsureRequest) (capture.EnsureOutcome, error) {
	res, err := p.store.EnsureCounterparty(ctx, people.EnsureCounterpartyInput{
		Email:       in.Email,
		DisplayName: in.DisplayName,
		Domain:      in.Domain,
		OwnerID:     in.OwnerID,
		ActivityID:  ids.From[ids.ActivityKind](in.ActivityID),
		Source:      in.Source,
		CapturedBy:  in.CapturedBy,
		SuppressOrg: in.SuppressOrg,
	})
	if errors.Is(err, people.ErrCounterpartySuppressed) {
		// A13: the erased address stays dead — a deliberate no-op, not a
		// fault for the reconcile queue, and nothing was created to count.
		return capture.EnsureOutcome{}, nil
	}
	if err != nil {
		return capture.EnsureOutcome{}, err
	}
	// A NEW company is the trigger, not every captured mail: an organization
	// that already existed has either been enriched or been deliberately left
	// alone, and re-asking on every message from it would spend the day's cap on
	// companies nobody learned anything new about.
	if res.OrgCreated && res.OrganizationID != nil {
		p.enrich.organizationCaptured(ctx, *res.OrganizationID, in.Domain)
	}
	return capture.EnsureOutcome{PersonCreated: res.PersonCreated, OrganizationCreated: res.OrgCreated}, nil
}

// EnsureChannelCounterparty is the same adaptation for an inbound channel
// message (telegram-oa design §6.4). No company is derived and so no web
// dossier is queued: a channel identity carries no domain, and there is nothing
// for the enrich trigger to read a website from.
func (p peopleEnsurer) EnsureChannelCounterparty(ctx context.Context, in capture.EnsureChannelRequest) (capture.EnsureOutcome, error) {
	res, err := p.store.EnsureChannelCounterparty(ctx, people.EnsureChannelCounterpartyInput{
		Identity:    in.Identity,
		DisplayName: in.DisplayName,
		ActivityID:  ids.From[ids.ActivityKind](in.ActivityID),
		Source:      in.Source,
		CapturedBy:  in.CapturedBy,
	})
	if errors.Is(err, people.ErrCounterpartySuppressed) {
		// A13 on the channel key: an erased subject stays dead — a deliberate
		// no-op, not a fault for the reconcile queue, and nothing was created
		// to count.
		return capture.EnsureOutcome{}, nil
	}
	if err != nil {
		return capture.EnsureOutcome{}, err
	}
	if res.Conflict != nil {
		p.raiseIdentityConflict(ctx, *res.Conflict, in.Source, in.CapturedBy)
	}
	return capture.EnsureOutcome{PersonCreated: res.PersonCreated}, nil
}

// raiseIdentityConflict is the D8 identity-review half of routing (design
// §7.3): the ensure above already routed the message deterministically and
// wrote nothing onto the rival, so what remains is telling a human "these two
// records may be one person." It runs in its own transaction (EnqueueIdentityConflict),
// AFTER the ensure's own commit, and deliberately swallows nothing into
// silence — a failure is logged with the pair and both lanes so it is
// actionable — but never returns an error: the message that surfaced this
// conflict is already on the timeline, and it must stay there even when this
// write does not succeed. Every later message from the same conflicting
// identity retries this call, and dedupequeue's own pair index absorbs the
// repeat (EnqueueIdentityConflict's own contract), so a transient failure
// here self-heals on the next message rather than needing a retry queue.
func (p peopleEnsurer) raiseIdentityConflict(ctx context.Context, conflict people.LaneConflict, source, capturedBy string) {
	if _, err := p.store.EnqueueIdentityConflict(ctx, conflict, source, capturedBy); err != nil {
		p.log.ErrorContext(ctx, "capture: identity-conflict review failed to enqueue",
			"routed_to", conflict.RoutedTo.String(), "routed_lane", conflict.RoutedLane,
			"rival", conflict.Rival.String(), "rival_lane", conflict.RivalLane, "err", err)
	}
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
		Scopes:       gmailScopes,
	})
}

// newGcalOAuth builds the calendar connector's OAuth client. It shares the same
// Google app credentials as Gmail (one app per deployment) but authorizes
// SEPARATELY, requesting the calendar scope alone — the gcal package owns that
// scope and its own error sentinels, so calendar diagnostics never surface as
// "gmail:" and the credential never accretes Gmail's mail-read grant.
//
//nolint:ireturn // returns the gcal.OAuth seam by design (a fakeable interface)
func newGcalOAuth(c GmailConfig) gcal.OAuth {
	return gcal.NewOAuth(gcal.OAuthConfig{
		ClientID:     c.ClientID,
		ClientSecret: c.ClientSecret,
	})
}

// NewCaptureRegistryWithGmail builds the capture registry and, when the Google
// OAuth app is configured, registers the read-only Google connectors — Gmail
// and Google Calendar — so Registry.Connect (api) and SyncOnce (worker poller)
// can resolve each by name. A deployment without the app configured gets a
// plain registry (both absent by omission). Returns nil only if pool is nil.
func NewCaptureRegistryWithGmail(pool *pgxpool.Pool, vault keyvault.Vault, c GmailConfig, cfg CaptureConfig) *capture.Registry {
	reg := NewCaptureRegistry(pool, vault, cfg)
	if c.canSync() {
		reg.Register(gmail.New(newGmailOAuth(c), gmail.NewAPI(nil, "")))
		reg.Register(gcal.New(newGcalOAuth(c), gcal.NewAPI(nil, "")))
	}
	return reg
}

// GmailPollRegistry returns a Google-registered capture registry for the
// worker's background poller (Gmail + Calendar), or nil when the Google app is
// not configured — nil tells NewJobRunner to skip the polls entirely (no
// connector, no job).
func GmailPollRegistry(pool *pgxpool.Pool, vault keyvault.Vault, c GmailConfig, cfg CaptureConfig) *capture.Registry {
	if !c.canSync() {
		return nil
	}
	return NewCaptureRegistryWithGmail(pool, vault, c, cfg)
}

// CaptureSyncRegistry is the worker's sweep registry: always non-nil —
// the standing IMAP connector needs no deployment config — with the gmail
// and graph connectors added when their OAuth apps are configured. A provider
// nobody registered simply never appears in the dispatcher's provider list.
func CaptureSyncRegistry(pool *pgxpool.Pool, vault keyvault.Vault, c GmailConfig, g GraphConfig, cfg CaptureConfig) *capture.Registry {
	var reg *capture.Registry
	if c.canSync() {
		reg = NewCaptureRegistryWithGmail(pool, vault, c, cfg)
	} else {
		reg = NewCaptureRegistry(pool, vault, cfg)
	}
	if g.canSync() {
		reg.Register(graph.New(newGraphOAuth(g), graph.NewAPI(nil, "")))
	}
	return reg
}

// GraphConfig is the composed Microsoft (Graph) OAuth app for a deployment:
// one app per deployment, supplied by whoever operates it — the Microsoft
// twin of GmailConfig. ClientID+ClientSecret enable the background sync
// (token refresh); StateKey+PublicBaseURL additionally enable the
// connect/callback transport. Tenant narrows the identity endpoint to one
// Microsoft 365 tenant; empty means "common" (any organization).
type GraphConfig struct {
	ClientID     string
	ClientSecret string
	Tenant       string
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
func (c GraphConfig) canSync() bool { return c.ClientID != "" && c.ClientSecret != "" }

// canConnect reports whether the human-facing connect/callback transport can
// run: the sync creds plus a callback URL and a state key of at least
// minStateKeyLen bytes (a weak key is refused, not silently accepted).
func (c GraphConfig) canConnect() bool {
	return c.canSync() && len(c.StateKey) >= minStateKeyLen && c.PublicBaseURL != ""
}

// Enabled reports whether the connect/callback transport is fully configured —
// the same condition WithGraphCapture gates on, exported so a caller (cmd) can
// log accurately rather than guessing from the client id alone.
func (c GraphConfig) Enabled() bool { return c.canConnect() }

//nolint:ireturn // returns the graph.OAuth seam by design (a fakeable interface)
func newGraphOAuth(c GraphConfig) graph.OAuth {
	return graph.NewOAuth(graph.OAuthConfig{
		ClientID:     c.ClientID,
		ClientSecret: c.ClientSecret,
		Tenant:       c.Tenant,
		Scopes:       graphScopes,
	})
}

// WithGraphCapture wires the Microsoft Graph half of the connector OAuth
// transport (api role): it registers the graph connector on the connect
// registry — building the registry, signer, and base URLs if WithGmailCapture
// did not already (a graph-only deployment) — and installs the graph OAuth
// app the shared connect/callback dispatch resolves by provider. Like
// WithGmailCapture it requires the vault and a fully-configured app; absent
// either, the graph provider keeps its declared 501/422 by omission. Order:
// after WithKeyvault, and after WithGmailCapture when both are configured.
func WithGraphCapture(c GraphConfig) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		if !c.canConnect() || s.vault == nil {
			return
		}
		if s.connectorHandlers.registry == nil {
			s.connectorHandlers.registry = NewCaptureRegistry(pool, s.vault, s.captureConfig)
			s.authority = identity.NewService(pool)
			s.signer = newStateSigner([]byte(c.StateKey))
			s.publicBaseURL = c.PublicBaseURL
			s.apiBaseURL = c.APIBaseURL
		}
		s.connectorHandlers.registry.Register(graph.New(newGraphOAuth(c), graph.NewAPI(nil, "")))
		s.graphOAuth = newGraphOAuth(c)
		s.graphAPI = graph.NewAPI(nil, "")
	}
}

// WithGmailCapture wires the Gmail OAuth connect/callback/disconnect/list
// transport (api role). It requires the vault (so WithKeyvault must precede it
// in the option list) and a fully-configured app; absent any of those the
// connector surface keeps its declared-but-unimplemented 501 by omission.
//
// It ALSO installs the outbound send pre-flight (WithSendAuthority) over the
// registry it builds, so this option governs part of the send path too: with
// it, a user whose mailbox holds no send scope — or a reply on a channel this
// workspace bound no bot for — is refused at request time with an actionable
// 422; without it that check is simply absent and the refusal happens later, at
// transmission, where only an operator sees it. The placement is not incidental
// — see the comment at the wiring below.
func WithGmailCapture(c GmailConfig, cfg CaptureConfig) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		// Without a vault the connect flow can't seal the refresh token, so
		// mounting the endpoints would only fail at the callback — leave the
		// surface its declared 501 instead. (WithKeyvault must precede this.)
		if !c.canConnect() || s.vault == nil {
			return
		}
		s.connectorHandlers = connectorHandlers{
			registry:      NewCaptureRegistryWithGmail(pool, s.vault, c, cfg),
			authority:     identity.NewService(pool),
			oauth:         newGmailOAuth(c),
			gmailAPI:      gmail.NewAPI(nil, ""),
			gcalOAuth:     newGcalOAuth(c),
			gcalAPI:       gcal.NewAPI(nil, ""),
			signer:        newStateSigner([]byte(c.StateKey)),
			publicBaseURL: c.PublicBaseURL,
			apiBaseURL:    c.APIBaseURL,
		}
		// The send pre-flight reads the registry the connect flow just wrote
		// to — the same one, not a second construction: a mailbox the user
		// connects here must be the mailbox the check asks about. That registry
		// always carries the channel connectors too (NewCaptureRegistry
		// registers Telegram unconditionally), so ONE authority answers for both
		// transports and the provider comes from whichever send is asking. A
		// role without the Google app configured reaches none of this: the
		// pre-flight is absent by omission, exactly as the connect surface is.
		WithSendAuthority(mailboxAuthority{grants: s.connectorHandlers.registry})(s, pool)
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
