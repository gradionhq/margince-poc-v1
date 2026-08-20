# Desktop distribution — one folder, no Docker

Margince normally runs as a server: containers, a managed Postgres, an
operator who configures it. This is the other shape — **one folder a
non-technical person downloads, starts, and uses in their browser**, with no
Docker, no terminal setup and no services to configure. (On Windows, one
prerequisite is real and named in the how-to: the Microsoft Visual C++ x64
redistributable, which is not redistributed here.)

It exists for a single audience: one person, one computer, their own CRM. That
audience is what justifies it. For anyone able to run `docker compose up`,
[infra/ci-pipeline.md](../../infra/ci-pipeline.md) and
[deployment.md](../deployment.md) already serve them better, and this build
would not pay for its own maintenance.

macOS on Apple silicon and Windows on x64 are both built. They are one product
and one launcher; where they diverge, they diverge because the platform left no
choice, and each divergence is named below.

To build it, see [how-to/build-the-desktop-app.md](../how-to/build-the-desktop-app.md).

## Why it needs its own Postgres

This is the whole cost of the project, and it is not the packaging.

The schema requires four extensions:

| Extension | Required by |
|---|---|
| `vector` | `backend/migrations/core/0022_embeddings.up.sql` |
| `unaccent`, `pg_trgm` | `backend/migrations/core/0052_fts_linguistics.up.sql` |
| `btree_gist` | `backend/migrations/core/0032_meeting_exclusion.up.sql` |

Three are `contrib` modules, which every prebuilt Postgres distribution ships.
**`vector` is not.** pgvector is a third-party extension that must be compiled
against the exact Postgres build it loads into, so no prebuilt distribution can
carry it and every platform here owns a compile step.

It is also not optional. `CREATE EXTENSION vector` is migration 22, so a
Postgres without it does not degrade — it fails on the user's first launch, a
small fraction of the way into a migration history that only grows.

The consequence is a standing obligation: Postgres ships patch releases
roughly quarterly, pgvector releases on its own cadence, and each one means
rebuilding on both platforms.

### Relocatable, and how that is enforced

The folder runs from wherever the user put it, so nothing inside may reference
an absolute path outside itself. How much work that is depends entirely on the
platform's loader, and the two are not close.

**macOS** bakes an absolute install path into every Mach-O load command at link
time. `build-postgres.sh` rewrites them to `@rpath` and then **re-signs every
patched file**, because `install_name_tool` invalidates a signature and arm64
macOS refuses to execute a binary whose signature is invalid. The build then
verifies that no binary links to `/opt/homebrew`, `/usr/local`, or the staging
prefix it was built at — the last being exactly what relocation removes, and
the one an unwary check forgets.

**Windows** resolves a DLL from the directory of the executable that loads it,
so an extracted tree is already relocatable and there is nothing to rewrite.
That is why the Windows lane pins and verifies the upstream community zip
rather than compiling Postgres itself: the property the macOS compile exists to
produce is one the platform gives away. Only pgvector is compiled, with MSVC,
against that staged tree.

### The other kind of relocatability: which OS the binary admits to needing

A binary that resolves all its libraries can still refuse to launch, because a
Mach-O carries the oldest macOS it will run on and the loader enforces it.
`clang` sets that from the machine doing the build, so an unpinned build stamps
whatever the builder was running that week. Nothing is visibly wrong: it works
on the build machine, it works on anything newer, and it fails on everything
older with a floor **nobody chose and nobody wrote down** — one that silently
rises the next time the builder takes an OS update.

Measured on a Mac running 15.7: a C file compiled with no deployment target
reports `minos 15.0`, while a Go binary from the same tree reports `minos 13.0`,
because Go sets its own. So the bundle's real floor was the newest of its
parts, and the two halves of one folder disagreed by two major versions.

`desktop/build/macos-target.sh` is the one place that number lives. It exports
`MACOSX_DEPLOYMENT_TARGET=13.0` — Go's own floor for this toolchain, so the C
halves and the Go halves agree rather than merely coexist — and `assert_min_os`
re-reads every shipped binary and **fails the build** if any of them wants
newer. A pinned variable alone would be a comment: it is one unset environment
away from being ignored, and the machine that ignores it is the one machine
that cannot notice.

