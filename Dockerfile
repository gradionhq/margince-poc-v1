# syntax=docker/dockerfile:1

# The three role images — api, web, worker — in ONE file. Every role is a
# build target (docker-bake.hcl selects it via `target`; a plain
# `docker build --target <role> .` works too), and they share the Go builder
# base below, so the stages that are identical across roles are spelled once
# and BuildKit builds them once per bake.
#
# Deployment-target-agnostic: no image bakes instance configuration. Every
# setting comes from the runtime environment (the MARGINCE_* vars in
# docs/reference/configuration.md); the api additionally reads a margince.yaml
# mounted at MARGINCE_CONFIG for first-boot bootstrap. See docs/deployment.md.
#
# The BuildKit cache mounts below pay off on a builder whose daemon outlives
# the build — the D13 Jenkins agent the deploy pipeline uses. On the release
# workflow's ephemeral runner they would start empty, so release.yml saves and
# restores their contents across runs (buildkit-cache-dance + actions/cache)
# and exports the layer cache (cache-to/cache-from type=gha, wired in
# docker-bake.hcl). A plain local build ignores all of that and still works.

# ── Go builder base (composed workspace, ADR-0069) ───────────────────────────
# The build is NOT a plain `go build`: gen-composition must run first to
# materialize build/composition/go.work (the composed workspace that folds in
# the enabled extensions/* packs); the Go binaries build under that workspace
# and the SPA lane consumes its generated registry + merged contracts. This
# mirrors `make -C backend build`. See CLAUDE.md § "make composition".
#
# The base always runs on the build platform and cross-compiles to the target:
# in a multi-platform bake only the thin runtime stages run emulated, never
# the toolchains.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS gobase

RUN apk add --no-cache git ca-certificates

WORKDIR /src

# Dependency manifests first, so the module download is a layer of its own:
# it re-runs when a dependency pin changes, never on a source edit, and the
# release workflow's exported layer cache turns it into a cross-run hit. The
# modules live IN the layer (no cache mount here) for exactly that reason.
# The composed workspace folds in extensions/* on top; anything those need
# beyond this set is fetched in the build steps below.
COPY go.work go.work.sum ./
COPY backend/go.mod backend/go.sum ./backend/
COPY backend/tools/go.mod backend/tools/go.sum ./backend/tools/
COPY cli/craft/go.mod ./cli/craft/
COPY composition/go.mod composition/go.sum ./composition/
RUN GOWORK=/src/go.work go mod download

# The whole repo is the build context — the composed workspace references
# ../backend, ../backend/tools, ../cli/craft and ../extensions/* by relative
# path, so a partial copy would break gen-composition.
COPY . .

# Materialize build/composition/ from the enabled extension set. gen-composition
# runs pinned to the ROOT workspace (it lives in the separate backend/tools
# module and must resolve before the composition exists).
# The compile cache rides a shared mount: every role's build step below reuses
# the same compiled dependency graph, and the release workflow persists the
# mount across runs (see .github/workflows/release.yml).
WORKDIR /src/backend
RUN --mount=type=cache,id=margince-gobuild,target=/root/.cache/go-build \
    GOWORK=/src/go.work go run ./tools/gen-composition

# ── api: build ────────────────────────────────────────────────────────────────
# The api + migrate binaries under the COMPOSED workspace. migrate ships in the
# api image so its entrypoint can apply the embedded migrations as the owner
# role before serving. CGO is off — no role has cgo deps.
FROM gobase AS api-build

# The one immutable revision, passed identically to the api and web targets so
# the api can report when the view documents it fetched came from a different
# build. Diagnostic metadata, never an integrity signature — see
# internal/shared/buildinfo. Absent (a plain `docker build`) leaves it empty,
# which DISABLES the comparison rather than alarming on it.
ARG MARGINCE_BUILD_REVISION=""

