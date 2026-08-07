# Build the desktop app

Build the self-contained folder a non-technical user runs on macOS with no
Docker and no prerequisites: Postgres, the event bus, the api, the worker and
the SPA, started by one launcher and used in a browser.

Why it is shaped this way — the custom Postgres, the update contract, the
known limits — is [explanation/desktop-distribution.md](../explanation/desktop-distribution.md).

**Requires macOS on Apple silicon**, the Xcode Command Line Tools
(`xcode-select --install`), Go, and node+pnpm for the frontend.

## Build it

```
make desktop
```

The result is `build/desktop/margince/` (~128 MB). The first run compiles
Postgres and pgvector from source and takes about five minutes; later runs
skip that and finish in seconds, because Postgres changes only when its
pinned version does.

| Target | What it does |
|---|---|
| `make desktop` | The whole folder. Reuses an already-built Postgres and bus |
| `make desktop-rebuild` | Force everything, including Postgres and the bus |
| `make desktop-postgres` | Just the relocatable Postgres 16 + pgvector + contrib (~5 min) |
| `make desktop-valkey` | Just the event bus |
| `make desktop-app` | Just api/worker/migrate + frontend + launcher |
| `make desktop-dist` | Just assemble and verify signatures |
| `make desktop-clean` | Remove `build/desktop/` entirely |

Rerun `make desktop-postgres` after bumping the pinned Postgres or pgvector
version in `desktop/build/build-postgres.sh`; the checksums are pinned there
and a mismatch fails the build rather than silently using a cached tarball.

## Run it

**Copy the folder somewhere short first.** The database uses a unix socket
inside the folder, and the path has a 103-byte system limit — the repo's own
`build/desktop/margince/` is already 127 bytes and will refuse to start,
naming the limit and telling you to move it.

```
cp -R build/desktop/margince ~/Margince
cd ~/Margince && ./margince
```

Or double-click **Start Margince.command** in Finder, which is what a
non-technical user does.

Either way it prints the address, opens the browser and runs until Ctrl-C.
The first launch generates the configuration, runs `initdb` and applies the
whole migration history, so it takes a few seconds longer than later ones.

It prints the sign-in email and, **on the first launch only**, the generated
password. Later launches point at `data/admin-password`, which is the only
copy.

## Configure it

Everything optional is off by default. Turn features on in `margince.env`
next to the launcher — generated on first run with every supported setting
documented and commented out, so it doubles as the reference for what exists:
AI keys, S3-compatible storage for attachments, Gmail/Outlook capture,
outbound webhooks, log level, and the port.

```
# margince.env
ANTHROPIC_API_KEY=sk-ant-...
MARGINCE_PORT=8801
```

Restart to apply. A malformed line refuses the start and names the file and
line rather than being skipped. Field reference:
[reference/configuration.md](../reference/configuration.md).

Company name, currency and timezone live in `margince.yaml`. Both files are
created once and never overwritten, so your edits survive a restart and an
update.

For a real model, also add `ai-routing.yaml` next to them — the launcher
detects it and stops using the offline fake. See
[connect-a-cloud-model-provider.md](connect-a-cloud-model-provider.md).

## Update an installation

Replace **the launcher, `Start Margince.command`, and `runtime/`**. Leave
`margince.yaml`, `margince.env` and `data/` alone — they are the user's, and
`data/` is the database.

```
cp -R build/desktop/margince/runtime ~/Margince/
cp build/desktop/margince/margince ~/Margince/
```

Replacing the whole folder would destroy the records.

## Start over

```
rm -rf ~/Margince/data ~/Margince/margince.yaml ~/Margince/margince.env
```

The next launch bootstraps a fresh installation with a new password. To
remove everything, delete the folder — nothing is stored outside it.

## When something goes wrong

Logs are in `data/logs/`: `api.log`, `worker.log`, `postgres.log`,
`valkey.log`. The launcher's own output covers startup and shutdown only;
each service writes its own file.

| Symptom | Cause |
|---|---|
| "the installation folder is too deeply nested" | The socket path exceeds 103 bytes. Move the folder closer to your home directory |
| "address already in use" | Another program holds the port, or a previous instance is still running. Quit it, or set `MARGINCE_PORT` |
| "expected KEY=value" | A malformed line in `margince.env`, named with its line number |
| Attachments or logos fail | No object storage. Set `MARGINCE_BLOBSTORE_*` |
| AI answers look canned | No key or routing file, so the offline fake is driving the AI surfaces |

To stop a stuck instance, kill it by PID rather than by name — it is started
as `./margince`, so a `pkill -f` on the full path will not match it:

```
kill -INT "$(lsof -nP -iTCP:8800 -sTCP:LISTEN -t)"
```
