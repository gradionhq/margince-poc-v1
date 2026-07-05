# Run the frontend

The web app lives in `frontend/` (React 19 + Vite; see
[frontend/README.md](../../frontend/README.md) for the full picture). It
talks only to the `/v1` contract surface — there is no privileged path.

## Develop

```sh
make dev            # api on :8080 (db-up + migrate + serve)
make frontend-dev   # Vite dev server; proxies /v1 to https://localhost:8080
```

Log in against the api to get the `crm_session` cookie, then set the
workspace slug once under Settings → Workspace connection (local dev
sends `X-Workspace-Slug`; production resolves the workspace from the
subdomain).

## Verify

```sh
make frontend-check   # Biome + unit tests + tsc + build (the frontend gate)
make frontend-e2e     # the screen-acceptance harness: AC-named tests,
                      # 390px sweep, axe WCAG 2.2 AA, perceived-perf budget
```

The e2e harness runs hermetically over a seed mock by default. To run the
identical suite against a live, seeded backend:

```sh
BASE_URL=https://localhost:8080 make frontend-e2e
```

## Regenerate contract types

After any `backend/api/crm.yaml` change:

```sh
cd frontend && pnpm gen:api
```

`src/api/schema.d.ts` is generated — never hand-edit it.
