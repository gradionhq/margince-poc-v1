# desktop — the macOS bundle (proof of concept)

A `Margince.app` that runs the whole stack locally for a non-technical
single user on macOS arm64. No Docker, no terminal, no prerequisites.

**Status: proof of concept.** It boots, migrates, serves the SPA and
survives a restart. It is not signed for distribution, has no first-run
wizard, and stores no secrets in the Keychain. See "Not production-ready"
below before shipping anything from here.

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
`build-postgres.sh` exists and why this bundle carries an ongoing
maintenance obligation: every Postgres patch release means rebuilding here.

## Build

Run in order; each writes into `build/out/` (git-ignored).

```
./build/build-postgres.sh   # PG 16 + pgvector + contrib, relocatable   (~5 min)
./build/build-valkey.sh     # the event bus                             (~1 min)
./build/build-app.sh        # api, worker, migrate, frontend, launcher
./build/build-bundle.sh     # assemble + ad-hoc sign Margince.app
```

`build-app.sh` builds the server roles through `build/composition/`, not with
a bare `go build`. That wiring is what links the enabled `extensions/` units;
a bundle built against the vanilla stub silently ships without them and looks
identical from the outside. `extensions=2` in the api log is the evidence it
worked.

## Run

```
open build/out/Margince.app
```

To run against a scratch data directory instead of the real one:

```
MARGINCE_DESKTOP_DATA=/tmp/margince-test \
  build/out/Margince.app/Contents/MacOS/Margince
```

## How it fits together

```
Margince.app/Contents/
├── MacOS/Margince          ui/main.swift — owns NSApplication + WKWebView
└── Resources/
    ├── margince-launcher   launcher/ — supervises everything below
    ├── pgsql/, valkey-server, api, worker, migrate, web/
```

The Swift app owns `NSApplication` so macOS sees one app with one Dock tile
and one Cmd-Q, and supervises the Go launcher as a child. The launcher starts
Postgres and the bus, migrates, starts the api and worker, then serves the SPA
and reverse-proxies the API paths on one origin. The two processes agree via a
single contract line on stdout: `MARGINCE_READY <url>`.

**Data lives outside the bundle**, in `~/Library/Application Support/Margince`.
A user updates by dragging the new app over the old one; anything durable kept
inside the bundle would be destroyed by exactly that gesture.

## Not production-ready

- **Ad-hoc signed only.** A shipped bundle needs a Developer ID and
  notarization. Without them a downloaded copy is quarantined and the first
  double-click reports that the developer cannot be verified.
- **Collation is byte order.** Built `--without-icu` with `initdb --no-locale`,
  so `ORDER BY full_name` sorts diacritics by byte value. Product-visible
  given the shipped Vietnamese localization — decide before release.
- **Signing in through the WKWebView window is unverified.** The session
  cookie carries `Secure`; WebKit treats `localhost` as trustworthy so this
  very likely works, but it has not been exercised.
- **No object storage.** `blobstore` is left unconfigured, so attachment and
  logo paths degrade. MinIO is AGPLv3, which needs a decision before it can
  be redistributed here.
- **Trust auth on a 0700 socket directory**, no passwords. Fine for one user
  on one Mac; revisit alongside the Keychain work.
- **No backup, no PG major-version upgrade path, no first-run wizard.**
