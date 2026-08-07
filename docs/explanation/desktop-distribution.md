# Desktop distribution — one folder, no Docker

Margince normally runs as a server: containers, a managed Postgres, an
operator who configures it. This is the other shape — **one folder a
non-technical person downloads, starts, and uses in their browser**, with no
Docker, no terminal setup and no prerequisites.

It exists for a single audience: one person, one Mac, their own CRM. That
audience is what justifies it. For anyone able to run `docker compose up`,
[infra/ci-pipeline.md](../../infra/ci-pipeline.md) and
[deployment.md](../deployment.md) already serve them better, and this build
would not pay for its own maintenance.

To build it, see [how-to/build-the-desktop-app.md](../how-to/build-the-desktop-app.md).

## Why it needs its own Postgres

This is the whole cost of the project, and it is not the packaging.

The schema requires four extensions:

| Extension | Required by |
|---|---|
| `vector` | `backend/migrations/core/0022_embeddings.up.sql` |
| `unaccent`, `pg_trgm` | `backend/migrations/core/0052_fts_linguistics.up.sql` |
| `btree_gist` | `backend/migrations/core/0032_meeting_exclusion.up.sql` |

Three are `contrib` modules, which every prebuilt embedded-Postgres
distribution ships. **`vector` is not.** pgvector is a third-party extension
that must be compiled against the exact Postgres build it loads into, so the
usual embedded-Postgres route cannot work and this owns a custom build.

It is also not optional. `CREATE EXTENSION vector` is migration 22 of 202, so
a Postgres without it does not degrade — it fails on the user's first launch,
about a tenth of the way through the migration run.

The consequence is a standing obligation: Postgres ships patch releases
roughly quarterly, pgvector releases on its own cadence, and each one means
rebuilding, re-signing and re-notarizing here.

### Relocatable, and how that is enforced

The folder runs from wherever the user put it, so nothing inside may
reference an absolute path outside itself. Postgres already finds its own
`share/` and `lib/` relative to the running executable, so the work is in the
Mach-O load commands: `build-postgres.sh` rewrites them to `@rpath` and then
**re-signs every patched file**, because `install_name_tool` invalidates a
signature and arm64 macOS refuses to execute a binary whose signature is
invalid.

The build then verifies that no binary links to `/opt/homebrew`,
`/usr/local`, or the staging prefix it was built at — the last being exactly
what relocation removes, and the one an unwary check forgets.

## The folder, and the update contract

```
margince/
├── margince                  ← replaced by an update
├── Start Margince.command    ← replaced by an update
├── runtime/                  ← replaced by an update
│   └── pgsql/  valkey-server  api  worker  migrate  web/
├── margince.yaml             ← the user's: company name, currency, timezone
├── margince.env              ← the user's: every optional feature
├── ai-routing.yaml           ← the user's, optional: binds tasks to models
└── data/                     ← the user's: database, logs, uploads
```

Everything is relative to this folder. Nothing is written to `~/Library` and
nothing escapes to `/tmp`, so it can be moved, copied to another Mac, or
deleted as a unit.

That makes the split load-bearing rather than cosmetic. **An update replaces
the launcher, the starter and `runtime/`, and nothing else.** A non-technical
user updates by copying new files over an existing folder; if durable data
lived under the replaced part, that gesture would destroy months of records.
The layout exists so the natural gesture is safe by construction rather than
by instruction.

### Why the program directory is `runtime/` and not `resources/`

`codesign` reads a directory that contains both a same-named executable and a
subdirectory called `resources` as a legacy bundle. It then tries to sign the
whole folder, walks into it, and fails on the `.command` starter as an
unsignable subcomponent — and `codesign --verify` on the launcher reports
that the code "has no resources but signature indicates they must be
present".

Renaming the directory removes the ambiguity outright. For the same reason
binaries are signed **in the staging directory**, where no path can be
mistaken for a bundle; signatures are embedded in the Mach-O and survive the
copy into the folder, so the assembly step verifies rather than signs.

## How it runs

The launcher is a supervisor, not a second composition root — it starts the
shipped binaries as child processes and imports none of them. It is a
stdlib-only Go module deliberately outside `go.work`, so it neither sees nor
perturbs the backend's dependency graph.