# The release this set is built from, stamped into the binary at link time. The
# api is the role that owns the schema, so at boot it RECORDS this as the
# installation's release and every other role refuses to start against a
# different one — the run-time answer to a torn tag pull the registry cannot
# refuse. Absent (a plain `docker build`) it stays "dev", which disables the
# comparison; see internal/shared/buildinfo.
ARG MARGINCE_RELEASE_VERSION=dev
ARG TARGETARCH
# cmd/migrate is deliberately NOT stamped: it ships in this image, so it is the
# same release by construction, and it applies the schema rather than joining
# the set the guard compares.
RUN --mount=type=cache,id=margince-gobuild,target=/root/.cache/go-build \
    GOWORK=/src/build/composition/go.work CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH \
        go build -ldflags="-s -w \
            -X github.com/gradionhq/margince/backend/internal/shared/buildinfo.Revision=$MARGINCE_BUILD_REVISION \
            -X github.com/gradionhq/margince/backend/internal/shared/buildinfo.ReleaseVersion=$MARGINCE_RELEASE_VERSION" \
        -o /bin/margince-api ./cmd/api \
    && GOWORK=/src/build/composition/go.work CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH \
        go build -ldflags="-s -w" -o /bin/margince-migrate ./cmd/migrate

# ── worker: build ─────────────────────────────────────────────────────────────
# cmd/worker — the background process role: the outbox relay (mail does not
# leave without it), the retention evaluator, the clock time-scan, and — with a
# bound model — the Surface-B agent runner + embeddings lane. No HTTP surface.
# The worker does NOT run migrations; the api role owns that, so exactly one
# role applies the schema. On a cold database the worker waits (and k8s
# restarts it) until the api has migrated.
FROM gobase AS worker-build

# The same release version the api target is stamped with. The worker is the
# role that REFUSES: at boot it compares this against the release the api
# recorded and exits rather than run half of one release beside half of another.
ARG MARGINCE_RELEASE_VERSION=dev
ARG TARGETARCH
RUN --mount=type=cache,id=margince-gobuild,target=/root/.cache/go-build \
    GOWORK=/src/build/composition/go.work CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH \
        go build -ldflags="-s -w \
            -X github.com/gradionhq/margince/backend/internal/shared/buildinfo.ReleaseVersion=$MARGINCE_RELEASE_VERSION" \
        -o /bin/margince-worker ./cmd/worker

# ── web: build ────────────────────────────────────────────────────────────────
# The Vite/React SPA — a static build served by nginx. The app talks only to
# the /v1 contract surface, same-origin (frontend/src/api/client.ts derives its
# base from location.origin + "/v1"), so there are NO build-time API-base env
# vars: the D13 ingress routes /v1 (+ /healthz /readyz /metrics) to the api
# service and / to the web one, both under the same host.
#
# The SPA has two lanes, exactly like the Go roles: a VANILLA build resolves
# the committed empty-tree registry (frontend/src/composition/extensions.gen.ts)
# and the committed contract types, and a COMPOSED build resolves the generated
# registry and the MERGED contracts under build/composition/. A shipped image
# is always the composed one — an installation's image must serve the units
# that installation enabled — which is what the gobase stage's gen-composition
# run provides.
FROM --platform=$BUILDPLATFORM node:24-alpine AS web-build

RUN corepack enable

# `pnpm fetch` lays down a virtual store, and the install that follows it
# reorganises that directory once the workspace's members are visible. pnpm
# treats replacing an existing modules directory as destructive and refuses it
# without a TTY to confirm at — which an image build never has. CI=true is
# pnpm's own answer for a non-interactive run, and it is honest here: this is
# exactly a non-interactive build.
ENV CI=true

WORKDIR /app
# `pnpm fetch` populates the store from the LOCKFILE ALONE — no package manifest
# is copied, so this layer is not invalidated by editing one, and more to the
# point it does not have to KNOW the workspace's members.
#
# That second property is what the workspace root move made necessary. Members
# now come and go with the enabled set (extensions/*/frontend), and a
# manifest-based cache layer would have to copy every unit's package.json to
# stay correct: `--frozen-lockfile` fails outright for any member the lockfile
# records and the image has not copied yet, so the first unit to grow a screen
# would have broken this build with an error about a missing importer.
COPY pnpm-lock.yaml ./
RUN --mount=type=cache,id=margince-pnpm-store,target=/pnpm-store \
    --mount=type=cache,id=margince-corepack,target=/root/.cache/node/corepack \
    pnpm fetch --store-dir /pnpm-store

