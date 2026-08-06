# Deploying Margince (self-hosting)

Margince ships deployment-target-agnostic container materials: you can run it on
any container platform (Kubernetes, Nomad, Docker Compose, a plain host). This
repo carries only the **generic** pieces; a concrete deployment (its domain,
secrets, platform manifests) is yours to own — keep those in your own infra repo.

## What ships in this repo

| File | Purpose |
|---|---|
| `Dockerfile.api` | `cmd/api` (HTTP) + bundled `cmd/migrate`; applies migrations at boot |
| `Dockerfile.worker` | `cmd/worker` — outbox relay, retention, Surface-B AI (no HTTP) |
| `Dockerfile.web` | the Vite SPA behind nginx-unprivileged |
| `scripts/deploy/api-entrypoint.sh` | migrate as owner, then serve the API as app |
| `scripts/deploy/worker-entrypoint.sh` | start the worker as app (no owner credential) |
| `scripts/deploy/db-bootstrap.sql` | one-time DB role + database + extension setup |
| `frontend/nginx.conf` | SPA static serving (listens on 8080, non-root) |

All three images build with the **repo root** as context (the Go build folds in
the `extensions/*` packs via `gen-composition`):

```bash
docker build -f Dockerfile.api    -t margince-api:local .
docker build -f Dockerfile.worker -t margince-worker:local .
docker build -f Dockerfile.web    -t margince-web:local .
```

## The two-role database model (required — read this first)

Margince enforces tenant isolation with `FORCE ROW LEVEL SECURITY`. It stops the
table **owner** from bypassing RLS, but superusers and any role with the
`BYPASSRLS` attribute still bypass it. So **both** runtime roles must be neither a
superuser nor granted `BYPASSRLS`. Two such roles are required:

- **`margince_owner`** — owns the database + tables, runs migrations (DDL) and the
  custom-fields runtime-DDL pool.
- **`margince_app`** — the runtime role the api + worker connect as. Its table
  grants are applied by migration `0015_app_role_grants`, which is a **no-op
  unless the role already exists** — so it must be created *before* the first
  migration runs.

Create the roles + database + extensions **once**, as a Postgres superuser
(pgvector is not a "trusted" extension, so a non-superuser cannot install it from
a migration):

```bash
# Pass the passwords RAW (not pre-quoted) — the script quotes/escapes them.
psql "postgres://postgres:…@<host>:5432/postgres" \
  -v owner_pw="$OWNER_PW" -v app_pw="$APP_PW" \
  -f scripts/deploy/db-bootstrap.sql
```

It is idempotent. The app containers then hold only the two non-superuser DSNs.

## Configuration — everything via the environment

The images bake in **no** instance configuration. All settings come from the
runtime environment; the binaries resolve every flag from a `MARGINCE_*` env
fallback. The full table of record is
[`docs/reference/configuration.md`](reference/configuration.md); the annotated
env template is [`.env.template`](../.env.template). The essentials:

| Var | Role | Meaning |
|---|---|---|
| `MARGINCE_OWNER_DSN` | api | owner-role DSN — migrations + custom-fields DDL (read by the entrypoint) |
| `MARGINCE_DSN` | api, worker | app-role DSN the process serves under |
| `MARGINCE_REDIS` | api, worker | Redis address (event bus / outbox relay) |
| `MARGINCE_CONFIG` | api, worker | path to the mounted `margince.yaml` (bootstrap org + admin) |
| `MARGINCE_ADMIN_PASSWORD` | api | first-boot admin password (entrypoint writes it to the file `margince.yaml` references) |
| `MARGINCE_AI_ROUTING` | api, worker | path to a mounted `ai-routing.yaml` — enables the AI lanes (plus the bound provider's BYOK key, e.g. `GEMINI_API_KEY`) |
| `MARGINCE_PUBLIC_BASE_URL` | api, worker | canonical external base URL (buyer-facing links / marketing mail) |

Do **not** set `MARGINCE_ENV=dev` in a deployed environment — it enables dev-only
trust switches.

### First-boot bootstrap config

On the first boot against an empty database the api bootstraps the organization +
admin from the file `MARGINCE_CONFIG` points to. Mount your own `margince.yaml`
(see [`config/margince.example.yaml`](../config/margince.example.yaml)) at that
path and set `MARGINCE_ADMIN_PASSWORD`. A missing config file just boots an
existing installation. Likewise mount an `ai-routing.yaml`
([`config/ai-routing.example.yaml`](../config/ai-routing.example.yaml)) and point
`MARGINCE_AI_ROUTING` at it to enable AI.

The example config declares the MCP connector (`mcp.connector_enabled: true`)
so a local stack works unedited. A deployment that mounts it as-is therefore
serves `/mcp` and `/oauth/*`, and **must** set `MARGINCE_PUBLIC_BASE_URL` — the
api refuses to boot on that gate without it. Remove the `mcp` block to keep the
connector off; the code default is off, so an absent block exposes nothing.

Your `margince.yaml`'s `password_file` **must point to where the entrypoint writes
`MARGINCE_ADMIN_PASSWORD`** — by default `secrets/admin-password` (i.e.
`/app/secrets/admin-password`, the api's working dir is `/app`). Set that value in
your config, or override the write path with `MARGINCE_ADMIN_PASSWORD_FILE` so the
two agree. (The example config's default differs, so change it to match.)

## Routing

Both services sit behind one reverse proxy / ingress, under **one host**:

| path | service |
| --- | --- |
| `/v1`, `/healthz`, `/readyz`, `/metrics` | api |
| `/webhooks/gmail`, `/webhooks/hubspot` | api (present only with that connector configured) |
| `/oauth/`, `/mcp`, `/.well-known/oauth-authorization-server`, `/.well-known/oauth-protected-resource` (and its `/mcp`-suffixed form) | api (present only with the MCP connector declared) |
| everything else, `/` included | web (the SPA, port 8080) |

Route the OAuth metadata documents by those exact paths, not by a
`/.well-known/*` prefix: they are the only things the api serves under
`/.well-known`, and a prefix rule takes `/.well-known/acme-challenge/…` away
from whatever answers your certificate challenges. The webhook row is the api's
because the caller is the provider, not a browser: each handler verifies its own
push, so the SPA cannot stand in for it.

One host, not two, because three things cross the split:

- The SPA calls the API **same-origin** at `location.origin + "/v1"`. There is no
  build-time API base — the same web image works for any domain.
- An MCP client discovers this installation at `/.well-known/oauth-*` and
  connects at `/mcp` on that same origin: RFC 9728 discovery is a chain rooted in
  the resource server's own 401, which a split origin breaks. It must be the host
  `--public-base-url` names.
- The consent flow crosses the two services in both directions. `GET
  /oauth/authorize` (api) redirects the human's browser to `/#/oauth-consent`
  (web); that screen reads `/v1/oauth/consent-request` and posts the decision
  back to `/oauth/authorize` (api). An ingress that serves `/` from somewhere
  else than `/oauth/authorize`, or that routes `/oauth` to the web service, 404s
  the human in the middle of approving a connection — and only there, since the
  client's own handshake never touches the SPA.

## Health checks

- `/healthz` — liveness: a dumb 200 (a DB outage must not restart-loop the api).
- `/readyz` — readiness: 200 when every dependency (Postgres, Redis, and any
  configured object store / vault / AI) is up, else 503 naming the unready one.

Point liveness at `/healthz` and readiness at `/readyz`.

## Order of operations

1. Bootstrap the database once (`db-bootstrap.sql`, as superuser).
2. Deploy the **api** — its entrypoint runs `migrate up` (owner) then serves.
3. Deploy the **worker** and **web**. On a cold database the worker may restart a
   few times until the api has migrated; this is expected.

## Operational notes

- **Outbound mail needs the worker** — the api only stages sends; `cmd/worker`
  transmits them.
- **Admin lockout break-glass:** `margince-migrate reset-password --dsn <owner>
  --email <admin-email>` (reads the new password from stdin). It will also set
  a password on a member who has none, so it *can* onboard — but it needs the
  owner DSN and a shell, so prefer the set-password link below for that.
- **Onboarding without outbound mail:** an invited member is created active with
  no password, so on an installation with no mail channel the invite alone
  leaves an account nobody can sign in as. Settings → Users & roles then offers a
  per-member **"Get set-password link"** — a single-use link the admin delivers
  out of band, redeemed through the normal set-password screen (ADR-0061
  Amendment 1). It needs `--public-base-url` set, since a credential-bearing
  link is never derived from a request `Host`.
- **AI keys fail closed:** a missing/invalid provider key disables the bound AI
  lanes but leaves core CRUD + auth working.
