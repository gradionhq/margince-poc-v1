# Connect a mailbox for capture

Connect a mailbox so Margince captures its mail onto the timeline — creating people, organizations, and
activities through the one dedupe chokepoint. This guide is **UI-first**: you drive it from the app,
with the equivalent `curl` shown alongside for scripting and verification. It covers the three paths you
can drive from the **UI** — **Gmail over OAuth** (a standing connection with background sync + backfill),
**IMAP one-shot pull** (a transient capture, which is how you reach a **Gmail** or **Outlook /
Microsoft 365** mailbox with an app-password), and **Graph OAuth** for Outlook / Microsoft 365 (a standing
connection, Path C) — plus **Google Calendar (`gcal`)**, a separate standing connection you add
alongside Gmail from the same Settings surface (Path D). For the mental model — the connector seam, the
one Sink, the three ingestion modes, credential custody — read
[explanation/capture-connectors.md](../explanation/capture-connectors.md) first.

> **Single-organization installation (ADR-0061/A107).** One installation serves one organization; the
> server resolves it itself, so no request selects a tenant — the `curl`s below carry only the session
> cookie. ("Workspace" still names the internal RLS tenant these tables are scoped by.)

## Where the UI lives

Two entry points, both hitting the same API:

- **Settings → Integrations** (`ConnectorsCard`) — the standing-connection roster: each live connection
  with a status badge (`connected` / `reauth_required` / `error`) and last-synced time, a **reconnect**
  action for a `reauth_required` OAuth connection, and a confirm-gated **disconnect**. Below the roster
  (or in its place, when nothing is connected yet) sits an always-present **"Add a connection"**
  affordance offering whichever of **Gmail**, **Google Calendar**, **Microsoft 365**, and **IMAP** aren't
  already connected. Picking an OAuth provider (Gmail / Calendar / Microsoft) redirects straight to that
  provider's consent screen from Settings — no detour through onboarding; picking IMAP opens the inline
  connect form right there. If a provider's backend app isn't configured, the button click surfaces
  **"{provider} isn't configured in this deployment"** instead of a raw error.
- **Onboarding → connect step** — the same connect step, reached on a fresh install (or via the
  `onboarding / connect` command): chips for **Google**, **Microsoft**, and **IMAP** (Google Calendar has
  no onboarding chip — add it from Settings). All three chips are live; Microsoft opens the same Graph
  OAuth redirect Settings does.

> **Restart after backend config.** The api is a compiled binary — changing an OAuth env var (below)
> needs `make dev` again to take effect; Vite hot-reloads the SPA but not the Go api.

## Which path do I want?

| Provider | Path | Persisted? | Background sync + backfill | What you need |
|---|---|---|---|---|
| **Gmail** | OAuth standing connection | Yes | Yes (+ Pub/Sub push) | a Google OAuth app + the vault key |
| **Gmail** | IMAP one-shot pull | No | No (re-run to capture more) | a Google **app-password** |
| **Outlook / M365** | IMAP one-shot pull | No | No | an Outlook **app-password** |
| **Outlook / M365** | Graph OAuth standing connection | Yes | Sync + backfill (poll-only) | a Microsoft Entra app + the vault key |
| **Google Calendar** | `gcal` OAuth standing connection (separate from Gmail) | Yes | Sync only (poll-only, no backfill) | the same Google app as Gmail, with the calendar scope + redirect URI added |

Start with **IMAP one-shot** if you just want to see capture work against a real mailbox from the UI — it
needs no OAuth app and no deployment config. Use **Gmail OAuth** to exercise the standing connection,
background sync, and backfill.

---

## Path A — Gmail over OAuth (standing connection)

### A1. Prerequisites (operator config)

Connecting is `x-agent-access: human-only` — you must be a signed-in human; an agent Passport is
refused, by design. Gmail OAuth also needs operator setup that the UI can't do for you:

- **A Google OAuth app** (Google Cloud project → *APIs & Services → Credentials → OAuth client ID → Web
  application*) with the **Gmail API** enabled, the `.../auth/gmail.readonly` scope on the consent screen
  (read-only — Margince never requests send/modify), and an **authorized redirect URI** of
  `<api-base>/v1/connectors/gmail/callback` (dev: `http://localhost:8080/v1/connectors/gmail/callback`).
- **The vault key** — the refresh token is sealed at rest; the connect flow refuses without it.

Set these on **both** the api and worker (the api connects, the worker syncs), then `make dev`:

```sh
export MARGINCE_GMAIL_CLIENT_ID="<google-oauth-client-id>"
export MARGINCE_GMAIL_CLIENT_SECRET="<google-oauth-client-secret>"
export MARGINCE_CONNECTOR_STATE_KEY="$(openssl rand -hex 32)"   # ≥32 bytes; signs the OAuth state
export MARGINCE_KEYVAULT_ROOT_KEY="$(openssl rand -base64 32)"  # base64 of exactly 32 bytes
export MARGINCE_PUBLIC_BASE_URL="http://localhost:8080"         # post-consent landing + default callback base
# Optional — near-real-time Gmail (else it runs on the 2-minute sync poll):
# export MARGINCE_GMAIL_PUBSUB_TOPIC="projects/<p>/topics/<t>"  # worker: enables push-watch
# export MARGINCE_GMAIL_PUSH_TOKEN="$(openssl rand -hex 16)"    # api: enables POST /webhooks/gmail-push
```

Without the client id/secret + state key + public base URL, `/connectors/gmail/*` stays its declared
`501` and clicking **Gmail** in the Add-a-connection panel shows "Gmail isn't configured in this
deployment" instead of redirecting. The full table is
[reference/configuration.md → Capture connector OAuth](../reference/configuration.md).

### A2. Connect from the UI

1. Open the app, go to **Settings → Integrations**, and click **Gmail** in the **Add a connection**
   footer or empty state (or click the **Google** chip on the onboarding connect step, on a fresh
   install).
2. The page redirects to Google — sign in and consent to the read-only Gmail scope.
3. Google returns you to the app; the panel **proves** the connection by re-reading `GET /connectors`
   and shows a trust pill for the live `gmail` connection. Back in **Settings → Integrations** you'll now
   see a `gmail` row with a **connected** badge.

The worker's sync dispatcher (every 30s) picks the connection up on its next due tick and begins
capturing new mail incrementally.

<details><summary>Same thing via <code>curl</code></summary>

```sh
# 1. get the consent URL, open it in a browser, sign in + consent
curl -X POST http://localhost:8080/v1/connectors/gmail/connect \
  --cookie 'crm_session=<session>' -H 'Content-Type: application/json' -d '{}' \
  | jq -r '.authorize_url'

# 2. after the callback lands, confirm the standing connection
curl --cookie 'crm_session=<session>' http://localhost:8080/v1/connectors \
  | jq '.data[] | {provider, status, last_synced_at, next_sync_due_at}'
```

Doing it in the browser is smoother — it carries the CSRF cookie the callback checks automatically.
</details>

### A3. Backfill existing mail (preview before spend)

New mail flows in on the sync poll; to fill the CRM *backward* over a window, use the **backfill panel**
that appears right after a Google connect:

1. Pick a **window** — `3m` / `6m` / `12m` (default `6m`). The panel auto-**previews**: it shows the
   estimated message count and estimated AI cost. This is the consent surface and spends nothing.
2. Click **Start the import**. A live progress bar tracks scanned vs. estimated, with running counts of
   captured emails, people, and organizations created. **Cancel** keeps everything already captured.

Windows are **widen-only** (`3m` → `6m` → `12m`).

<details><summary>Same thing via <code>curl</code></summary>

```sh
curl -X POST http://localhost:8080/v1/connectors/gmail/backfill/preview \
  --cookie 'crm_session=<session>' -H 'Content-Type: application/json' -d '{"window":"6m"}' \
  | jq '{estimated_messages, estimated_cost_minor, currency, estimate_quality}'

curl -X POST http://localhost:8080/v1/connectors/gmail/backfill \
  --cookie 'crm_session=<session>' -H 'Content-Type: application/json' -d '{"window":"6m"}' | jq '.state'

curl --cookie 'crm_session=<session>' http://localhost:8080/v1/connectors/gmail/backfill \
  | jq '{state, estimated_messages, counts}'   # state: queued → running → done
```
</details>

---

## Path B — IMAP one-shot pull (Gmail or Outlook, with an app-password)

The IMAP path dials a mailbox over IMAPS, pulls the most-recent messages, and captures each — using the
credentials for **this call only**. Nothing is persisted (no stored connection, no background sync); to
capture more, run it again. It needs **no operator config and no vault** — you can do it from the UI
immediately.

### B1. Get an app-password

Basic-auth IMAP with your normal password is blocked by both providers — you need an **app-password**
(which requires 2-step verification on the account):

