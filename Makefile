# Thin delegator: the real Makefile lives in backend/ (the Go module root).
# `make check` is the merge gate; `make dev` boots everything.
# The frontend lane is separate (`make frontend-check`) — it needs node+pnpm,
# which not every backend machine has; CI runs both.

# Overridable exactly as in backend/Makefile, so a pinned toolchain reaches the
# one target here that invokes the compiler directly instead of delegating.
GO ?= go

.PHONY: help install ai-routing-local dev-fresh check check-backend check-q check-go check-gates check-fe build test test-v test-cover test-integration e2e-ai e2e-ai-report ai-probe test-db-up test-it test-integration-serial bench-perf lint arch-lint vet gen gen-types gen-types-check drift composition check-composition test-extensions db-up db-init db-wait migrate migrate-up migrate-down run psql redis-cli tidy dev dev-stop dev-logs clean tools tools-go infra-up infra-down infra-logs infra-reset seed-dev seed-dev-db seed-reset verify-boot frontend-check frontend-e2e fe-install fe-typecheck fe-lint fe-build fe-preview fe-format fe-test ds-purity font-lock icon-lint fitness-jurisdiction storybook fe-uat craft-static craft-residue check-craft-doc check-image-pins contract-breaking-check test-lanes go-file-length rls-store-path no-jurisdiction pkg-freeze hooks sbom sbom-sign sbom-check

# Bare `make` lists every command instead of running the first target.
.DEFAULT_GOAL := help

## help — list every available command (the default goal): the root targets
## below, then the backend targets `make <name>` delegates into.
help:
	@echo "Margince — make commands"
	@echo ""
	@echo "Root:"
	@grep -hE '^## [A-Za-z0-9_-]+ —' $(MAKEFILE_LIST) | sed -E 's/^## /  /'
	@echo ""
	@echo "Backend (each also runnable as \`make <name>\` from the repo root):"
	@$(MAKE) -s -C backend help

## install — one-shot fresh-worktree setup (the factory's worktree-init runs
## this by name): frontend deps + the Go gate binaries + the repo git hooks.
## Idempotent; extend here as new setup steps are needed. A fresh worktree can
## run `make check` / `make dev` immediately after.
install: fe-install tools hooks ai-routing-local
	@echo "install: worktree ready (frontend deps + gate tools + hooks + local ai-routing)"

## ai-routing-local — seed the gitignored per-engineer config/ai-routing.yaml
## from the committed template on first run; never clobbers an existing copy.
ai-routing-local:
	@test -f config/ai-routing.yaml || { \
		cp config/ai-routing.example.yaml config/ai-routing.yaml; \
		echo "ai-routing-local: seeded config/ai-routing.yaml from config/ai-routing.example.yaml — edit it to bind local models"; \
	}

## check-backend — the backend half of the gate: the root deterministic script
## gates plus the backend gate (build, vet, lint, arch-lint, unit + fitness
## tests, contract drift). No frontend toolchain needed — this is what the CI
## deterministic-gates job runs.
check-backend: check-craft-doc check-image-pins contract-breaking-check test-lanes go-file-length rls-store-path no-jurisdiction pkg-freeze
	$(MAKE) -C backend check

## check — the full merge gate: backend + frontend
## (`check = check-backend check-fe`). check-fe fails if the frontend deps are
## missing, so run `make install` first.
check: check-backend check-fe

## check-q — quiet `make check`: the full log lands in .tmp/check.log and only an
## excerpt prints on failure (keeps a green run's output to one line).
check-q:
	@mkdir -p .tmp
	@if $(MAKE) check > .tmp/check.log 2>&1; then \
		echo "OK: check-q passed"; \
	else \
		echo "FAIL: check-q (last 40 lines of .tmp/check.log):"; \
		tail -n 40 .tmp/check.log; exit 1; \
	fi

## check-go — the Go half of the gate (backend build, vet, lint, arch-lint, unit
## + fitness tests, contract drift). A scope-aware per-task gate for backend-only
## work; the full `make check` adds the deterministic script gates.
check-go:
	$(MAKE) -C backend check

