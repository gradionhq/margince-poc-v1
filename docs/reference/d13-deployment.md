# Deploying Margince to District 13 (D13)

Margince runs on NFQ's [District 13](https://devops.nfq-asia.com) Kubernetes
platform. You declare services in `.d13.<branch>.yaml`, push to the matching
branch, and Jenkins + Helm build and roll out. This doc covers the **staging**
deployment (`margince.staging.gradion.com`, VPN-only).

The D13 mechanics (config schema, Vault, kubectl) live in the
`gradion-engineering:d13-dev-ops` skill; this doc is the Margince-specific glue.

## What ships

| Service | Image | Role | Ingress |
|---|---|---|---|
| `api` | `Dockerfile.api` | `cmd/api` — HTTP, applies migrations at boot | `/v1` `/healthz` `/readyz` `/metrics` |
| `web` | `Dockerfile.web` | Vite SPA behind nginx | `/` |
| `worker` | `Dockerfile.worker` | `cmd/worker` — outbox relay, retention, Surface-B AI | none (background) |
| `database` | managed | PostgreSQL 16 | — |
| `cache` | managed | Redis 7 | — |

All three private services share the same host (`margince.staging.gradion.com`),
path-routed by the ingress (longest path wins), and all carry the office/VPN IP
whitelist — staging is unreachable from the public internet.

## The two-role database model (why bootstrap is a manual step)

Margince enforces tenant isolation with `FORCE ROW LEVEL SECURITY`, which **only a
superuser bypasses**. So the app must never connect as a superuser:

- **`margince_owner`** — owns the database + every table, runs migrations (DDL)
  and the custom-fields runtime-DDL pool. Not a superuser, no `BYPASSRLS`.
- **`margince_app`** — the runtime role the api + worker connect as. Its table
  grants are applied by migration `0015_app_role_grants`, which is a **no-op
  unless the role already exists** — so it must be created *before* the first
  migration runs.

D13's managed Postgres only hands us the `postgres` superuser, so these roles +
the database + the (untrusted) `vector` extension are created **once, out of
band**. The app pods deliberately hold no superuser credential.

## First-time setup

1. **Open a D13 project** (ticket at https://devops.nfq-asia.com or
   `#asia-district-13`). Project name: `margince`. Namespace: `margince-staging`.
   Request the `margince.staging.gradion.com` CNAME (custom domain → NFQ DevOps).

2. **Fill Vault.** The pipeline materializes `./.env` from Vault before each
   build. Populate every `VAULT_MANAGED` key from `.env.staging`:
   - `MARGINCE_DSN` — `postgres://margince_app:<APP_PW>@database:5432/margince?sslmode=disable`
   - `MARGINCE_OWNER_DSN` — `postgres://margince_owner:<OWNER_PW>@database:5432/margince?sslmode=disable`
   - `MARGINCE_ADMIN_PASSWORD` — first-boot admin password (≥12 chars)
   - `GEMINI_API_KEY` — the BYOK key for the bound AI tiers
   - non-secret keys (`MARGINCE_REDIS`, `MARGINCE_PUBLIC_BASE_URL`, log level/format)
     carry the same values as `.env.staging`.

3. **Bootstrap the database once.** With the two role passwords chosen above
   (they MUST match the DSNs in Vault), run `scripts/d13/db-bootstrap.sql`
   against the managed Postgres as `postgres`:

   ```bash
   # find the postgres pod
   kubectl -n margince-staging get pods -l app=database
   # run the bootstrap (owner_pw / app_pw match the Vault DSNs)
   kubectl -n margince-staging exec -i <postgres-pod> -- \
     psql -U postgres -v owner_pw="'<OWNER_PW>'" -v app_pw="'<APP_PW>'" \
     < scripts/d13/db-bootstrap.sql
   ```

   It is idempotent — safe to re-run.

4. **Create the `staging` branch and push.** D13 maps branch → environment;
   the `staging` branch triggers the staging deploy.

   ```bash
   git switch -c staging origin/main
   git push -u origin staging
   ```

Jenkins builds the three images, applies migrations (api entrypoint, as
`margince_owner`), and the api bootstraps the organization + admin from
`config/margince.staging.yaml` on first boot.

## Redeploying

Push to `staging`. Every push rebuilds and rolls out. The api reruns
`migrate up` at boot (idempotent — no-ops when the schema is at head), so schema
changes ship with the code that needs them.

## Verifying a deploy

```bash
kubectl -n margince-staging get pods
kubectl -n margince-staging logs deploy/api-application --tail=100     # look for "schema is at head" then the api listen line
kubectl -n margince-staging logs deploy/worker-application --tail=100
```

From a VPN-connected host:

```bash
curl -sf https://margince.staging.gradion.com/healthz     # dumb 200 (liveness)
curl -s  https://margince.staging.gradion.com/readyz      # 200 when every dep is up, else 503 naming the unready one
```

Then sign in at `https://margince.staging.gradion.com` as `admin@gradion.com`
with the `MARGINCE_ADMIN_PASSWORD` you set.

## Operational gotchas

- **Outbound mail needs the worker.** The api stages sends; only `cmd/worker`
  transmits them. It is always on here.
- **Admin lockout break-glass:** `migrate reset-password --dsn <owner> --email
  admin@gradion.com` (reads the new password from stdin). Run it via
  `kubectl exec` into the api pod.
- **Cold-start ordering:** on a brand-new database the worker may crash-loop
  briefly until the api has applied migrations — k8s restarts it and it recovers.
  This is expected, not a failure.
- **AI keys fail closed:** a missing/invalid `GEMINI_API_KEY` disables the bound
  AI lanes but leaves core CRUD + auth working.

## Local Docker build check (before pushing)

The Docker builds use the repo root as context. Test them before you push:

```bash
cp .env.staging .env      # stand in for the Vault-materialized .env
docker build -f Dockerfile.api    -t margince-api:test .
docker build -f Dockerfile.worker -t margince-worker:test .
docker build -f Dockerfile.web    -t margince-web:test .
rm .env
```

## Promoting to production later

Add a `.d13.production.yaml` (drop the VPN whitelist for a public host, bump
replicas/resources), a `production` branch, and a production Vault set. The
Dockerfiles and entrypoints are environment-agnostic and need no changes — only
`config/margince.staging.yaml` has a staging-specific sibling to add
(`config/margince.production.yaml`) and wire into `Dockerfile.*` per branch.