Architecture is the limit that stays. The bundle is whatever the builder was —
Apple silicon or Intel, never universal — because a universal Postgres means
building it twice and `lipo`-ing the result, and the audience is one person on
one machine. `build-dist.sh` prints the architecture rather than leaving it to
be discovered.

The two lanes verify the same claim in the way each platform allows: macOS
inspects the link table, Windows **runs each third-party binary out of the
assembled folder**. A missing DLL is invisible on the build machine, where the
file is on `PATH` anyway, and fatal on the user's — so the check has to happen
where the user's copy will be, not where the compiler was.

## The folder, and the update contract

```
margince/
├── margince / margince.exe        ← replaced by an update
├── Start Margince.command / .cmd  ← replaced by an update
├── runtime/                       ← replaced by an update
│   └── pgsql/  the bus  api  worker  migrate  web/
├── margince.yaml                  ← the user's: company name, currency, timezone
├── margince.env                   ← the user's: every optional feature
├── ai-routing.yaml                ← the user's, optional: binds tasks to models
└── data/                          ← the user's: database, logs, uploads
```

Everything is relative to this folder. Nothing is written to `~/Library` or
`%APPDATA%`, and nothing escapes to a temp directory, so it can be moved,
copied to another machine with the same OS and CPU architecture, or deleted as a unit.

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

Windows has no such rule. The name is kept anyway, because one layout means
one document, one update gesture and one `layout.go`.

## How it runs

The launcher is a supervisor, not a second composition root — it starts the
shipped binaries as child processes and imports none of them. It is a
stdlib-only Go module deliberately outside `go.work`, so it neither sees nor
perturbs the backend's dependency graph.

1. Reads `margince.env`; writes `margince.yaml` on first run.
2. Initialises `data/pg` if absent, then starts Postgres — on macOS over a unix
   socket inside `data/sockets` with `listen_addresses=''`, so there is no TCP
   listener at all; on Windows over loopback at an ephemeral port, because
   Windows Postgres has no socket transport.
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
silently moved, for the same reason. The api, the bus and — on Windows — the
database use ephemeral ports because nothing outside the folder addresses them.

The launcher serves the SPA itself and proxies the api paths — the same list
`frontend/vite.config.ts` proxies in dev. One origin means no CORS
configuration the server has no other reason to carry, and it keeps the api's
port an internal detail.

### Shutdown asks for a fast one, in each platform's vocabulary

Postgres reads `SIGTERM` as a *smart* shutdown and waits for every client to
disconnect, which never happens while a pooled connection is open — the app
would hang on quit. `SIGINT` is the fast shutdown: roll back in flight, close
cleanly. `SIGQUIT` would be faster but leaves recovery work for the next
launch, and an unclean shutdown on every quit is how a desktop database earns
a reputation for corrupting data.

Windows has no signals, so the same intent is spelled `pg_ctl stop -m fast`
for the database and a `CTRL_BREAK` console event for everything else. Each
child is started in its own process group, because a control event addressed to
the console reaches every process attached to it — the supervisor would kill
itself on the way to killing its first child. The Go runtime maps that event to
`os.Interrupt`, which `cmd/api` and `cmd/worker` already wait on, so the
shipped binaries shut down through the same path they use on a server.

## Where the two platforms genuinely differ

Four differences are forced. Everything else is shared.

### The database is reached differently, so it is protected differently

macOS uses trust auth over a unix socket in a `0700` directory: no password is
exchanged, and the filesystem is the access control — for one user on one Mac,
stronger than a password stored beside the data it protects.

Windows Postgres has no socket transport at all. The cluster therefore listens
on loopback, and trust auth there would open the database to every other
account on the machine. So the Windows path generates a password per role,
initialises with `scram-sha-256`, and hands `initdb` the secret in a file
rather than on a command line that every process on the machine can read.

Losing the socket also removes the 103-byte path ceiling, so a Windows
installation may live wherever the user put it.

### Postgres cannot be a child process on Windows

`postgres.exe` refuses to start under an account holding administrative
rights — *"Execution of PostgreSQL by a user with administrative permissions is
not permitted"* — and `pg_ctl` is what creates the restricted process that
drops them. Launching `postgres.exe` directly would work for a standard account
and fail for an administrator, which is the kind of split that only shows up on
someone else's machine.

The cost is that the postmaster is not the launcher's child, so a launcher
killed outright can leave one holding the data directory. The property is
bought back at the other end: the next start asks `pg_ctl status` and stops a
stray before it begins, rather than failing forever with a lock-file message
the user has no way to interpret.