## check-gates — the meta-gate lane: the waiver census, the obligations derived
## from the migrations and the contract, and the walk-scope proofs. A dev-loop
## convenience for iterating on those gates, and NEVER a prerequisite of
## check-backend: every test named below lives in `package backendarch`, which
## `make -C backend check` already runs uncached, so `make check` covers them
## and a prerequisite here would only run them twice.
check-gates:
	@cd backend && $(GO) test -count=1 -run 'TestEveryPackageLevelReasonMapIsAWaiverOrADeclaredFixture|TestEveryWaiversDeclarationIsSweptForStalenessExactlyOnce|TestGatekitServesTestsOnly|TestEveryVersionPinnedTableBumpsItsVersion|TestEveryToolRegistrarIsInvokedByEveryFullRegistry|TestAPublishedFieldNameIsAFieldNameNotProse|TestEveryValidationFieldLiteralNamesAContractField|TestSeamReachableModulesCarryTheirOwnFieldVerdict|TestEveryStoreEntryPointIsAuthGated' .

## infra-up / infra-down — aliases for the dev stack (some deploy tooling and
## UAT guides call the infra lane by these names). infra-up
## is `db-up`; infra-down STOPS the containers but keeps the data volumes — use
## `make clean` to also drop them.
infra-up: db-up

infra-down:
	$(MAKE) -C backend infra-down

## dev — the full local COLD-START stack in a real browser: Postgres + Redis, the api, the
## background worker (cmd/worker — outbox relay + Surface-B runner, always on),
## and the Vite dev server, so the SPA runs against a live api on http://localhost:8080
## (the app on :8080, api behind it on :18080). Bare `make dev` uses the shared
## `margince` database; `make dev
## DEV_SLUG=<slug>` gives an isolated margince_dev_<slug> on slug-derived ports,
## so two worktrees run concurrently without colliding. A bare `make dev` first
## SWEEPS: every margince api/worker/vite on the machine is killed, whatever
## holds :8080 is evicted, and stray margince_dev_* databases are dropped,
## so exactly one stack runs and the app is ALWAYS on :8080. Boots COLD: the
## organization + admin the api bootstraps from config/margince.yaml and no
## other data, so onboarding and empty states are the default view — run
## `make seed-dev` on top when you want the demo records. Reads an optional
## Anthropic BYOK key from .env.local for the live cold-start read-back. Logs +
## stop handle under .tmp/dev/<slug>/.
dev:
	@bash scripts/dev.sh up "$(DEV_SLUG)"

## dev-fresh — `make dev` onto a REBUILT database: drops it, re-migrates,
## and boots the installation a first customer gets (organization + admin,
## no records). Use it when the last session left data behind; plain
## `make dev` keeps whatever is there.
dev-fresh:
	@bash scripts/dev.sh up "$(DEV_SLUG)" --fresh

## dev-stop — stop dev stacks and free their ports. Bare: stops EVERY stack on
## the machine (the mirror of what `make dev` sweeps). With DEV_SLUG=<slug>:
## just that one. DROP=1 also drops the per-slug databases (never `margince`).
dev-stop:
	@bash scripts/dev.sh stop "$(DEV_SLUG)" $(if $(filter 1,$(DROP)),--drop,)

## dev-logs — follow the dev stack's log, coloured per process (api/worker/fe)
## and per severity, with the job-queue heartbeat hidden. ROLE=<api|worker|fe|boot>
## narrows to one process, LEVEL=<debug|info|warn|error> sets a severity floor,
## ALL=1 keeps the heartbeat, FOLLOW=0 N=<n> prints the last n lines and exits.
## A dev view only — the servers' own output stays plain text for a collector.
dev-logs:
	@bash scripts/dev-logs.sh

build test test-v test-cover test-integration e2e-ai e2e-ai-report ai-probe test-db-up test-it test-integration-serial bench-perf lint arch-lint vet gen drift composition check-composition test-extensions db-up db-init db-wait seed-reset seed-dev-db migrate migrate-up migrate-down run psql redis-cli tidy clean tools tools-go infra-logs infra-reset:
	$(MAKE) -C backend $@

## check-fe — the frontend half of the gate (part of `make check`). Fails loudly
## if the frontend deps are missing rather than skipping — a set-up worktree has
## run `make install`, which installs them. The CI frontend job runs this too.
check-fe:
	@[ -d frontend/node_modules ] || { echo "check-fe: frontend/node_modules missing — run 'make install' (or 'make fe-install') first" >&2; exit 1; }
	$(MAKE) frontend-check
## fitness-jurisdiction — no country strings in core (alias for no-jurisdiction).
fitness-jurisdiction: no-jurisdiction
## gen-types — regenerate the contract types (alias for gen).
gen-types: gen
## gen-types-check — fail if generated types drifted (alias for drift).
gen-types-check: drift

## fe-lint — Biome lint the frontend.
fe-lint:
	cd frontend && pnpm install --frozen-lockfile && pnpm lint
