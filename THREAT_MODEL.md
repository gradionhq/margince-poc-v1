# Threat Model: Margince CRM (margince-poc-v1)

## 1. System context

Margince is a self-hosted, source-available (BUSL-1.1) sales-CRM proof-of-concept
by Gradion. Its distinguishing feature is a *governed AI-agent surface*: external
agents (Claude/Copilot/customer-owned) and an internal reason-act runner operate
directly on customer records through audited, risk-tiered tools over MCP (stdio or
hosted HTTP) and plain REST, rather than a bolt-on assistant. This is the build repo
(the running Go software, ~934 Go files); the normative specification lives in a
sibling `margince-foundation` repo and wins on any disagreement (contract-first,
principle P3). It is a single Go module (`github.com/gradionhq/margince/backend`)
organized as a strictly enforced dependency DAG — `shared → platform → modules(17) →
compose → cmd` — plus a standalone Vite/React SPA served separately (the API serves
`/v1` only; no embedded SPA).

The deployment model is **one installation per organization** (A107/ADR-0061): there
is no tenant selector on the wire; the server resolves its singleton workspace itself
and first-boot bootstraps the org + admin from a `margince.yaml` file under a Postgres
advisory lock. Even though it is single-org, `workspace_id` + Postgres row-level
security is still the isolation primitive on every tenant table. Four process binaries
share the compose layer: `cmd/api` (HTTP :8080 + inline outbox relay), `cmd/worker`
(standalone relay + the Surface-B agent runner + retention/reconciliation sweeps),
`cmd/migrate` (up|down + `reset-password`), and `cmd/mcp` (the stdio MCP server). The
production deployment is **Gradion-operated SaaS** (owner-stated): DB-role
provisioning, TLS termination, and secret injection are managed outside this
repository. It also targets customer-hosted and fully-local (zero-egress "sovereign")
profiles. Backing services are PostgreSQL 16 + pgvector, Redis 7 (event stream), and
MinIO (blob store).

The security posture is unusually explicit and largely enforced by **fitness tests
derived from the live schema** rather than maintained lists: FORCE + deny-on-unset RLS
keyed on an `app.workspace_id` GUC set only inside `WithWorkspaceTx` (fails closed
before any SQL); composite `(workspace_id, col)` tenant foreign keys; RBAC enforced at
the store/service entry point (not the handler) with existence-hiding (403 on object
denial, 404 on row-scope miss); a single-transaction write shape (domain +
append-only `audit_log` + `event_outbox`) with server-stamped provenance and an
immutable audit log; agent authority structurally bounded to `agent = passport scopes
∩ granting-human RBAC ∩ seat`, re-authenticated every call, with 🟡 (irreversible/
outbound) actions staged for human confirm-first approval; a default-deny
consent/outbound-suppression gate plus GDPR Art. 15/17 engines; and an SSRF egress
guard (`netguard`) plus a secret-stripper on model-bound payloads. The most notable
residual risk lives in the newest, least-battle-tested surfaces (the HubSpot overlay/
mirror mode; inbound connector webhooks) and in surfaces the code cannot fully settle
because they live in the out-of-tree SaaS deployment tier (secret injection, TLS,
network policy, DB-role provisioning) — confirmed by the owner but not verifiable from
this repository.

## 2. Assets

