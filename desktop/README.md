# desktop — the self-contained macOS build

One folder that runs the whole Margince stack locally — Postgres, the event
bus, the api, the worker and the web UI — with no Docker and no
prerequisites. The user starts it and Margince opens in their browser.

**Status: proof of concept.** It boots, migrates, serves the UI and survives a
restart. It is not signed for distribution and several surfaces are off by
default.

```
make desktop        # build build/desktop/margince/  (first run ~5 min)
```

The folder cannot run from `build/desktop/` — that path already exceeds the
103-byte unix socket limit. Copy it somewhere shorter, then start it:

```
cp -R build/desktop/margince ~/Margince
cd ~/Margince && ./margince
```

## The documentation

This directory holds only the sources. Everything else lives in `docs/`, so
there is one copy to keep true:

- **[How to build, run, configure and update it](../docs/how-to/build-the-desktop-app.md)**
  — every `make desktop-*` target, the settings file, the update gesture, and
  the failure table.
- **[Why it is shaped this way](../docs/explanation/desktop-distribution.md)**
  — why it must carry its own Postgres (pgvector is not in `contrib`), how
  relocatability is enforced, the update contract the layout encodes, and the
  known limits.
- **[Every flag and environment variable](../docs/reference/configuration.md)**

## What is here

| Path | What it is |
|---|---|
| `build/build-postgres.sh` | Relocatable Postgres 16 + pgvector + contrib |
| `build/build-valkey.sh` | The event bus |
| `build/build-app.sh` | api, worker, migrate, frontend, launcher |
| `build/build-dist.sh` | Assemble and verify the distributable folder |
| `launcher/` | The Go supervisor — stdlib-only, deliberately outside `go.work`; it starts the shipped binaries as child processes and imports none of them |