## fe-build — production build of the web app.
fe-build:
	cd frontend && pnpm install --frozen-lockfile && pnpm build
## fe-preview — preview the production build.
fe-preview:
	cd frontend && pnpm preview
## fe-format — Biome format --write the frontend source.
fe-format:
	cd frontend && pnpm exec biome format --write src
## fe-test — frontend unit tests (vitest).
fe-test:
	cd frontend && pnpm install --frozen-lockfile && pnpm test

## ds-purity — design-system token purity (no raw hex/rgb outside tokens.css).
ds-purity:
	frontend/scripts/check-ds-purity.sh
## font-lock — font-family lock lint (the sanctioned families only).
font-lock:
	frontend/scripts/check-font-lock.sh
## icon-lint — icon-glyph lock lint (UI chrome is Lucide only).
icon-lint:
	frontend/scripts/check-icon-glyph.sh
## ds-spacing — spacing gate: no NEW raw-px margin/padding/gap in inline styles
## (diff-scoped vs origin/main; use the --space-* scale or a layout class).
ds-spacing:
	frontend/scripts/check-ds-spacing.sh

## seed-dev — create/refresh the demo workspace (demo-workspace,
## admin@demo.test / demo-password-123) through the public API, then seed
## demo FX rates (SQL — fx_rate has no API). Stack must be running
## (make dev). Idempotent; re-runs log in instead of re-bootstrapping.
seed-dev:
	./scripts/seed-dev.sh
	$(MAKE) -C backend seed-dev-db

## verify-boot — prove a running, seeded stack end to end: seeded-admin
## login, seeded people visible over /v1, frontend production build.
## Pure client (make dev, then make seed-dev — dev boots cold); fails loudly,
## never skips.
verify-boot:
	./scripts/verify-boot.sh


## frontend-check — the frontend merge lane. The three token-purity gates
## run first: cheap fail-closed greps
## on top of the vitest conformance suite, so the discipline holds even if
## the test tree regresses. The gen:api + diff pair is the
## TS type-drift gate: src/api/schema.d.ts is generated from crm.yaml, and a
## contract change that skips regeneration would silently strand the frontend
## types, so regenerate and commit them together.
frontend-check:
	frontend/scripts/check-ds-purity.sh
	frontend/scripts/check-font-lock.sh
	frontend/scripts/check-icon-glyph.sh
	frontend/scripts/check-ds-spacing.sh
	cd frontend && pnpm install --frozen-lockfile && pnpm gen:api && \
		{ git diff --exit-code -- src/api/schema.d.ts src/api/public-events.ts || \
			{ echo "frontend types drifted from the backend contracts — commit the regenerated src/api/*.d.ts (printed above)"; exit 1; }; } && \
		pnpm check

## fe-install — install the frontend deps (pnpm, frozen lockfile). The FE half
## of `make install`; also what `fe-uat` / `dev` assume has run.
fe-install:
	cd frontend && pnpm install --frozen-lockfile

## fe-typecheck — TypeScript typecheck of the frontend (tsc project build, no
## app build). A scope-aware per-task gate for FE-only work.
fe-typecheck:
	cd frontend && pnpm install --frozen-lockfile && pnpm exec tsc -b

## frontend-e2e — the screen-acceptance harness (AC-<screen>-N + axe WCAG AA
## + perceived perf budgets) against the built app over the seed mock.
## Set BASE_URL to point the same suite at a live backend.
frontend-e2e:
	cd frontend && pnpm install --frozen-lockfile && pnpm e2e

## storybook — the component workbench on :6006 (the design-system catalog +
## the story surface fe-uat renders). Stories live beside their component as
## <name>.stories.tsx.
storybook:
	cd frontend && pnpm install && pnpm storybook

## fe-uat — change-scoped Storybook render+capture UAT for frontend-only diffs:
## renders THIS branch's changed component's stories in headless Chromium and
## screenshots them — no live stack, no DB. Fails on an unclean render, on a
## changed story the build didn't register, or on a changed component with no
## story. Artifact: .tmp/fe-uat/manifest.json. Deliberately NOT in `make check`
## — it is the fe-only UAT lane a coordinator runs instead of the full stack.
## Optional: ARGS="--allow-missing".
fe-uat:
	cd frontend && pnpm install --frozen-lockfile && \
		pnpm exec playwright install chromium >/dev/null 2>&1 && \
		node scripts/fe-uat.mjs $(ARGS)