| asset | description | sensitivity |
|---|---|---|
| Person/organization/lead PII | `person`, `person_email/phone/social/address`, `organization`, `organization_domain/fact`, `lead` (incl. LinkedIn, lead-score). Core contactable + firmographic PII. | high |
| Deal/pipeline/revenue data | `deal` (amount_minor, FX-frozen), `pipeline`, `stage`, `deal_stage_history` (append-only money snapshots), `offer`/line-items, `product`, `fx_rate`. Commercial crown jewels. | high |
| Quotas / revenue targets | `quota` (owner-XOR-team, human-set, workspace-shared). Commercial planning data. | medium |
| Activity timeline | `activity` (+ thread/label/remind_at), `activity_link` (polymorphic person/org/deal/lead). Communication history; drives the link-walk row-scope. | high |
| Custom fields (`cf_*` columns) | `custom_field` catalog + runtime `cf_*` columns; the sole runtime `ALTER TABLE` chokepoint. Operator-defined PII of unknown sensitivity. | high |
| Consent records | `consent_purpose`, `person_consent`, `consent_event` (append-only proof with verbatim wording), `consent_doi_token`. GDPR lawful-basis evidence. | high |
| DSR / GDPR case data | `data_subject_request` (Art. 15/16/17 with statutory `due_at`), `erasure_suppression`. Regulator-facing. | high |
| Captured raw payloads | `raw_capture` (re-parseable email/calendar originals), `capture_digest`, `site_read` (scraped web). High-volume unfiltered inbound PII + untrusted content. | high |
| AI payload capture | `ai_call_payload` (post-SecretStripper request+response, opt-in, 365-day). Art. 9-adjacent. | high |
| Signals / Voice DNA / segments | `signal` + `signal_resolution` (consent-gated warm-room), `voice_profile*` (learned writing style), `list`/`tag`/`saved_view`. Targeting + derived intelligence. | medium |
| Attachments + blobstore | `attachment` (scan_status) + MinIO blob contents. User-uploaded files of arbitrary sensitivity. | medium |
| Session tokens | `session.token_hash` (SHA-256; raw never at rest); the `crm_session` cookie = primary human credential. | critical |
| Passport tokens (`mgp_`) | `passport.token_hash` — Agent Seat Passport, doubles as REST Bearer. Theft = acting as the granting human. | critical |
| Single-use auth tokens | `auth_token` (password_reset/email_verify/invite; hashed, short-TTL, single-use). Account-takeover vector. | high |
| Password hashes | `app_user.password_hash` (Argon2id, nullable for SSO). | high |
| Connector credentials | `connector_connection.auth` / `credential_ref` — live third-party OAuth/API creds (Gmail/GCal/IMAP/Graph). Access to external mailboxes/calendars. | critical |
| Vault secret custody | `vault_secret` (AES-256-GCM; **no RLS** — isolation is cryptographic via Ref + AAD). Root key `MARGINCE_KEYVAULT_ROOT_KEY`. | critical |
| AI BYOK provider keys | Anthropic/OpenAI/Gemini API keys (env-only). Billable + exfil-capable if leaked. | high |
| Workspace signing keys | `workspace_signing_key.private_key` (Ed25519) — signs approval JWS. Forgery = fabricated approvals. | critical |
| Bootstrap/config secrets | `margince.yaml` (org+admin, DSN), owner vs app-role DSNs, connector-state HMAC key, Redis/MinIO creds. Foundational infra secrets. | critical |
| Audit-log integrity | `audit_log` — append-only, DB trigger blocks UPDATE/DELETE, REVOKE from the app role. The P12 accountability spine. | critical |
| Write-shape + outbox integrity | `storekit` single-tx (domain+audit+`event_outbox`); `event_outbox` (**no RLS**, tenancy in the envelope); `captured_by` from principal. Break = broken audit/provenance. | critical |
| RLS tenant-isolation boundary | `WithWorkspaceTx` GUC + FORCE RLS on every `workspace_id` table + composite FKs. The isolation guarantee. | critical |
| Approval-staging authority | `approval` — the staged row IS the authority object; bound to `diff_hash` + `passport_id` + `version`, single-use, expiring. Tamper = unauthorized mutations execute. | critical |
| RBAC seat/scope + governance gates | `role`/`role_assignment`/`team`/`seat_type`, `auth.Admit`, the confirm-first approval gate, the consent suppression gate, GDPR erasure/SAR correctness, jurisdiction retention floors, the customfields ALTER chokepoint, the automation catalog. The control plane. | critical |
| Service availability | `cmd/api` :8080, `cmd/worker` (relay + Surface-B runner), PostgreSQL 16, Redis 7, `cmd/mcp`. Downtime stalls events/automations. | high |

## 3. Entry points & trust boundaries