- **Gmail** — enable 2-Step Verification, then *Google Account → Security → App passwords* → generate one
  for "Mail". Host `imap.gmail.com`, port `993`.
- **Outlook / Microsoft 365** — enable two-step verification, then *Security → Advanced security options
  → App passwords* → create one. Host `outlook.office365.com`, port `993`. (If your tenant disables IMAP
  or app-passwords, use the Graph OAuth path — Path C.)

### B2. Pull from the UI

1. **Settings → Integrations** → click **IMAP mailbox** in the **Add a connection** footer or empty state
   (or the **IMAP** chip on the onboarding connect step).
2. Fill the form: **host** (`imap.gmail.com` or `outlook.office365.com`), **email** (the mailbox
   address / login), **password** (the app-password), **mailbox** (`INBOX`), **max messages** (default
   `30`, capped at `200`).
3. Submit. The result panel shows the tally: **captured** (landed as timeline activities), **contacts**
   (distinct counterparties), and **skipped** (automated/system mail). Click **Enter CRM** to see them.

<details><summary>Same thing via <code>curl</code></summary>

Read the app-password **silently** and build the JSON on stdin, so the secret never lands in your shell
history or a process listing (mirroring the "never logged" guarantee):

```sh
read -rsp 'IMAP app-password: ' APP_PW; echo    # silent — never in argv/history
jq -n --arg pw "$APP_PW" \
  '{host:"imap.gmail.com", port:993, email:"you@gmail.com", password:$pw, mailbox:"INBOX", max_messages:50}' \
| curl -X POST http://localhost:8080/v1/connectors/imap/connect \
    --cookie 'crm_session=<session>' -H 'Content-Type: application/json' --data @- \
| jq '{connected, mailbox, captured, skipped, contacts}'
unset APP_PW
```

For Outlook, set `email` to your `@outlook.com` / tenant address and `host` to `outlook.office365.com`.
</details>

Failure modes are honest and leak no internals — the form surfaces them directly:

- **credentials rejected** (`422 imap_login_rejected`) — wrong host/email/password, or a normal password
  where an app-password is required.
- **server unreachable** (`502 imap_unreachable`) — DNS / TCP / TLS / timeout.

### B3. Local-testing gotcha — no localhost mailserver

The IMAP dialer is **SSRF-guarded** (`netguard.RefusePrivate`): it refuses to dial any private,
loopback, or reserved address, checked post-DNS on the concrete IP so a DNS-rebind can't bypass it.
So you **cannot point it at a mailserver on `127.0.0.1`, `localhost`, or a private-range host** — it
comes back "server unreachable" by design. Test against a **public** IMAP server (a real Gmail/Outlook
mailbox is easiest). This is a security guard, not a bug.

---

## Path C — Outlook / Microsoft 365 over Graph (standing connection)

Graph is the richer Outlook path — a standing connection with delta-cursor sync and backfill — but it is
**poll-only** (no push subscription built yet). The shape mirrors Path A, and it now has the same
first-connect UI: an onboarding **Microsoft** chip and a Settings **Add a connection** button.

### C1. Prerequisites (operator config)

```sh
export MARGINCE_GRAPH_CLIENT_ID="<entra-app-id>"
export MARGINCE_GRAPH_CLIENT_SECRET="<entra-app-secret>"
export MARGINCE_GRAPH_TENANT="common"   # or a specific tenant id
# plus the same MARGINCE_CONNECTOR_STATE_KEY / MARGINCE_KEYVAULT_ROOT_KEY / MARGINCE_PUBLIC_BASE_URL as A1
```

Register a Microsoft Entra (Azure AD) app with delegated scopes `offline_access User.Read Mail.Read` and
a redirect URI of `<api-base>/v1/connectors/graph/callback`, then `make dev` to pick up the env.

### C2. Connect from the UI

1. Either click the **Microsoft** chip on the onboarding connect step, or go to **Settings →
   Integrations** and click **Microsoft** in the **Add a connection** footer (or empty state).
2. The page redirects to Microsoft — sign in and consent to `offline_access User.Read Mail.Read`.
3. Microsoft returns you to the app; **Settings → Integrations** shows a `graph` row with a **connected**
   badge, reconnect/disconnect actions, and the backfill panel.

<details><summary>Same thing via <code>curl</code></summary>

```sh
curl -X POST http://localhost:8080/v1/connectors/graph/connect \
  --cookie 'crm_session=<session>' -H 'Content-Type: application/json' -d '{}' | jq -r '.authorize_url'
```