## craft-static — the deterministic code-craftsmanship gate (ADR-0045) over the
## whole backend. The pre-push hook (.githooks/pre-push) runs the diff-scoped
## subset automatically; this target is the full manual sweep.
craft-static:
	go run -C cli/craft . static --root ../../backend

## craft-residue — fail if any unresolved CRAFT-FIX/CRAFT-DISPUTE marker was
## left in the backend tree (the review-loop residue check, ADR-0045). The CI
## `craft-residue` job runs this so a marker can never ride to main.
craft-residue:
	go run -C cli/craft . residue --root ../../backend

## check-craft-doc — assert AGENTS.md still carries the `## Craftsmanship`
## section (the craft gate's operating contract, ADR-0045). A cheap doc floor
## so the gate's rules can't be silently unpinned from the repo's rulebook.
check-craft-doc:
	@grep -q '^## Craftsmanship' AGENTS.md || { echo "FAIL: AGENTS.md is missing the '## Craftsmanship' section"; exit 1; }
	@echo "OK: AGENTS.md ## Craftsmanship present"

## check-image-pins — every `uses:` in .github/workflows/ AND every container
## `image:` (workflow service containers + infra/docker-compose.dev.yml) is
## pinned to an immutable ref (supply-chain: a floating vN/main tag or image
## tag lets a compromised artifact ride into CI unreviewed). Lives at the root
## because the workflows do; also a CI step, so a pin can't regress.
check-image-pins:
	@./scripts/check-image-pins.sh

## contract-breaking-check — oasdiff severity gate on backend/api/crm.yaml vs
## origin/main: a breaking change (removed op, narrowed type…) fails; additive
## changes pass. A deliberate spec re-sync runs with CONTRACT_STABILITY=pre-live.
contract-breaking-check:
	@./scripts/check-contract-breaking.sh

## test-lanes — hermetic-unit-lane enforcement: no untagged test may open a
## real Postgres/Redis; real-infra suites carry //go:build integration.
test-lanes:
	@./scripts/check-test-lanes.sh

## go-file-length — hard 500-LOC cap on hand-written Go files, ratcheted via
## scripts/go-file-length-waivers.txt (pre-existing offenders may shrink,
## never grow).
go-file-length:
	@./scripts/check-go-file-length.sh

## rls-store-path — DB-free floor under the RLS runtime proof: no
## internal/modules statement may address the superuser pool directly
## (bypassing FORCE RLS); per-workspace work runs inside WithWorkspaceTx.
## A genuinely cross-workspace query carries a `// rls-exempt: <reason>` line.
rls-store-path:
	@./scripts/check-rls-store-path.sh

## no-jurisdiction — pack-boundary fitness gate: no country-specific
## regulatory identifier (XRechnung/ZUGFeRD/DATEV/…) or ISO-3166 code appears
## in core CODE, only in the jurisdiction seam (internal/modules/de,
## internal/shared/ports/jurisdiction). Comments citing a statute are allowed.
no-jurisdiction:
	@./scripts/check-no-jurisdiction.sh

## pkg-freeze — published-surface freeze gate (ADR-0069 §3, EXT-P3): apidiff
## on every backend/pkg package vs the merge target (origin/$GITHUB_BASE_REF
## in CI; locally the extensions integration branch, else origin/main).
## ADVISORY before the first v1+ release tag (the surface is design-fluid:
## incompatible changes print, never block); ENFORCING from v1.0.0 — then a
## ratified change is its exact finding line in
## scripts/pkg-freeze-allowlist.txt, bound to the merge-base sha it
## ratifies against, and package removals are never allowlistable.
pkg-freeze:
	@./scripts/check-pkg-freeze.sh

## hooks — install the repo's git hooks (the pre-push craft-static gate).
## Run once after cloning.
hooks:
	git config core.hooksPath .githooks
	@echo "installed: core.hooksPath=.githooks (pre-push runs craft static on changed backend files)"

