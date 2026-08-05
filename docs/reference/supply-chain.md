# Supply chain — SBOMs, license gate, signatures

An **SBOM** (software bill of materials) is a machine-readable inventory of
everything the tree ships — every dependency, its version, and its license.
Three tools run this lane, all as digest-pinned Docker images: **syft**
generates the SBOMs, **grant** gates their licenses, and **cosign** signs them.

```text
git archive HEAD ─▶ syft ─▶ 3 SBOM docs ─▶ normalize ─▶ parity ─▶ grant (license gate)
                                                            └─▶ cosign (main only)
```

The SBOM lane covers the whole repo (backend + frontend + extensions), so its
targets live in the **root** `Makefile` rather than being delegated to
`backend/`. It is **not** part of `make check`: CI runs it as its own workflow,
`.github/workflows/sbom.yml`, triggered by dependency-set and pipeline changes.

Everything here describes the **source tree**, not a container image: the scan
input is the committed content of `HEAD`.

## What is produced

`make sbom` exports `git archive HEAD` into `.tmp/sbom-src/`, points syft at
that export, and deletes it again on the way out (a shell `trap`). Scanning an
export rather than the working tree is the reason host state — `node_modules`,
`.env`, editor files, an uncommitted experiment — can never leak into an SBOM,
and it makes the committed content of `HEAD` the single authority on what is
scanned. (`.gitignore` is not that authority: it does not remove a file already
tracked in `HEAD`, and `git add -f` can commit an ignored one.)

One scan, three documents:

| File | Format | syft writer |
|---|---|---|
| `sboms/margince.cdx.json` | CycloneDX JSON | `cyclonedx-json` |
| `sboms/margince.spdx221.json` | SPDX JSON 2.2.1 | `spdx-json@2.2` |
| `sboms/margince.spdx300.json` | SPDX JSON 3.0 | `spdx-json@3.0` |

`/sboms/` is gitignored — these are build artifacts CI publishes, never
committed.

Scan policy lives in [`.syft.yaml`](../../.syft.yaml); the Makefile owns the
"what is scanned" policy (the clean export), the config owns the rest:

- `source.name: margince`. syft has **no** `source.license` key, so the primary
  component's own license cannot be set from config — Margince's source license
  (BUSL-1.1) is handled on the grant side instead.
- `enrich: [all]` with `license.content: none` — license metadata is pulled from
  the Go module proxy and the npm registry, so **`make sbom` needs network
  access**. It is not an offline scan.
- `file.metadata.selection: all`, digests `sha1` + `sha256` + `sha512`. syft's
  default (`owned-by-package`) hashes only files a cataloger attributed to a
  discovered package, which would leave first-party source (`backend/**`,
  `frontend/src/**`, migrations, config templates) with no checksum at all.
  SHA-1 is kept because SPDX file entries historically key on it; SHA-256 and
  SHA-512 are what a consumer actually verifies.
- Excluded committed trees — agent/CI/build-tooling/test state, not shipped
  product: `./.claude/**`, `./.github/**`, `./.githooks/**`, `./.pnpm-store/**`,
  `./scratchpad/**`, `./cli/craft/**`, `./fixtures/**`.

## Versioning

`SBOM_VERSION` is passed to syft as `--source-version`, so it travels **inside**
each document rather than only in a filename — which is what puts it under
cosign's signature.

```make
SBOM_VERSION ?= $(shell git describe --tags --exact-match 2>/dev/null || echo "dev-$$(git rev-parse HEAD 2>/dev/null || echo unknown)")
```

- **HEAD exactly on a tag** ⇒ the tag alone. A tag maps to one commit, so the
  revision is implicit.
- **Otherwise** ⇒ `dev-<full git revision>`, so a published pre-release SBOM is
  traceable to its exact commit.
- `--exact-match` is load-bearing: plain `git describe` would fall back to the
  nearest tag in its `-N-g<sha>` form, and this repo carries non-release tags
  (`archive/*`) that would then read as a release version.
- Overridable on the command line (`make sbom SBOM_VERSION=v1.3.0`); the LICENSE
  stamping that accompanies a real tag is a separate rule, see
  [license-release-rule.md](license-release-rule.md).

## The one-tree invariant

The three documents must describe **one** tree. syft's three writers do not
agree about the file set out of the box, for one scan:

- **CycloneDX** names every file with the absolute scan-root prefix
  (`/src/.tmp/sbom-src/`); **SPDX** names are repo-relative.
- Both **SPDX** writers additionally emit a pseudo-entry per **directory** —
  whose only checksum is the zero SHA-1 — plus one **empty-name** scan-root
  entry. CycloneDX emits neither.

`make sbom-normalize` reconciles them: strip the prefix from CycloneDX file
components, and drop the empty-name / zero-SHA-1 entries from both SPDX
documents, leaving all three enumerating the same repo-relative regular files.
It runs on the syft output **before any signature**, so the signed bytes are the
normalized bytes. Both filters are idempotent, so re-running — or a future syft
that already emits relative names and no directory entries — is a no-op.