### The event bus is Valkey on macOS and Redis on Windows

macOS ships **Valkey**: this binary is redistributed inside a BUSL-1.1 product
and Redis 7.4 onward is RSALv2/SSPL, while Valkey is the BSD-licensed fork of
the same lineage.

Valkey has no Windows build, and upstream declines to add one, pointing Windows
users at WSL — which a bundle whose whole promise is "no prerequisites" cannot
ask for. So Windows ships **Redis 7.2**, the last BSD-3 line before the
relicense and the exact lineage Valkey forked from: redistributable on the same
terms, and speaking the protocol `platform/events` already uses.

Two things this rules out. The long-standing native Windows Redis ports are
stuck on 5.0, and the outbox subscriber uses `XAUTOCLAIM`, which arrived in
6.2 — those builds would fail on the first stalled message rather than at build
time. And Microsoft's Garnet, which is MIT and native, implements no stream
commands at all, so the relay has nothing to write to.

That leaves one real option, and it has a licensing consequence worth stating
plainly: there is no MSVC-native Redis, because Redis wants `fork()`, unix
sockets and an event loop Windows does not have. Every working Windows build
gets them from a POSIX emulation layer, which travels as `msys-2.0.dll` beside
`redis-server.exe`. That DLL is **LGPLv3**. It is shipped unmodified with its
licence text, which is what the licence asks for, and the build script does the
copying so the obligation is met rather than described.

### Signing

macOS **must** sign: it refuses to execute a binary whose signature is invalid,
which is why the relocation step re-signs every file it patches. Windows has no
such requirement, and Authenticode needs a purchased certificate rather than an
ad-hoc one. So the Windows bundle is unsigned and the first launch shows a
SmartScreen warning — documented in the how-to, not worked around.

## Configuration

`margince.env` is the one place features are turned on. It is generated on
first run with every supported setting documented and commented out, so it
doubles as the reference for what is possible. Full field list:
[reference/configuration.md](../reference/configuration.md).

Its contents become the api's and worker's environment — the same 12-factor
surface a server deployment sets, supplied by a file because a desktop
installation has no deployment to set it. Two rules hold:

- **`MARGINCE_ENV` defaults to `dev`, and margince.env may override it.** This
  is the one place the desktop shape and the server shape genuinely disagree,
  and the reason is licensing: a serving role boots on a licence or it does not
  boot, and `MARGINCE_ENV` is fail-closed, so an installation that names nothing
  is production and is held to a licence it was never issued. Pinned to
  production, the bundle could not start at all.

  What the `dev` posture costs is narrower than it sounds, and worth stating
  precisely because the obvious fear is the wrong one: it does **not** arm the
  admin data-reset endpoint. That is gated by `operations.allow_data_reset` in
  `margince.yaml`, which the first run never writes, so the route stays a 404
  whatever the posture says. What the posture does change is that `/me` reports
  `non_production`, and that a licence minted by a test authority would be
  honoured — this installation has neither.

  It is a default and not a decision. An operator issued a licence puts
  `MARGINCE_LICENSE` and `MARGINCE_ENV=production` in `margince.env`, and the
  installation is held to both. The DSNs, by contrast, are still appended after
  the user's settings and cannot be displaced.
- **A malformed line refuses the start**, naming the file and line. Silently
  skipping a mistyped setting is how a user concludes a feature is broken.

Secrets live in this file at `0600`, not in the Keychain or the Windows
Credential Manager.

**On Windows that mode is close to meaningless**, and it is worth being exact
rather than reassuring: Go maps the permission argument to the read-only
attribute and sets no DACL, so `margince.env`, `data/admin-password` and the
generated database passwords all inherit the permissions of the directory they
land in. Under a user profile that is already per-account and the files are
private. Somewhere like `C:\Margince` it is not, and every local account on
the machine can read them — including the database password that
postgres_windows.go treats as the access control. **Keep a Windows
installation inside your own user folder.** Setting an explicit DACL would fix
this properly and is not something the stdlib does; see the known limits.

## Known limits

- **A deeply nested folder cannot start, on macOS.** `sockaddr_un` caps a
  socket path at 103 bytes, and with everything relative, how deeply the folder
  is unpacked decides whether the database can start. There is deliberately no
  `/tmp` fallback: escaping would put runtime state where the user cannot see
  or delete it. The launcher measures the path and says what to do.