# --- SBOM (software bill of materials, issue #331) ---
# Repo-wide (backend + frontend + extensions), so it lives at the root, not
# delegated to backend/. syft / grant / cosign run through digest-pinned Docker
# images so the toolchain has zero host dependencies and a registry tag re-push
# cannot swap the tools that read the repo and hold a signing identity. Tags are
# comments — bump tag and digest together. Override SYFT/GRANT/COSIGN to use
# host binaries (e.g. `make sbom SYFT=syft GRANT=grant`).
SBOM_DIR     := sboms
SYFT_IMAGE   ?= anchore/syft@sha256:1288ea4c8b38767b4e620c1e312c8cb26b6e887a99b4f07ab6cd19fc6f225026 # v1.50.0
GRANT_IMAGE  ?= anchore/grant@sha256:172463611795f43b77302cdfbd7b3f81295492a7330e0820cfe41c3674920237 # v0.6.8
COSIGN_IMAGE ?= gcr.io/projectsigstore/cosign@sha256:c77247c92f4dfea851c70555738226498393e34e2f9ca83cb959e51c230e4ad7 # v2.4.3
DOCKER_SBOM  := docker run --rm -v "$(CURDIR)":/src -w /src
SYFT         ?= $(DOCKER_SBOM) $(SYFT_IMAGE)
GRANT        ?= $(DOCKER_SBOM) $(GRANT_IMAGE)
# cosign's image defaults to uid 65532, which owns neither the bind-mounted
# sboms/ dir nor a writable HOME — run it as the invoking user so the *.cosign.bundle
# files it writes (mode 0600) are owned by that user and stay readable to whatever
# consumes them next (CI's upload-artifact runs as the same non-root runner). HOME
# points at the gitignored .tmp so cosign's sigstore/TUF cache has somewhere to land.
# The OIDC env vars are ambient in CI (id-token: write).
COSIGN_HOME  := .tmp/cosign-home
COSIGN       ?= $(DOCKER_SBOM) -u $(shell id -u):$(shell id -g) -e HOME=/src/$(COSIGN_HOME) -e SIGSTORE_ID_TOKEN -e ACTIONS_ID_TOKEN_REQUEST_URL -e ACTIONS_ID_TOKEN_REQUEST_TOKEN $(COSIGN_IMAGE)
# Scan a clean export of committed HEAD, so host state (node_modules, .env, IDE
# files) never leaks into the SBOM and .gitignore stays the single authority.
SBOM_SRC     := .tmp/sbom-src
# A release build (HEAD exactly on a tag) reads as the tag alone — the tag maps
# to one commit, so the revision is implicit. An unreleased build pins the full
# git revision as dev-<revision> so a published pre-release SBOM is traceable to
# its exact commit. --exact-match avoids git describe's nearest-tag "-N-g<sha>"
# form leaking a non-release tag (e.g. archive/*) into a release version. The
# revision travels inside each SBOM, so cosign's signature covers it.
SBOM_VERSION ?= $(shell git describe --tags --exact-match 2>/dev/null || echo "dev-$$(git rev-parse HEAD 2>/dev/null || echo unknown)")
SBOM_FILES   := $(SBOM_DIR)/margince.cdx.json $(SBOM_DIR)/margince.spdx221.json $(SBOM_DIR)/margince.spdx300.json

## sbom — generate the source-tree SBOMs (CycloneDX + SPDX 2.2.1 + SPDX 3.0)
## from a clean export of HEAD, license-enriched. Signing is separate: make sbom-sign (CI).
sbom:
	@mkdir -p $(SBOM_DIR)
	@set -e; src=$(SBOM_SRC); \
	  rm -rf "$$src"; mkdir -p "$$src"; \
	  trap 'rm -rf "$$src"' EXIT; \
	  git archive HEAD | tar -x -C "$$src"; \
	  $(SYFT) scan dir:"$$src" -c .syft.yaml --source-version "$(SBOM_VERSION)" \
	    -o cyclonedx-json=$(SBOM_DIR)/margince.cdx.json \
	    -o spdx-json@2.2=$(SBOM_DIR)/margince.spdx221.json \
	    -o spdx-json@3.0=$(SBOM_DIR)/margince.spdx300.json
	@echo "wrote $(SBOM_FILES)"

## sbom-sign — keyless-sign each generated SBOM with cosign (writes *.cosign.bundle; needs an OIDC token).
sbom-sign:
	@mkdir -p $(COSIGN_HOME)
	@for f in $(SBOM_FILES); do \
	  echo "signing $$f"; \
	  $(COSIGN) sign-blob --yes --bundle "$$f.cosign.bundle" "$$f" || exit 1; \
	done
	@echo "signed: $(addsuffix .cosign.bundle,$(SBOM_FILES))"

## sbom-check — license gate: grant fails if any bundled dependency uses a non-allowed license (.grant.yaml).
sbom-check:
	@test -f $(SBOM_DIR)/margince.cdx.json || { echo "FAIL: no SBOM found — run 'make sbom' first"; exit 1; }
	@$(GRANT) check $(SBOM_DIR)/margince.cdx.json -c .grant.yaml