# The composition: the MERGED contracts the TS types are generated from and the
# generated extension registry the SPA routes into. Both are copied from the
# gobase stage rather than regenerated here — this image has no Go toolchain,
# and a second derivation of the same artifacts could disagree with the one the
# api image ships.
WORKDIR /app
COPY --from=gobase /src/build/composition/api/ ./build/composition/api/
COPY --from=gobase /src/build/composition/frontend/ ./build/composition/frontend/
COPY package.json pnpm-workspace.yaml ./
COPY frontend/ ./frontend/
# The unit trees, because a unit's frontend layer is a workspace member and the
# generated registry imports it by package name. Copied AFTER the fetch above so
# a unit edit re-links rather than re-downloading, and copied whole rather than
# per-unit because which units exist is the installation's business, not this
# file's — the same reason the gobase stage takes the entire context.
COPY extensions/ ./extensions/

# --prefer-offline, NOT --offline. The fetch above populates a BuildKit cache
# mount, and that mount is not guaranteed to survive: when the fetch layer is a
# cache HIT but the mount has been pruned, fetch does not re-run and the store
# is empty here — so a hard --offline turns a cold cache into a failed build
# rather than a slower one. Prefer-offline uses the store whenever it is warm
# and reaches the registry when it is not.
#
# --ignore-scripts keeps the supply-chain posture: no dependency's install
# lifecycle runs, and this build needs no postinstall hook.
RUN --mount=type=cache,id=margince-pnpm-store,target=/pnpm-store \
    pnpm install --frozen-lockfile --prefer-offline --ignore-scripts --store-dir /pnpm-store

# Regenerate the contract types from the MERGED crm.yaml, then build the composed
# lane. This keeps the image's types pinned to the contract it was built against
# rather than trusting the committed schema.d.ts.
#
# gen:composed-types writes to build/composition-frontend/ — a SECOND composition
# root, because gen-composition's -verify requires build/composition/ to hold
# exactly the files it generated. tsconfig.composed.json resolves
# "@composition/schema" there, so src/api/client.ts is parameterised by the
# merged contract and an extension route is a typed call rather than a hole.
# The committed src/api/schema.d.ts is never rewritten by this build, so the
# empty-tree byte-identity the composition gates prove is untouched. (The one
# in-container overwrite that remains is public-events.ts, which has no alias
# because nothing composes it yet.)
#
# MARGINCE_COMPOSITION_FRONTEND is the runtime half of the alias (vite.config.ts)
# and tsconfig.composed.json is the compile-time half. Both FAIL LOUDLY when the
# gobase stage produced nothing: vite throws on the missing directory and tsc
# cannot resolve "@composition/extensions". A lane that skipped composition
# cannot silently fall back to the vanilla registry and ship an image that routes
# none of the installation's units.
ENV MARGINCE_COMPOSITION_FRONTEND=/app/build/composition/frontend
WORKDIR /app/frontend
# `tsc -b` dominates this step (~40s of ~51s locally; vite itself is ~6s). Both
# composite projects write their .tsbuildinfo into node_modules/.tmp, which is
# discarded with this stage, so every build type-checks the whole tree from
# scratch. Persisting just that directory makes tsc incremental across builds;
# it keys off recorded file content hashes, so changed files are still re-checked
# and the type gate over the freshly generated schema.d.ts is unchanged.
# The same revision the api target is built with, written into each MCP App view
# document so the api can report skew between the two tiers. See the api-build
# stage for why an absent value disables the comparison rather than alarming.
ARG MARGINCE_BUILD_REVISION=""
ENV MARGINCE_BUILD_REVISION=$MARGINCE_BUILD_REVISION
# The release this set is built from, compiled INTO the bundle (a vite define in
# frontend/vite.config.ts). The SPA is served to a browser rather than run as a
# process, so it cannot be stopped the way the api and the worker stop each
# other: what it does instead is compare its own release against the one the api
# reports and refuse to render the app until they agree. That comparison needs
# the version inside the JavaScript, not in the image's environment, because the
# environment is the build's and the browser only ever sees the bundle.
ARG MARGINCE_RELEASE_VERSION=dev
ENV MARGINCE_RELEASE_VERSION=$MARGINCE_RELEASE_VERSION
RUN --mount=type=cache,id=margince-tsbuildinfo,target=/app/frontend/node_modules/.tmp \
    --mount=type=cache,id=margince-corepack,target=/root/.cache/node/corepack \
    pnpm gen:composed-types && pnpm gen:events:composed && pnpm build:composed