- **Collation is byte order.** Built `--without-icu` on macOS and initialised
  `--no-locale` on both, the only setting identical everywhere. Text with
  diacritics stores and returns correctly, but `ORDER BY full_name` sorts by
  byte value. This is product-visible and undecided.
- **Neither build is signed for distribution.** macOS is ad-hoc signed and
  needs a Developer ID plus notarization, without which a downloaded copy is
  quarantined; Windows is unsigned and warns through SmartScreen.

  The quarantine mark costs one dialog, not one per binary, and only because
  the bundle removes it deliberately. A browser stamps the archive, the
  unarchiver copies the stamp onto everything it extracts, and Gatekeeper
  assesses at *exec* time — so the untreated bundle interrupts its own boot six
  times over, once per program the launcher spawns, with no dialog explaining
  why answering the previous one did not count. `Start Margince.command` clears
  the mark from the launcher, whose own dialog the user has just answered, and
  the launcher clears `runtime/` before spawning anything. `data/` is never
  touched: the user's records keep their provenance, and a live socket there
  would fail the call anyway.

  This is a workaround for the missing signature, and it is one the signature
  would delete rather than improve — a notarized build reaches none of this
  code, because Gatekeeper never asks.
- **The event bus accepts unauthenticated local connections.** It listens on
  loopback at an ephemeral port with no `requirepass`, so any account on the
  machine can read and write the event stream — which carries job payloads, and
  therefore CRM data. On a single-user desktop that is the same trust boundary as
  the user's own files; on a shared machine it is a second account reading the
  first one's records.

  It is named here rather than fixed because closing it is not a desktop change.
  `--redis` takes a bare `host:port` and the api builds its client with
  `redis.Options{Addr: …}` and no `Password`, so a per-installation credential
  means adding one to the server's own configuration surface — which every
  deployment then inherits. Worth doing, tracked separately; not something to
  reach into the server's config for while packaging a folder.

  Note the asymmetry it creates on macOS: the database is reached through a
  socket in a `0700` directory, so the bus is now the weaker of the two local
  paths.
- **Windows file permissions are the folder's, not the file's.** The `0600`
  the launcher asks for sets no DACL there, so the secrets are only as private
  as the directory the user chose. An installation under the user's own
  profile is private; one in a shared location is readable by every local
  account. Fixing it means setting an explicit DACL, which the stdlib does not
  expose and this stdlib-only launcher therefore does not do.
- **One architecture per build, and it is the builder's.** An Apple-silicon
  bundle will not start on an Intel Mac; an Intel one runs on Apple silicon
  only under Rosetta 2. Windows is x64 with no ARM build. Shipping to a mixed
  fleet means building once per architecture.
- **The Windows build assumes the Microsoft Visual C++ runtime.** It is
  installed machine-wide by almost anything built with MSVC, and the C++
  workload the build itself requires puts it on the build host — which is
  exactly why the "does it run from the assembled folder" check cannot see its
  absence. A machine without it fails at the first `postgres.exe` with a
  missing-DLL dialog.
- **Windows gets no timezone.** Windows records its own zone identifier and the
  mapping to the IANA name `margince.yaml` takes lives in CLDR data no stdlib
  call exposes. The first run is created as `UTC` and the user corrects it in
  one line, rather than the bundle carrying a copy of that table to guess with.
- **No object storage by default**, so attachment and logo paths degrade
  until `MARGINCE_BLOBSTORE_*` is set. MinIO relicensed to AGPLv3, which is
  awkward to redistribute inside a BUSL-1.1 product, so nothing is bundled.
- **The api starts quietly about everything it cannot do.** Object storage,
  connectors and webhooks being unconfigured produce no startup warning, so a
  user who never opens `margince.env` gets no signal.
- **A launcher killed outright leaves orphans**, on both platforms: the fixed
  UI port is then held and the next start is refused, naming the port. Only the
  Windows database recovers itself.
- **No backup, no restore, no PG major-version upgrade path**, and no
  first-run wizard.

## Status

Proof of concept. The macOS build boots, migrates, serves the UI, survives a
restart and shuts down cleanly. The Windows lane is authored against the
platform's documented behaviour and has not yet been run end to end on a
Windows host; the limits above are real on both.
