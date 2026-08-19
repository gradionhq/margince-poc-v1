# Build the desktop app

Build the self-contained folder a non-technical user runs on macOS or Windows
with no Docker and no prerequisites: Postgres, the event bus, the api, the
worker and the SPA, started by one launcher and used in a browser.

Why it is shaped this way — the custom Postgres, the update contract, why the
two platforms differ where they do, the known limits — is
[explanation/desktop-distribution.md](../explanation/desktop-distribution.md).

## Will it run on my computer?

What a **user** needs. Building it needs more — see the next section.

| | macOS bundle | Windows bundle |
|---|---|---|
| **OS** | macOS 12 Monterey or newer | Windows 10 or newer (Server 2016+ shares that kernel) |
| **Architecture** | Whatever the build machine was — **not** universal. An Apple-silicon build does not run on an Intel Mac at all; an Intel build runs on Apple silicon under Rosetta 2. `make desktop-dist` prints which one it produced | x64 only. Windows on ARM has x64 emulation, but no ARM build is produced and none is tested |
| **Must already be installed** | Nothing | The [Microsoft Visual C++ x64 redistributable](https://aka.ms/vs/17/release/vc_redist.x64.exe). Present on most machines, **not** bundled |
| **Admin rights** | Not needed | Not needed. Running as an administrator also works — `pg_ctl` drops the privileges Postgres refuses to start with |
| **First-launch warning** | Ad-hoc signed, so a copy downloaded through a browser is quarantined and Gatekeeper refuses it — **once**: right-click → **Open**, and the bundle clears the rest itself (see below). Copying by `cp`, USB or AirDrop sets no quarantine and needs none of this | Unsigned, so SmartScreen blocks it: **More info** → **Run anyway** |
| **Where it is put** | Path must be short: the database socket path has a 103-byte system limit, and the launcher measures it and says so | Anywhere. There is no socket, so no limit |
| **Browser** | Chrome/Edge 111+, Firefox 114+, or Safari 16.4+ (Safari 16.4 is available back to macOS 11, so it is reachable on every supported version) | Chrome/Edge 111+ or Firefox 114+ |

The OS floors are not aspirational: `MACOSX_DEPLOYMENT_TARGET` is pinned to
12.0 and **the build fails** if any shipped binary requires newer, so it cannot
silently inherit the build machine's macOS. On Windows the floor is
PostgreSQL 16's own (Windows 10 or newer), which Go and the MSYS2 runtime also
share.

Both need roughly 1 GB free for the folder plus the database, and neither
writes a single byte outside its own folder — no installer, no registry keys,
no `~/Library`, no `%APPDATA%`.

## Or download one already built

Two places, for two different needs.

### A release, for a build meant to be kept

The [releases page](https://github.com/gradionhq/margince-poc-v1/releases)
carries both bundles as assets on every release that was cut with them —
`margince-macos-<version>.tar.gz` and `margince-windows-<version>.zip` — with
the first-launch steps in the release notes. Release assets do not expire, which
is the difference that matters: pick a version, download its build.

Cutting one is a manual dispatch of the **Release** workflow from the Actions
tab. Ordinary merges to main do not build desktop bundles — each one compiles
Postgres from source, and a merge answers no new question about it — so a
downloadable build exists because somebody decided it should.

### A run artifact, for testing a change

Each platform also has a CI lane that publishes the folder as a run artifact, so
testing a branch needs no toolchain at all — and the runner IS the proof the lane
still works, since neither half can be built on the other platform. These expire
after 14 days.

| Workflow | Runner | Artifact |
|---|---|---|
| `desktop-macos` | `macos-latest` (Apple silicon) | `margince-macos-<sha>` — a **tarball**, because artifact upload does not preserve the executable bit |
| `desktop-windows` | `windows-latest` (x64) | `margince-windows-<sha>` — a plain folder |

Both run automatically when `desktop/**` changes on a pull request, by hand from
the Actions tab, and as reusable workflows called by **Release** when it is
cutting a downloadable build — the same lane either way, so a release bundle
cannot differ from the one a pull request was checked against. Download from the
run page, or:

```
gh run download <run-id> -n margince-macos-<sha>
tar -xzf margince-macos.tar.gz          # keeps the +x bit and the signatures
```

Neither build is signed for distribution, so the first launch still needs the
Gatekeeper or SmartScreen step below.

## What building it needs

**Each platform builds on itself.** Neither half cross-builds: the macOS lane
compiles Postgres and Valkey with the Xcode tools, and pgvector on Windows has
no build system other than `nmake` against MSVC.

| | Build host | Also needs |
|---|---|---|
| macOS | Apple silicon or Intel — the bundle inherits the builder's architecture | Xcode Command Line Tools (`xcode-select --install`), Go, node+pnpm |
| Windows | x64 | [Visual Studio Build Tools](https://visualstudio.microsoft.com/downloads/) with the "Desktop development with C++" workload (for pgvector), [MSYS2](https://www.msys2.org/) with `base-devel gcc` (for the event bus), Go, node+pnpm |

## Build it — macOS

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

## Build it — Windows

Run this **on Windows**. PowerShell is the entry point because a Windows build
host is not required to have GNU make:

```powershell
powershell -ExecutionPolicy Bypass -File desktop\build\build-windows.ps1
```

The result is `build\desktop\margince-windows\`. The first run downloads a
310 MB PostgreSQL archive and compiles pgvector and Redis; later runs reuse
both and finish in the time the Go and frontend builds take. Add `-Force` to
rebuild them anyway.

If `make` and `pwsh` are available, the same lane has wrapper targets:

| Target | Script | What it does |
|---|---|---|
| `make desktop-win` | `build-windows.ps1` | The whole folder. Reuses a staged Postgres and bus |
| `make desktop-win-rebuild` | `build-windows.ps1 -Force` | Force everything |
| `make desktop-win-postgres` | `build-postgres.ps1` | Stage PostgreSQL 16, compile pgvector against it (needs MSVC) |
| `make desktop-win-bus` | `build-bus.ps1` | The event bus: Redis 7.2 built under MSYS2 |
| `make desktop-win-app` | `build-app.ps1` | api/worker/migrate + frontend + launcher |
| `make desktop-win-dist` | `build-dist.ps1` | Assemble the folder and verify it runs standalone |
| `make desktop-clean` | — | Remove `build/desktop/` entirely, both platforms |

The pinned versions and checksums live at the top of each script. A mismatch
fails the build rather than silently using a cached download.

## Run it

### macOS

**Copy the folder somewhere short first.** The database uses a unix socket
inside the folder, and the path has a 103-byte system limit — the repo's own
`build/desktop/margince/` is normally past it already — how far depends on
where you cloned — and the launcher refuses to start, naming the limit and the
measured length and telling you to move it.

```
cp -R build/desktop/margince ~/Margince
cd ~/Margince && ./margince
```

Or double-click **Start Margince.command** in Finder, which is what a
non-technical user does.

**A copy that arrived through a browser warns once.** The build is ad-hoc
signed — Developer ID signing and notarization need a paid Apple account — so
Gatekeeper refuses it: right-click → **Open** → **Open**, or approve it in
System Settings → Privacy & Security.

Once, though, not once per program. The browser marks the download and the
unarchiver copies that mark onto everything it extracts, and Gatekeeper asks at
the moment a program is *executed* — so an untreated bundle puts a separate
dialog in front of `initdb`, `postgres`, `valkey-server`, `migrate`, `api` and
`worker` as the stack comes up, each one blocking the boot. The starter clears
the mark from the launcher and the launcher clears it from `runtime/` before it
spawns anything, which is what reduces that queue to the single dialog above.
Neither touches `data/`: those are your records, not ours to relabel.

A copy that never went through a browser — `cp`, a USB stick, AirDrop — is not
marked and shows nothing. To clear an already-downloaded folder yourself:

```
xattr -dr com.apple.quarantine ~/Margince/runtime
```

### Windows

There is no socket and so no path limit; copy the folder anywhere:

```powershell
Copy-Item -Recurse build\desktop\margince-windows $HOME\Margince
& $HOME\Margince\margince.exe
```

Or double-click **Start Margince.cmd**, which opens it in its own window.
**The first launch shows a SmartScreen warning** — the build is unsigned, and
Authenticode signing needs a purchased certificate. "More info" → "Run anyway".

### Either way

It prints the address, opens the browser and runs until Ctrl-C. The first
launch generates the configuration, initialises the database and applies the
whole migration history, so it takes a few seconds longer than later ones.

It prints the sign-in email and, **on the first launch only**, the generated
password. Later launches point at `data/admin-password`, which is the only
copy.

## Configure it

Everything optional is off by default. Turn features on in `margince.env`
next to the launcher — generated on first run with every supported setting
documented and commented out, so it doubles as the reference for what exists:
S3-compatible storage for attachments, Gmail/Outlook capture, outbound
webhooks, log level, the port, and the credentials that drive the AI
surfaces.

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
update. **On Windows, check the timezone**: Windows records its own zone
identifier rather than the IANA name this field takes, so a Windows
installation is created as `UTC` and the value is yours to correct once.

For a real model, also add `ai-routing.yaml` next to them — the launcher
detects it and stops using the offline fake. See
[connect-a-cloud-model-provider.md](connect-a-cloud-model-provider.md).

## Update an installation

Replace **the launcher, the starter script, and `runtime/`**. Leave
`margince.yaml`, `margince.env` and `data/` alone — they are the user's, and
`data/` is the database.

**Quit it first**, so nothing is holding the database while its binaries are
swapped. Then delete `runtime/` before copying the new one: copying over the
top leaves behind any file the new version dropped, and a stale library beside
a new binary is a failure with no obvious cause.

```
# macOS
rm -rf ~/Margince/runtime
cp -R build/desktop/margince/runtime ~/Margince/
cp build/desktop/margince/margince ~/Margince/
cp "build/desktop/margince/Start Margince.command" ~/Margince/
```

```powershell
# Windows
Remove-Item -Recurse -Force $HOME\Margince\runtime
Copy-Item -Recurse build\desktop\margince-windows\runtime $HOME\Margince\
Copy-Item -Force build\desktop\margince-windows\margince.exe $HOME\Margince\
Copy-Item -Force "build\desktop\margince-windows\Start Margince.cmd" $HOME\Margince\
```

Those are the only three things an update replaces. Replacing the whole folder
would destroy the records.

## Start over

```
rm -rf ~/Margince/data ~/Margince/margince.yaml ~/Margince/margince.env
```

```powershell
Remove-Item -Recurse -Force $HOME\Margince\data, $HOME\Margince\margince.yaml, $HOME\Margince\margince.env
```

The next launch bootstraps a fresh installation with a new password. To
remove everything, delete the folder — nothing is stored outside it.

## When something goes wrong

Logs are in `data/logs/`: `api.log`, `worker.log`, `postgres.log`, `bus.log`.
The launcher's own output covers startup and shutdown only; each service
writes its own file.

| Symptom | Cause |
|---|---|
| "the installation folder is too deeply nested" | macOS only: the socket path exceeds 103 bytes. Move the folder closer to your home directory |
| "address already in use" | Another program holds the port, or a previous instance is still running. Quit it, or set `MARGINCE_PORT` |
| "expected KEY=value" | A malformed line in `margince.env`, named with its line number |
| "a database from a previous session is still running" | Windows: a launcher was killed without stopping Postgres and the stray could not be stopped. Sign out and back in |
| Windows SmartScreen blocks the first launch | The build is unsigned. "More info" → "Run anyway" |
| Windows: "VCRUNTIME140.dll was not found" | The [Microsoft Visual C++ redistributable](https://aka.ms/vs/17/release/vc_redist.x64.exe) is not installed. It is not bundled |
| Attachments or logos fail | No object storage. Set `MARGINCE_BLOBSTORE_*` |
| AI answers look canned | No key or routing file, so the offline fake is driving the AI surfaces |
| "no licence is configured and this installation is production" | `MARGINCE_ENV` was set to `production` in `margince.env` without a `MARGINCE_LICENSE` beside it. Supply the token, or remove the override and let the default `dev` posture stand |
| Dates and times look wrong on Windows | The first run defaulted to `UTC`. Set `timezone` in `margince.yaml` |

To stop a stuck instance, find it by the port it listens on rather than by
name — on macOS it is started as `./margince`, so a `pkill -f` on the full path
will not match it. **Substitute your own port** if `margince.env` sets
`MARGINCE_PORT`; 8800 is only the default.

```
kill -INT "$(lsof -nP -iTCP:8800 -sTCP:LISTEN -t)"
```

```powershell
Stop-Process -Id (Get-NetTCPConnection -LocalPort 8800 -State Listen).OwningProcess
```
