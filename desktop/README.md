# desktop — the self-contained macOS build (proof of concept)

One folder that runs the whole Margince stack locally: Postgres, the event
bus, the api, the worker and the web UI. No Docker, no terminal setup, no
prerequisites. The user double-clicks a starter and Margince opens in their
browser.

**Status: proof of concept.** It boots, migrates, serves the UI and survives
a restart. It is not signed for distribution and several product surfaces are
off by default. See "Not production-ready" below.

## Why a custom Postgres build

The schema requires four extensions:

| Extension | Required by |
|---|---|
| `vector` | `backend/migrations/core/0022_embeddings.up.sql` |
| `unaccent`, `pg_trgm` | `backend/migrations/core/0052_fts_linguistics.up.sql` |
| `btree_gist` | `backend/migrations/core/0032_meeting_exclusion.up.sql` |

Three come from `contrib`, which every prebuilt embedded-Postgres
distribution ships. `vector` does not — it is a third-party extension that
must be compiled against the exact Postgres build it loads into. That is why
`build-postgres.sh` exists, and why this carries an ongoing obligation: every
Postgres patch release means rebuilding here.

## Build

Run in order. Everything lands in `build/desktop/` at the repo root
(git-ignored).

```
./build/build-postgres.sh   # PG 16 + pgvector + contrib, relocatable   (~5 min)
./build/build-valkey.sh     # the event bus                             (~1 min)
./build/build-app.sh        # api, worker, migrate, frontend, launcher
./build/build-dist.sh       # assemble + ad-hoc sign the folder
```

`build-app.sh` builds the server roles through `build/composition/`, not with
a bare `go build`. That wiring is what links the enabled `extensions/` units;
a build against the vanilla stub silently ships without them and looks
identical from the outside. `extensions=2` in the api log is the evidence it
worked.

## Run

```
build/desktop/margince/margince
```

or double-click **Start Margince.command** in Finder. Either way it prints the
address, opens your browser, and runs until Ctrl-C.

## Layout — and the update contract

```
margince/
├── margince                  ← replaced by an update
├── Start Margince.command    ← replaced by an update
├── resources/                ← replaced by an update
│   └── pgsql/  valkey-server  api  worker  migrate  web/
├── margince.yaml             ← yours: company name, currency, timezone
├── margince.env              ← yours: every optional feature
├── ai-routing.yaml           ← yours, optional: binds tasks to models
└── data/                     ← yours: database, logs, uploads
```

**Everything is relative to this folder**, so it can be moved, copied to
another Mac, or deleted as a unit. Nothing is written to `~/Library`, and
nothing escapes to `/tmp`.

That makes the update gesture load-bearing: **an update replaces the launcher,
the starter and `resources/`, and nothing else.** Replacing the whole folder
would destroy the user's records.

## How it works

The launcher is a supervisor, not a second composition root — it starts the
shipped binaries as child processes and imports none of them.

1. Reads `margince.env`, writes `margince.yaml` on first run.
2. `initdb` into `data/pg` if absent; starts Postgres on a unix socket inside
   `data/sockets` with no TCP listener at all.
3. Starts the bus on loopback at an ephemeral port.
4. Runs migrations with the owner role.
5. Starts the api and worker on ephemeral ports, with `MARGINCE_ENV=production`.
6. Serves the SPA and reverse-proxies the api paths on **one fixed port**
   (8800 by default) so a browser bookmark keeps working.
7. On Ctrl-C, stops everything in reverse and shuts Postgres down cleanly.

Only the UI port is fixed and user-visible. The internal ports are ephemeral
because nothing outside the folder addresses them.

## Configuration

`margince.env` is the single place features are turned on. It is generated on
first run with every supported setting documented and commented out, so it
doubles as the reference for what is possible — AI keys, S3-compatible
storage for attachments, Gmail/Outlook capture, outbound webhooks, log level,
and the listening port.

Settings there are passed to the api and worker as their environment.
`MARGINCE_ENV` is appended last and cannot be overridden: a desktop
installation holds real customer records and stays in the production posture,
which keeps the dev-only destructive switches off.

## Not production-ready

- **Ad-hoc signed only.** A published build needs a Developer ID and
  notarization; without them a downloaded copy is quarantined and the first
  launch is refused.
- **Collation is byte order.** Built `--without-icu` with `initdb --no-locale`,
  so `ORDER BY full_name` sorts diacritics by byte value. Product-visible
  given the shipped Vietnamese localization — decide before release.
- **Trust auth on a `0700` socket directory**, no database passwords. Sound
  for one user on one Mac; revisit for any other shape.
- **A deeply nested folder cannot start.** The database socket path has a
  103-byte system limit; the launcher checks it and says so, but the fix is
  the user moving the folder.
- **No backup, no restore, no PG major-version upgrade path.**
- **No first-run wizard and no Keychain**, so secrets sit in `margince.env`
  at `0600` rather than in the system keystore.