Consent in the browser; the callback seals the refresh token and the worker syncs it.
</details>

### C3. Backfill

Backfill (`/connectors/graph/backfill*`) works exactly as in [A3](#a3-backfill-existing-mail-preview-before-spend) —
same window/preview/progress panel, just on the `graph` connection.

---

## Path D — Google Calendar over OAuth (standing connection, separate from Gmail)

Google Calendar (`gcal`) is a **second, independent** standing connection, not a mode of the Gmail one.
It reuses the same Google OAuth app as Path A but requests only `calendar.readonly` as its own
authorization (deliberately never `include_granted_scopes`), so the calendar grant never inherits — and
never bleeds into — the Gmail mail-read grant. **Connecting both means signing two separate Google
consent screens** — a deliberate least-privilege split, not a rough edge.

### D1. Prerequisites (operator config)

Same env vars as [A1](#a1-prerequisites-operator-config) (`MARGINCE_GMAIL_CLIENT_ID/SECRET`,
`MARGINCE_CONNECTOR_STATE_KEY`, `MARGINCE_KEYVAULT_ROOT_KEY`, `MARGINCE_PUBLIC_BASE_URL`) — Calendar rides
the same Google OAuth app as Gmail. On that app's Google Cloud project, additionally enable the
**Calendar API** and add `<api-base>/v1/connectors/gcal/callback` (dev:
`http://localhost:8080/v1/connectors/gcal/callback`) as an authorized redirect URI.

### D2. Connect from the UI

1. Go to **Settings → Integrations** and click **Google Calendar** in the **Add a connection** footer (or
   empty state) — there is no onboarding chip for Calendar, so Settings is the only first-connect path.
2. The page redirects to Google — sign in and consent to the read-only Calendar scope (a separate consent
   screen from Gmail's, even if you're already connected to Gmail).
3. Google returns you to the app; **Settings → Integrations** shows a `gcal` row with a **connected**
   badge. There is no backfill panel for Calendar — it syncs forward from connect time only.

<details><summary>Same thing via <code>curl</code></summary>

```sh
curl -X POST http://localhost:8080/v1/connectors/gcal/connect \
  --cookie 'crm_session=<session>' -H 'Content-Type: application/json' -d '{}' \
  | jq -r '.authorize_url'
```
</details>

---

## Verify end-to-end

1. **The mailbox connected.** For Gmail/Graph/gcal, **Settings → Integrations** shows a `connected` row
   (or `GET /connectors`); for IMAP, the result panel shows `connected` with a non-zero **captured**.
2. **Mail became timeline activities.** Open a captured counterparty's timeline (or `GET /activities`)
   and confirm each message is an email activity, provenance-stamped `connector:<name>`.
3. **People and organizations were auto-created.** A new external counterparty becomes a person (and, for
   a non-freemail sender, a domain-named organization + employment edge) through the dedupe chokepoint;
   a fuzzy near-match lands in the dedupe review queue rather than duplicating.
4. **The credential is never echoed.** No read surface returns a secret — the roster carries only
   `credential_ref` server-side; the IMAP password is used once and never stored.
5. **A failure degrades, never kills.** Revoke/expire a Gmail token and the row goes `reauth_required`
   with a **reconnect** action in Settings; point IMAP at an unreachable host and get a clean failure —
   no internals leaked.
6. **Backfill is preview-before-spend and widen-only.** The preview spends nothing; a narrower window
   after a wider one is refused (`409 window_narrowing`); cancel keeps captured rows.
7. **The IMAP SSRF guard holds.** A `127.0.0.1` / private-host IMAP target fails as "unreachable" —
   never a successful dial to an internal service.

## Current UI gaps

The connect UI is now live for all four connectors: Gmail, Google Calendar, Graph, and IMAP each have a
first-connect affordance from **Settings → Integrations**, and Gmail, Microsoft, and IMAP have one from
**onboarding** too (Google Calendar is Settings-only — there's no onboarding chip for it). The roster and
backfill panel, though, only apply to Gmail and Graph: IMAP is a one-shot pull with no standing
connection to roster and no backfill to run, and Google Calendar has no backfill (it syncs forward from
connect time only — see the [Calendar section](#d2-connect-from-the-ui) above). See
[explanation/capture-connectors.md → Honest limitations](../explanation/capture-connectors.md#honest-limitations)
for what's still scoped out of the pipeline overall.