1. Reads `margince.env`; writes `margince.yaml` on first run.
2. `initdb` into `data/pg` if absent, then starts Postgres on a unix socket
   inside `data/sockets` — `listen_addresses=''`, so there is no TCP listener
   at all and no port to collide.
3. Starts the bus on loopback at an ephemeral port.
4. Runs migrations with the owner role.
5. Starts `api` and `worker` on ephemeral ports.
6. Serves the SPA and reverse-proxies the api paths on **one fixed port**.
7. On Ctrl-C, stops everything in reverse and shuts Postgres down cleanly.

Children get their working directory pinned to the installation folder. The
bootstrap `password_file` is written relative so the folder stays portable,
and relative paths resolve against the child's working directory — not
wherever the user happened to start it.

### One fixed port, several ephemeral ones

Only the UI port is fixed (8800 by default, `MARGINCE_PORT` overrides it),
because the browser is the only way in and a bookmark cannot follow a port
that changes every restart. A port already in use is **refused**, not
silently moved, for the same reason. The api and bus ports are ephemeral
because nothing outside the folder addresses them.

The launcher serves the SPA itself and proxies the api paths — the same list
`frontend/vite.config.ts` proxies in dev. One origin means no CORS
configuration the server has no other reason to carry, and it keeps the api's
port an internal detail.

### Shutdown is SIGINT, not SIGTERM

Postgres reads `SIGTERM` as a *smart* shutdown and waits for every client to
disconnect, which never happens while a pooled connection is open — the app
would hang on quit. `SIGINT` is the fast shutdown: roll back in flight, close
cleanly. `SIGQUIT` would be faster but leaves recovery work for the next
launch, and an unclean shutdown on every quit is how a desktop database earns
a reputation for corrupting data.

## Configuration

`margince.env` is the one place features are turned on. It is generated on
first run with every supported setting documented and commented out, so it
doubles as the reference for what is possible. Full field list:
[reference/configuration.md](../reference/configuration.md).

Its contents become the api's and worker's environment — the same 12-factor
surface a server deployment sets, supplied by a file because a desktop
installation has no deployment to set it. Two rules hold:

- **`MARGINCE_ENV` is appended last and cannot be overridden.** An
  installation holding real customer records stays in the production posture,
  which keeps the dev-only destructive switches (today, the admin data-reset
  endpoint) off. That is not a switch to expose beside an API key.
- **A malformed line refuses the start**, naming the file and line. Silently
  skipping a mistyped setting is how a user concludes a feature is broken.

Secrets live in this file at `0600`, not in the macOS Keychain. The database
uses trust auth over a `0700` socket directory — no password is exchanged,
and the filesystem is the access control, which for one user on one Mac is
stronger than a password stored beside the data it protects.

## Known limits

- **A deeply nested folder cannot start.** `sockaddr_un` caps a socket path
  at 103 bytes, and with everything relative, how deeply the folder is
  unpacked decides whether the database can start. There is deliberately no
  `/tmp` fallback: escaping would put runtime state where the user cannot see
  or delete it. The launcher measures the path and says what to do.
- **Collation is byte order.** Built `--without-icu` with `initdb
  --no-locale`, the only locale identical on every Mac. Text with diacritics
  stores and returns correctly, but `ORDER BY full_name` sorts by byte value.
  This is product-visible and undecided.
- **Ad-hoc signing only.** A published build needs a Developer ID and
  notarization; without them a downloaded copy is quarantined and the first
  launch is refused as coming from an unidentified developer.
- **No object storage by default**, so attachment and logo paths degrade
  until `MARGINCE_BLOBSTORE_*` is set. MinIO relicensed to AGPLv3, which is
  awkward to redistribute inside a BUSL-1.1 product, so nothing is bundled.
- **The api starts quietly about everything it cannot do.** Object storage,
  connectors and webhooks being unconfigured produce no startup warning, so a
  user who never opens `margince.env` gets no signal.
- **No backup, no restore, no PG major-version upgrade path**, and no
  first-run wizard.

## Status

Proof of concept. It boots, migrates, serves the UI, survives a restart and
shuts down cleanly. It is not signed for distribution and the limits above
are real.