| entry_point | description | trust_boundary | reachable_assets |
|---|---|---|---|
| /v1 contract API | Full CRM contract behind the session middleware chain (RecoverPanics→LimitBodies→SecureHeaders→Correlate→AccessLog→identity.Middleware→publicEdge→[agentGate,idempotency]→handler); body-capped; `correlation_id` server-minted. Includes auth (login/reset), user-management, uploads. | unauth network → authenticated human session (`crm_session`) OR agent passport → RBAC-gated store | all PII, deals, credentials, RBAC gates, audit/write-shape |
| /healthz /readyz /metrics probes | Unauthenticated by design; `/readyz` names the unready dependency; `/metrics` = Prometheus internals (outbox backlog, AI counters). Owner-stated: restricted to an operator network in the SaaS deployment. | anonymous network → server internal state (topology, metrics) | Service availability |
| /v1/public/* anonymous edges | Session-less booking + preference center; each resolves its own token/slug→tenant, own rate limiters, confined system principal. | anonymous internet → confined system principal → domain store | Person/organization/lead PII, Consent records |
| OAuth authorization server (/oauth/*) | ADR-0013 auth server; `/oauth/register` (open DCR, public clients + PKCE) and `/oauth/token` session-less; `/oauth/authorize` needs a session; mints Bearer passport tokens. | anonymous/public OAuth client → issued passport credential | Passport tokens, RBAC seat/scope |
| Connector OAuth callback | Session-less redirect; auth = HMAC-signed `state` (`--connector-state-key`) + CSRF cookie; workspace+human rebuilt from state; exact-one-segment path match. | unauth cross-site redirect → workspace+human reconstructed from signed state | Connector credentials, Vault secret custody |
| Gmail push webhook | Provider POST, mounted only when `--gmail-push-token` set; constant-time shared token + optional Google OIDC (audience + service-account); 1 MB body cap. | untrusted internet POST (forgeable URL) → shared secret + optional Google-signed identity → capture sync enqueue | Activity timeline, Captured raw payloads, Service availability |
| MCP stdio server (cmd/mcp) | JSON-RPC tool surface over stdio; auth `MARGINCE_PASSPORT_TOKEN` env-only; refuses pre-bootstrap; re-auth every call. | passport token (env) → governed tool surface capped by human seat/RBAC | all PII, deals, Approval-staging authority |
| MCP hosted transport (POST /mcp) | Streamable-HTTP, Bearer passport in Authorization → AuthenticateAgent; LimitBodies; timeouts sized to the 120s model budget. | Bearer passport (network) → governed tool surface (same gate as stdio) | all PII, deals, Approval-staging authority |
| Agent REST admission gate | In-router middleware for mutating AGENT calls; resolves chi route → generated `agentPolicies` op→tier; fail-closed on unknown/human-only → deny; 🟢 auto, 🟡 stages approval (SHA-256 canonical-body diff hash); body buffered ≤1 MB. | authenticated agent mutating request → tier decision → auto-exec \| staged human approval \| deny | all PII, deals, Approval-staging authority, RBAC gates, outbound |
| Process flags + MARGINCE_* env | The four binaries parse env-backed flags; sensitive: `--dsn` (app), `--schema-dsn` (owner DDL), `--connector-state-key`, client secrets, `--gmail-push-token`, `--gmail-jwks-url` (test override), `ANTHROPIC_API_KEY`. In SaaS these are injected by out-of-tree tooling (owner-stated). | operator/deployment config → process capability (DB privilege, connector creds, signing keys) | Bootstrap/config secrets, Vault secret custody, Connector credentials, AI BYOK keys |
| External JSON payload deserialization | `json.Unmarshal` on the agent-gate body, gmail push envelope, OIDC claims; fronted by LimitBodies/`io.LimitReader`; contract parse-fail → 422 problem+json. | untrusted wire bytes → typed Go structs (bounded, error-mapped) | Service availability |
| RLS GUC contract (WithWorkspaceTx/WithInfraTx) | Single tenant-isolation chokepoint; `SET LOCAL app.workspace_id` via parameterized `set_config`; `ErrNoWorkspace` before SQL if unbound; `WithInfraTx` = the sanctioned cross-tenant path (bootstrap, session-by-hash, outbox relay, fleet mailbox lookup). | per-request workspace context → row-level tenant isolation | RLS tenant-isolation boundary, all PII |
| Dynamic SQL identifier interpolation | ~25 sites Sprintf table/column **names** into SQL (values stay `$`-param); highest-value = customfields ALTER TABLE (confirmed guarded: regex allowlist + `pgx.Identifier.Sanitize` + slug-derived); storekit predicate/patch, auth/rbac row-scope clauses. | identifier source (allowlist vs user text) → raw SQL text | all PII, RLS tenant-isolation boundary |
| Capture connector.Sink | Single normalized-inbound sink idempotent on the source natural key; content originates from **untrusted email/calendar bodies**; the primary untrusted-content → domain-store path (stored injection / prompt-injection-feed source). | untrusted provider mail/calendar payload → normalized → domain store | Activity timeline, Person/organization/lead PII, Captured raw payloads, Deal/pipeline/revenue data |
| Capture connectors (Gmail/IMAP/GCal/Graph) | Per-provider Sync/Backfill/Normalize; tenant-supplied host (IMAP); `netguard` SSRF guard wired across clients; IMAP transient creds not persisted. | external mail/calendar server + provider creds → normalized record → Sink | Connector credentials, Captured raw payloads, Service availability, internal network (SSRF) |
| AI model egress (BYOK) | Anthropic cloud BYOK / Ollama local / offline fake; SecretStripper before the wire; opt-in payload capture stores only post-stripping; routing/budget capped; hand-rolled Anthropic client (no SDK). | internal CRM/customer data (incl. captured untrusted content) → external LLM API (secret-stripped, budget-capped) | all PII, AI BYOK keys, Vault secret custody, Consent records |
| Dev stack host ports | docker-compose dev binds PG 55432 / Redis 56379 (no auth) / MinIO 59000-1 (minioadmin) on the host. Local-development only (owner-stated: not the SaaS runtime). | host network → data stores holding all tenant data + blobs | all PII, Write-shape+outbox integrity, Attachments+blobstore |
| Bootstrap config file | First-boot org+admin from a plaintext on-disk `margince.yaml` + admin-password file; persists across teardown; the admin sees every record. | local filesystem → application superuser identity | Bootstrap/config secrets, RBAC seat/scope + governance gates, all PII |
| cmd/migrate owner DSN | Full-DDL owner (`margince_owner`) entry point for schema/migrations/seeds; also the runtime-DDL pool for customfield ALTER TABLE. | operator/CI → full schema authority (ALTER/DROP, bypass grants) | RLS tenant-isolation boundary, all PII, Audit-log integrity |
| GitHub Actions CI | Fork PRs run the same gates and execute PR-authored code in integration/live-boot/frontend/uat; the token is dropped there, kept on diff-scoped gate jobs; actions/images SHA-pinned. | untrusted PR contributor → CI runner + GITHUB_TOKEN/secrets | Write-shape+outbox integrity (main branch), CI secrets |
| Renovate auto-merge | gomod patch/minor auto-merge to `main` with no human review once CI is green; `platformAutomerge` on; the digest-pin gate does **not** cover Go modules. | upstream Go dependency author → main branch | Write-shape+outbox integrity (main branch), host process (all data) |
| margince_app runtime DB role | The role api/worker/mcp connect as; table-wide DML on all present + future tables + ALTER DEFAULT PRIVILEGES; UPDATE/DELETE revoked on `audit_log` only; isolation rests 100% on RLS + GUC. | app process → all-tenant data (RLS-gated) | all PII, RLS tenant-isolation boundary, Write-shape+outbox integrity |

## 4. Threats

Sorted by (impact, likelihood) descending. IDs are stable across edits — after this
interview pass some rows carry earlier IDs out of numeric order (notably T4, which the
owner down-ranked once deployment was confirmed as SaaS-managed).

| id | threat | actor | surface | asset | impact | likelihood | status | controls | evidence |
|---|---|---|---|---|---|---|---|---|---|
| T1 | Cross-tenant data exposure via a query path that runs without the per-request workspace GUC bound | remote_auth | RLS GUC contract (WithWorkspaceTx/WithInfraTx), Gmail push webhook, margince_app runtime DB role | RLS tenant-isolation boundary, Person/organization/lead PII, Deal/pipeline/revenue data | critical | possible | partially_mitigated | FORCE + deny-on-unset RLS on every workspace_id table; WithWorkspaceTx fails closed (ErrNoWorkspace) before SQL; composite (workspace_id,col) FKs; RLS/FK coverage fitness tests; only ~5 sanctioned WithInfraTx cross-tenant paths (the fleet mailbox walk is the design-sensitive one) | 17646ca |
| T2 | Session or passport theft/forgery leading to full impersonation of the human or agent | remote_unauth | /v1 contract API, MCP hosted transport (POST /mcp), OAuth authorization server (/oauth/*), Connector OAuth callback | Session tokens, Passport tokens (mgp_) | critical | possible | partially_mitigated | crm_session HttpOnly+Secure+SameSite=Strict; session and passport hashes SHA-256 at rest; passport re-authenticated every call with live revoked/expiry/user-active checks; connector callback state HMAC-signed + CSRF cookie | |
| T3 | Supply-chain compromise via unreviewed dependency auto-merge or a vulnerable inbound-mail parser | supply_chain | Renovate auto-merge, GitHub Actions CI, Capture connectors (Gmail/IMAP/GCal/Graph) | Write-shape + outbox integrity, Service availability, Person/organization/lead PII | critical | possible | risk_accepted | govulncheck merge gate + Renovate vulnerabilityAlerts; CI actions/images SHA/digest-pinned; persist-credentials:false on PR-code jobs; **Owner risk-accepted for the PoC.** NB: code at 2bd8673 still shows renovate.json automerge:true (gomod patch/minor) and go.mod pinning go-imap/v2 v2.0.0-beta.8 on the untrusted-mail path — the owner's "already addressed" belief was not confirmed in code (see §6) | |
| T5 | Agent or user reads/writes records outside RBAC and row-scope via the HubSpot overlay/mirror path | remote_auth | Agent REST admission gate, MCP stdio server (cmd/mcp), MCP hosted transport (POST /mcp), /v1 contract API | Person/organization/lead PII, Deal/pipeline/revenue data, RLS tenant-isolation boundary | high | likely | partially_mitigated | RBAC-at-store-entry, existence-hiding 403/404, EnsureVisible row-scope — but the overlay read/write path bypassed object RBAC and the write guard; the dedupe queue admitted one-sided pairs. Owner unsure whether overlay mode is enabled in production (§6) — likelihood kept at 'likely' pending confirmation | e5f6fc3, 91d6342 |
| T6 | Prompt injection: untrusted captured/mirror content loses its trust label and steers agent tool-calls into unintended actions | remote_unauth | Capture connector.Sink, AI model egress (BYOK), Agent REST admission gate | Person/organization/lead PII, Deal/pipeline/revenue data, Approval-staging authority | high | possible | partially_mitigated | Grounding content spotlighted as data-not-instructions; T2 untrusted trust label (was dropped once); 🟡 confirm-first on outbound/irreversible | e5f6fc3 |
| T7 | Server-side request forgery to internal or cloud-metadata endpoints via a tenant-supplied connector host | remote_auth | Capture connectors (Gmail/IMAP/GCal/Graph), Connector OAuth callback, Process flags + MARGINCE_* env | Vault secret custody, Connector credentials, Service availability | high | possible | partially_mitigated | netguard.RefusePrivate on resolved IP (DNS-rebind-safe; NAT64/0.0.0.0/8 ranges) wired across connectors + webread + crawler; connect-time scope authorization; port-range validation (all added post-incident) | 807b923, ffe6206 |
| T8 | Agent executes an untiered or over-scoped mutation exceeding the granting human's authority | remote_auth | Agent REST admission gate, MCP stdio server (cmd/mcp), MCP hosted transport (POST /mcp) | RBAC seat/scope + governance gates, Person/organization/lead PII, Deal/pipeline/revenue data | high | possible | partially_mitigated | Effective authority = passport scopes ∩ human RBAC ∩ seat; untiered mutation default-denied; agent-policy generator refuses to ship an untiered mutation (drift-lint); read/full seat ceiling. **Owner: per-agent quota intentionally left unbounded** — per-run ceilings (MaxSteps 40, 50k tokens) + agent≤human scopes are the only limits; the authority ceiling holds, the cost/DoS exposure is accepted (see T17) | |
| T9 | Non-consented outbound communication via a send path that bypasses the consent suppression gate | remote_auth | AI model egress (BYOK), /v1 contract API, Capture connector.Sink | Consent records, DSR / GDPR case data, Person/organization/lead PII | high | possible | partially_mitigated | Default-deny consent.Gate injected at the composition root; per-purpose check blocks unknown/unresolved/withdrawn; double-opt-in confirmed round-trip; append-only consent proof (consentproof_test) | |
| T10 | Account takeover or user enumeration via the password-reset flow | remote_unauth | /v1 contract API | Session tokens, Single-use auth tokens, Password hashes | high | rare | mitigated | A74: STARTTLS-required mailer; enumeration-resistant off-request account path; request throttling; hashed single-use short-TTL tokens; reset-token scrubbed from URL | 5f3b055 |
| T11 | Disclosure of secrets or internal error detail via config-on-disk or unmapped upstream error paths | remote_auth | Process flags + MARGINCE_* env, /v1 contract API, AI model egress (BYOK) | AI BYOK provider keys, Bootstrap/config secrets | high | rare | mitigated | BYOK keys env-only (no api_key in routing config; stray line = parse error); errors classified by SQLSTATE not Error() text; digest failures now return opaque 500; no stack/SQL/table names to client | d8575ec, 91d6342, 5f3b055 |
| T12 | Forged or replayed confirm-first (🟡) approval executes an unauthorized irreversible/outbound action | remote_auth | Agent REST admission gate, /v1 contract API | Approval-staging authority, Workspace signing keys | high | rare | partially_mitigated | Approval single-use, 15-min window, bound to staging passport + content diff_hash + target version (refused on skew); approver must hold the RBAC the effect needs; human-only surface rejects agents (no self-approval); Ed25519 JWS; GAP: some redeem-then-execute effects not retriable | |
| T13 | Audit-log or provenance tampering / write-shape bypass defeats accountability | insider | /v1 contract API, RLS GUC contract (WithWorkspaceTx/WithInfraTx), margince_app runtime DB role | Audit-log integrity, Write-shape + outbox integrity | high | rare | mitigated | Single-tx storekit Audit+Emit; audit_log immutable two ways (trigger RAISE + REVOKE UPDATE,DELETE from margince_app); captured_by server-stamped never from body; writeshape_test fitness gate | |
| T14 | Incomplete GDPR erasure/retention leaves subject data recoverable or destroys it below a statutory floor | remote_auth | /v1 contract API, Capture connector.Sink | DSR / GDPR case data, Captured raw payloads | high | rare | partially_mitigated | Art.17 eraser (anonymize + purge raw/embeddings/attachments + suppression-hash so re-capture can't resurrect + PII-free tombstone), refuses legal_hold; nightly retention evaluator honors DE/GoBD floors and holds activities transitively; tableownership_test; GAP: extraction:accept notes carry no idempotency key | |
| T15 | SQL injection via a dynamically interpolated identifier in a query | remote_auth | Dynamic SQL identifier interpolation | Person/organization/lead PII, RLS tenant-isolation boundary | high | rare | mitigated | Interpolated identifiers are slug-derived + regex-allowlisted (^[a-z_][a-z0-9_]*$, ≤63 bytes) then pgx.Identifier.Sanitize-quoted; values always $-parameterized; customfields ALTER path confirmed defended (BuildDDL + engine_test + privilege_boundary_test) | |
| T16 | Unauthorized schema change or privilege escalation via the owner DDL path | local_admin | cmd/migrate owner DSN, Dynamic SQL identifier interpolation | RLS tenant-isolation boundary, RBAC seat/scope + governance gates | high | rare | mitigated | customfields is the sole runtime ALTER chokepoint, RBAC-gated (privilege_boundary_test); the owner DSN is used only by migrate/DDL, never by the request-serving app role; margince_app has no BYPASSRLS and does not own tables | |
| T4 | Production compromise via committed static dev secrets or the dev trust-switch reused/left enabled in production | remote_unauth | Bootstrap config file, Process flags + MARGINCE_* env, Dev stack host ports, cmd/migrate owner DSN, margince_app runtime DB role | Vault secret custody, Bootstrap/config secrets, RLS tenant-isolation boundary | high | rare | partially_mitigated | Owner-stated: deployment is Gradion-operated SaaS — DB-role provisioning, TLS, and secret injection are managed out-of-tree, so the committed dev keyvault/connector-state keys and MARGINCE_ENV=dev do not reach production. BYOK/model keys and passports env-only. Residual: the out-of-tree SaaS provisioning/secret-injection tooling is unverifiable from this repo (§6); the static dev secrets in scripts/dev.sh remain for local use only | |
| T17 | Denial of service via resource exhaustion or an unrecovered panic across ingest/parse/crawl/aggregation lanes | remote_unauth | Capture connector.Sink, Capture connectors (Gmail/IMAP/GCal/Graph), AI model egress (BYOK), /v1 contract API, External JSON payload deserialization | Service availability | medium | likely | partially_mitigated | LimitBodies global cap; 2 MiB IMAP literal cap; per-goroutine panic recovery; Surface-B per-run MaxSteps/token budget; big.Rat input guards; /ai/usage window validation + 366-day cap. NB: per-agent *aggregate* quota is intentionally unbounded (owner, T8) — agent runs are capped per-run but not across a workspace | eab2bf8, 2bd8673, 91d6342, 807b923 |
| T18 | Financial data-integrity corruption via malformed numeric/money fields at the capture/sync boundary | remote_unauth | Capture connector.Sink | Deal/pipeline/revenue data | medium | possible | partially_mitigated | Strict money parsing (reject non-finite/overflow/rational/hex); amount_minor integer storage; FX-freeze | e5f6fc3, 8bb3cec |
| T19 | Malware distribution via an uploaded or captured attachment served to users | remote_auth | /v1 contract API, Capture connector.Sink | Attachments + blobstore | medium | possible | partially_mitigated | Uploads default scan_status=scanning and are undownloadable until a verdict (fail-closed). **Owner-confirmed live gap: no scanner product is wired behind the seam in any deployment**, so nothing ever clears an upload and the every-download-audit clause is not ported; captured-attachment behaviour still undefined | |
| T21 | Forged inbound webhook injects fabricated capture or triggers unauthorized mailbox sync | remote_unauth | Gmail push webhook | Activity timeline, Captured raw payloads, Service availability | medium | rare | mitigated | Endpoint mounted only when --gmail-push-token set; constant-time (crypto/subtle) token compare; optional Google OIDC verification vs audience + service-account; throttled JWKS refresh; 1 MB body cap | 5e92509, 9ab3225 |
| T22 | Organization lockout or privilege abuse via admin user-management operations | remote_auth | /v1 contract API | RBAC seat/scope + governance gates, Service availability | medium | rare | mitigated | Last-admin guard with FOR UPDATE covering all admin rows; admin-gated roster/controls; invite email+name validation; reactivate no longer clears suspended/lockout | ab1f826, 7c7cc87, 2b8f2df |
| T20 | Credential brute-force or rate-limit bypass behind a reverse proxy sharing one peer IP | remote_unauth | /v1 contract API, OAuth authorization server (/oauth/*) | Session tokens, Password hashes | medium | rare | mitigated | Login/bootstrap limiters key on the direct peer address and deliberately refuse attacker-controlled X-Forwarded-For; **owner-stated: the SaaS reverse proxy/WAF enforces per-client throttling in front of /v1 and /oauth** (§6 to verify the proxy keys per-client, not per shared egress IP) | |
| T23 | Information disclosure of deployment topology and internal metrics via unauthenticated operational probes | remote_unauth | /healthz /readyz /metrics probes | Service availability | low | possible | mitigated | Unauthenticated by design, but **owner-stated: /metrics and /readyz are restricted to an operator network in the SaaS deployment**, not internet-exposed (§6 to verify network policy) | |
| T24 | Abuse of anonymous public edges (booking/preference tokens) for enumeration, spam, or unsubscribe forgery | remote_unauth | /v1/public/* anonymous edges | Person/organization/lead PII, Consent records | low | possible | partially_mitigated | Each edge resolves its own token/slug→tenant, applies its own rate limiter, binds a confined system principal, and stays workspace-bound | |

## 5. Deprioritized

| threat | reason |
|---|---|
| Cross-tenant access via a tenant selector on the wire | Not present: single-org-per-install (A107/ADR-0061); the server resolves its singleton workspace itself, no request selects a tenant |
| Multiple-organization bootstrap race (attacker creates a second org on first boot) | Ruled out: first boot runs under a Postgres advisory lock; 0 workspaces → create, 1 → bind, >1 → refuse |
| DoS against a dev/local deployment | Explicitly out of scope per SECURITY.md; dev-mode trust relaxations are non-production (owner confirms SaaS is the production tier) |
| MARGINCE_ENV=dev trust relaxations exploited in a dev environment | Out of scope per SECURITY.md for dev; the production-reuse case is retained (T4) but down-ranked since the owner confirms SaaS-managed config |
| Third-party dependency CVEs without demonstrated in-repo impact | Parked per SECURITY.md and tracked by the govulncheck merge gate; the specific untrusted-parse path (go-imap beta) is retained under T3 |
| CSRF on session-authenticated state-changing endpoints | Mitigated by SameSite=Strict on crm_session + SecureHeaders + the connector-callback CSRF cookie; no residual gap identified in code |
| Repudiation of privileged actions | Structurally closed by the append-only immutable audit_log (trigger + REVOKE) with server-stamped actor; folded into T13 rather than listed separately |
| T3 supply-chain — owner risk-acceptance | Owner explicitly accepted the gomod-auto-merge + go-imap-beta risk for the PoC (govulncheck/vulnerabilityAlerts as backstop). Retained as a ranked, visible row in §4 with status `risk_accepted` rather than removed, because it is a live critical-impact surface; see §6 for the code/claim discrepancy to reconcile |

## 6. Open questions

Interview follow-ups: owner statements that affect a score and were not (or could not
be) confirmed in code. Each seeds a targeted code/config/infra check.

- **[Owner-states]** Production is Gradion-operated SaaS with DB-role provisioning, TLS, and secret injection managed outside this repo. Affects: T4 (impact critical→high, likelihood possible→rare, status→partially_mitigated). Verify by: reviewing the out-of-tree SaaS deployment/secret-injection tooling — confirm per-install keyvault + connector-state keys are generated (not the committed `scripts/dev.sh` defaults) and that `MARGINCE_ENV` is never `dev` in production.
- **[Owner-states]** `/metrics` and `/readyz` are restricted to an operator network. Affects: T23 (status→mitigated). Verify by: checking the SaaS ingress/network policy so no public route reaches the probe/metrics listener.
- **[Owner-states]** A reverse proxy/WAF enforces per-client rate limiting in front of `/v1` and `/oauth`. Affects: T20 (status→mitigated, likelihood→rare). Verify by: confirming the proxy throttles per client identity (not just per shared egress IP) and that the app still observes the true peer address.
- **[Owner-claim contradicted by code]** Owner initially believed T3 (Renovate gomod auto-merge + go-imap beta pin) was already fixed. Code at `2bd8673` shows `renovate.json` still has `automerge:true` for gomod patch/minor and `go.mod` still pins `go-imap/v2 v2.0.0-beta.8`. Owner then risk-accepted. Affects: T3 (status→risk_accepted). Verify by: re-checking `renovate.json` + `go.mod` when any pending fix merges; re-raise the status if the beta parser remains on the untrusted-mail path.
- **[Owner-uncertain]** Whether the HubSpot overlay/mirror mode is enabled in production is unknown. Affects: T5 (likelihood held at `likely`). Verify by: checking which SaaS tenants have overlay enabled and what the mirror exposes; downgrade to `possible` if it is dev-only.
- **[Owner-states]** Per-agent quota is intentionally unbounded (only per-run ceilings + agent≤human scopes apply); cost/DoS exposure accepted. Affects: T8 controls, T17 residual. Verify by: confirming the per-run ceilings (MaxSteps/token budget) are actually enforced on *every* agent surface (Surface A + B) so an unbounded aggregate can't be reached one run at a time.
- **[Owner-states, code-confirmed]** No attachment scanner is wired in any deployment. Affects: T19 (kept live). Decide: the intended behaviour for captured (vs uploaded) attachments, then wire a scanner behind the seam or make the download block permanent and audited.

## 7. Provenance

- mode: interview
- date: 2026-07-21
- inputs: --seed THREAT_MODEL.md (bootstrap draft); no --design-doc; no --vulns
- owner: present (an.ngo@gradion.com, Gradion)

## 8. Recommended mitigations

| mitigation | threat_ids | closes_class | effort |
|---|---|---|---|
| Route every overlay/mirror read and write through the same auth.Require + EnsureVisible gate as the native store (make the overlay a store honoring the RBAC contract; extend rbacgate_test to overlay entry points) | T5 | yes | M |
| Force every outbound tenant-host dialer through one shared netguard-wrapped client factory and authorize connector scope before any dial; fail a client built without the Control hook | T7 | yes | M |
| Wire an attachment scanner behind the scan seam (or make the download block permanent and audited) and define captured-attachment behaviour | T19 | yes | M |
| Make the T2 untrusted trust label a non-droppable end-to-end property carried into every agent-visible payload; add a fitness test asserting captured/mirror content is never emitted untagged | T6 | partial | M |
| Add a fitness function asserting every WithInfraTx callsite binds a workspace GUC per row or sits on a reviewed allowlist (keep RLS/FK coverage tests authoritative) | T1 | partial | M |
| Enforce the specified per-agent quota, OR — per the owner's accept-unbounded decision — document the accepted cost/DoS exposure and guarantee the per-run ceilings hold on every agent surface | T8, T17 | partial | M |
| Add an idempotency key to extraction:accept and to any redeem-then-execute approval effect so retries neither duplicate provenance nor drop approved effects | T12, T14 | partial | S |
| Parse all externally-sourced money through one strict decimal→minor-units helper that rejects non-finite/overflow/non-decimal forms; forbid float64 for currency | T18 | yes | S |
| Keep the default-deny consent gate injected on every outbound path at the composition root and add a fitness test that no send bypasses it | T9 | partial | M |
| Preserve the audit-log immutability + single-tx write shape and error-code classification as fitness gates (regression guard) | T11, T13 | yes | S |
| Verify and codify the out-of-tree SaaS provisioning (per-install keys, enforced TLS, no dev trust-switch) and keep it under review; document it so it is not invisible to future threat models | T4 | partial | L |
| Reconcile T3: either require human review for gomod merges + move go-imap off the v2 beta pin, or formally record the risk-acceptance with an owner and a review date | T3 | partial | S |
| Network-restrict /metrics and /readyz to the operator plane (owner states this holds in SaaS — codify it as a deployment invariant) | T23 | yes | S |