# ── api: runtime ──────────────────────────────────────────────────────────────
# cmd/api — the HTTP process role (serves /v1 + /healthz + /readyz + /metrics).
FROM alpine:3.24 AS api

RUN apk add --no-cache ca-certificates tzdata

COPY --from=api-build /bin/margince-api /usr/local/bin/margince-api
COPY --from=api-build /bin/margince-migrate /usr/local/bin/margince-migrate
COPY scripts/deploy/api-entrypoint.sh /usr/local/bin/entrypoint.sh

# The release version, a second time, as a file in the image.
#
# TWICE ON PURPOSE, and the two copies answer different questions. The OCI label
# (docker-bake.hcl) is readable from the registry without pulling, which is what
# an operator diffing a torn set needs. This file is readable from INSIDE a
# running container — `kubectl exec … cat /etc/margince/release-version` — which
# is what an operator holding a crash-looping worker needs, and it is the only
# place the web image's version can be read at run time at all, since nginx runs
# none of our code. Both derive from the one ARG in this one build, so they
# cannot disagree.
#
# /etc, not the nginx document root: this is metadata about the image, not
# content the web tier serves. The three roles spell the path identically so an
# operator has one thing to remember; every stage below repeats these two lines
# because a Dockerfile ARG does not cross a stage boundary.
ARG MARGINCE_RELEASE_VERSION=dev
RUN mkdir -p /etc/margince && printf '%s\n' "$MARGINCE_RELEASE_VERSION" > /etc/margince/release-version

# Run as a non-root user. /app/config is where a deployment mounts its
# margince.yaml (+ ai-routing.yaml); /app/secrets is where the entrypoint writes
# the bootstrap admin password. Both must be writable by the runtime user.
RUN chmod +x /usr/local/bin/entrypoint.sh \
    && adduser -D -u 10001 app \
    && mkdir -p /app/config /app/secrets \
    && chown -R app:app /app

WORKDIR /app
USER app
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]

# ── worker: runtime ───────────────────────────────────────────────────────────
FROM alpine:3.24 AS worker

RUN apk add --no-cache ca-certificates tzdata

COPY --from=worker-build /bin/margince-worker /usr/local/bin/margince-worker
COPY scripts/deploy/worker-entrypoint.sh /usr/local/bin/entrypoint.sh

# The in-image release version; the api runtime stage carries why.
ARG MARGINCE_RELEASE_VERSION=dev
RUN mkdir -p /etc/margince && printf '%s\n' "$MARGINCE_RELEASE_VERSION" > /etc/margince/release-version

# /app/config is where a deployment mounts its margince.yaml / ai-routing.yaml.
RUN chmod +x /usr/local/bin/entrypoint.sh \
    && adduser -D -u 10001 app \
    && mkdir -p /app/config \
    && chown -R app:app /app

WORKDIR /app
USER app

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]

# ── web: runtime ──────────────────────────────────────────────────────────────
# nginx-unprivileged runs as a non-root user (uid 101) and listens on 8080.
FROM nginxinc/nginx-unprivileged:alpine AS web

COPY frontend/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=web-build /app/frontend/dist /usr/share/nginx/html

# The in-image release version; the api runtime stage carries why. The SPA's own
# copy is compiled into the bundle above — this file is for an operator reading
# the image, and is the ONLY run-time reading of this role's version, because
# nginx cannot compare anything.
#
# The RUN needs root, so it precedes the USER switch below; the base image runs
# builds as root and drops to 101 for the server.
ARG MARGINCE_RELEASE_VERSION=dev
USER root
RUN mkdir -p /etc/margince && printf '%s\n' "$MARGINCE_RELEASE_VERSION" > /etc/margince/release-version

USER 101
EXPOSE 8080

CMD ["nginx", "-g", "daemon off;"]