The zero SHA-1 is the discriminator because it is the only signal syft gives:
v1.50 labels **every** SPDX element `software_fileKind == "file"`, directories
included, so the kind field cannot single a directory out. A real file always
carries a non-zero SHA-256/512 alongside its SHA-1.

`make sbom-parity` is the assertion. It extracts the three file-name sets —
`.components[]|select(.type=="file")|.name`, `.files[].fileName`, and
`.["@graph"][]|select(.type=="software_File")|.name` — sorts each, and diffs
them pairwise. Green prints `OK: three SBOMs list the same <n> files`; any
difference prints the diff and **fails the build**. So a syft upgrade that
reintroduces the scan-root prefix or the directory entries breaks here, loudly,
rather than silently at release validation. `make sbom` runs normalize then
parity itself, and CI runs `make sbom`, so every CI run is guarded.

## The license gate

`make sbom-check` runs **grant** against the CycloneDX document only:

```bash
grant check sboms/margince.cdx.json -c .grant.yaml
```

[`.grant.yaml`](../../.grant.yaml) sets `require-license: true` and
`require-known-license: true`, so a package with **no** detected license, or one
grant cannot resolve, denies just as an unallowed license does.

The allowlist — sixteen identifiers, exactly as configured:

| Group | Identifiers |
|---|---|
| Baseline permissive | `Apache-2.0`, `MIT`, `BSD-2-Clause`, `BSD-3-Clause`, `ISC`, `0BSD`, `Zlib`, `BSL-1.0`, `CC0-1.0` |
| Present in the tree, added to cover it | `MIT-0`, `BlueOak-1.0.0`, `MPL-2.0` |
| Maintainer decision | `CC-BY-4.0`, `Python-2.0`, `Unlicense` |
| Margince's own source license | `BUSL-1.1` |

Two categories never reach the allowlist check:

- **First-party packages** are ignored by coordinate (`ignore-packages`):
  `github.com/gradionhq/margince/*` (our own Go modules) and
  `example.margince.dev/*` (the committed extension stubs, ADR-0069). They carry
  no third-party license to gate.
- **CI Actions** are excluded from the **scan** entirely — `.syft.yaml` drops
  `./.github/**` — so syft never catalogs them as packages and grant never sees
  them. They are build infrastructure, not shipped product.

Everything else must still resolve to a known, allowed license.

## Signing

`make sbom-sign` keyless-signs each of the three documents with cosign
(`sign-blob --yes --bundle`), producing one bundle per SBOM:

- `sboms/margince.cdx.json.cosign.bundle`
- `sboms/margince.spdx221.json.cosign.bundle`
- `sboms/margince.spdx300.json.cosign.bundle`

It **requires an OIDC token** — `SIGSTORE_ID_TOKEN`, or GitHub's
`ACTIONS_ID_TOKEN_REQUEST_URL` / `ACTIONS_ID_TOKEN_REQUEST_TOKEN`, which are
ambient in a job holding `id-token: write`. All three are forwarded into the
container.

The target depends on **`sbom-parity`, not `sbom`**, and that is deliberate in
both directions. The signature must cover normalized, mutually agreeing bytes,
so something has to re-check them — parity does that cheaply and refuses to sign
a stale or un-normalized set. But re-running *generation* here would run the
syft scan while the signing token is in scope, which is precisely the isolation
the CI workflow exists to enforce (below); in CI the signing job consumes the
generation job's artifact instead.

## Toolchain posture

syft, grant and cosign all run as **digest-pinned Docker images**, so the host
needs none of them installed and a registry tag re-push cannot swap the tool
that reads the repo or the one that holds a signing identity. The tags in the
Makefile are comments next to the digest — **bump tag and digest together**.

