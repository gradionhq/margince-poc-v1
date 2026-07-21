# Run the frontend

The web app lives in `frontend/` (React 19 + Vite; see
[frontend/README.md](../../frontend/README.md) for the full picture). It
talks only to the `/v1` contract surface — there is no privileged path.

## Develop

```sh
make dev   # full local stack, cold: db + migrate + api (:8080) + the Vite SPA (:5173)
```

`make dev` starts the Vite dev server too, with its `/v1` proxy pointed at
the api (plain http — `localhost` is a browser secure-context, so the
`Secure` session cookie survives without TLS). Open the SPA on
http://localhost:5173 and log in to get the `crm_session` cookie — the
server resolves its singleton organization itself (A107/ADR-0061), so no
workspace selection exists. Stop the stack with `make dev-stop`.

## Verify

```sh
make frontend-check   # Biome + unit tests + tsc + build (the frontend gate)
make frontend-e2e     # the screen-acceptance harness: AC-named tests,
                      # 390px sweep, axe WCAG 2.2 AA, perceived-perf budget
```

The e2e harness runs hermetically over a seed mock by default. To run the
identical suite against a live, seeded backend:

```sh
BASE_URL=http://localhost:8080 make frontend-e2e
```

## Regenerate contract types

After any `backend/api/crm.yaml` change:

```sh
cd frontend && pnpm gen:api
```

`src/api/schema.d.ts` is generated — never hand-edit it.