| Variable | Default | Notes |
|---|---|---|
| `SYFT_IMAGE` | `anchore/syft@sha256:1288ea4c…` (v1.50.0) | the scanner |
| `GRANT_IMAGE` | `anchore/grant@sha256:17246361…` (v0.6.8) | the license gate |
| `COSIGN_IMAGE` | `gcr.io/projectsigstore/cosign@sha256:c77247c9…` (v2.4.3) | keyless signing |
| `SYFT` / `GRANT` / `COSIGN` | `docker run --rm -v "$(CURDIR)":/src -w /src <image>` | override the **whole** invocation to use host binaries: `make sbom SYFT=syft GRANT=grant` |
| `SBOM_VERSION` | tag, else `dev-<revision>` | see [Versioning](#versioning) |
| `SBOM_DIR` | `sboms` | output directory |
| `SBOM_SRC` | `.tmp/sbom-src` | the clean-export scratch path (created and removed per run) |
| `COSIGN_HOME` | `.tmp/cosign-home` | cosign's `HOME` inside the container |

The host does still need **Docker**, **git**/**tar** (the clean export) and
**jq** (normalization and parity run on the host), plus **network access** for
license enrichment.

**cosign runs as the invoking user, with a relocated `HOME`.** The cosign image
defaults to uid 65532, which owns neither the bind-mounted `sboms/` directory
nor a writable home. So the invocation adds `-u $(id -u):$(id -g)` and
`HOME=/src/.tmp/cosign-home`:

- **uid** — cosign writes each `*.cosign.bundle` mode `0600`. Written as uid
  65532 those bundles would be unreadable to whatever consumes them next; as the
  invoking user they stay readable (in CI, `upload-artifact` runs as the same
  non-root runner).
- **HOME** — sigstore's TUF cache needs somewhere to land; pointing it at the
  gitignored `.tmp/` keeps it out of the tree.

## CI — `.github/workflows/sbom.yml`

Regenerates the SBOM whenever a dependency set or the pipeline itself changes.
The runner needs only Docker, which `ubuntu-latest` pre-installs.

| Trigger | Condition |
|---|---|
| `workflow_dispatch` | manual, any ref (but see the `sign` job's own guard) |
| `pull_request` | types `opened`, `synchronize`, `reopened`, `ready_for_review`, filtered to the paths below |
| `push` | branch `main`, the same paths (anchored once as a YAML alias, reused by both events) |

Path filter: `go.work`, `go.work.sum`, `**/go.mod`, `**/go.sum`,
`pnpm-lock.yaml`, `**/package.json`, `Makefile`, `.syft.yaml`, `.grant.yaml`,
`.github/workflows/sbom.yml`.

Workflow-level `permissions: contents: read` is the floor for every job. The
OIDC minting credential is **not** granted there — only the `sign` job requests
it, so PR-controlled generation code can never mint a signing token.

**Job `sbom`** — skipped while a pull request is still a draft (a draft
iterating on dependency bumps has no SBOM consumer). Checks out with
`persist-credentials: false` and `fetch-depth: 0` (resolving an exact tag needs
tags and history), then runs `make sbom`, then `make sbom-check`. Publishes the
`sboms` artifact from `sboms/` with `if-no-files-found: error` and retention of
**14 days on a pull request, 90 otherwise** — PR artifacts are superseded by the
next push, only main's linger.

**Job `sign`** — `needs: sbom`, and gated to `push` (which the trigger already
restricts to `main`) or a `workflow_dispatch` on `refs/heads/main`. It adds
`id-token: write` to `contents: read`, downloads the `sboms` artifact rather
than regenerating it, runs `make sbom-sign`, and publishes `sboms/*.cosign.bundle`
as the `sbom-signatures` artifact (retention 90 days).

Two reasons it is a separate job and **never runs on a pull request**:

1. **A keyless signature is permanent.** It lands in the public Rekor
   transparency log and cannot be retracted. A PR preview, or a dispatch from a
   feature branch, must never produce one.
2. **Isolation from PR-controlled code.** Generation runs whatever the branch's
   `Makefile` and `.syft.yaml` say; keeping that out of any job holding
   `id-token: write` is what stops a pull request from reaching the signing
   identity. `needs: sbom` also means the license gate has already passed, so a
   policy-failing SBOM never reaches signing.

## Targets

| Target | What it does |
|---|---|
| `sbom` | Generate the three source-tree SBOMs from a clean `git archive HEAD` export into `sboms/`, license-enriched (needs network), then run `sbom-normalize` and `sbom-parity`. Signing is deliberately not part of it |
| `sbom-normalize` | Reconcile syft's three writers onto one file set — strip CycloneDX's absolute scan-root prefix, drop the SPDX directory / empty-name pseudo-entries. Idempotent; runs before any signature so the signed bytes are the normalized bytes |
| `sbom-parity` | Assert all three SBOMs enumerate the identical set of repo-relative files; prints `OK: three SBOMs list the same <n> files` or fails with the diff |
| `sbom-check` | The license gate: `grant check sboms/margince.cdx.json -c .grant.yaml`. Denies an unallowed, unknown, or missing license; first-party coordinates are ignored and CI Actions never enter the scan |
| `sbom-sign` | Keyless cosign `sign-blob` per SBOM → `*.cosign.bundle`. Needs an OIDC token. Depends on `sbom-parity`, not `sbom`, so it re-checks rather than regenerates — and so generation never runs under the signing token |

Every one of these except `sbom` fails fast with
`FAIL: no SBOM found — run 'make sbom' first` when `sboms/margince.cdx.json` is
absent.

See also: [make-targets.md](make-targets.md) for the rest of the root Makefile,
[../deployment.md](../deployment.md) for what ships, and
[license-release-rule.md](license-release-rule.md) for the LICENSE stamping a
tagged release owes.
