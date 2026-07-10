# Thin delegator: the real Makefile lives in backend/ (the Go module root).
# `make check` is the merge gate; `make dev` boots everything.
# The frontend lane is separate (`make frontend-check`) — it needs node+pnpm,
# which not every backend machine has; CI runs both.

.PHONY: help install check check-q check-go check-fe build test test-v test-cover test-integration bench-perf lint arch-lint vet gen gen-types gen-types-check drift db-up db-init db-wait migrate migrate-up migrate-down run psql tidy dev dev-tls clean eval tools tools-go infra-up infra-down infra-logs infra-reset seed-dev seed-reset verify-boot frontend-check frontend-dev frontend-e2e fe-dev fe-install fe-typecheck fe-lint fe-build fe-preview fe-format fe-test ds-purity font-lock icon-lint fitness-jurisdiction storybook fe-uat craft-static craft-residue craft-drift craft-sync check-craft-doc check-image-pins contract-breaking-check test-lanes go-file-length rls-store-path no-jurisdiction uat_env uat_env_stop hooks

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
## run `make check` / `make uat_env` immediately after.
install: fe-install tools hooks
	@echo "install: worktree ready (frontend deps + gate tools + hooks)"

## check — the merge gate: the root deterministic script gates plus the backend
## gate (build, vet, lint, arch-lint, unit + fitness tests, contract drift).
check: craft-drift check-craft-doc check-image-pins contract-breaking-check test-lanes go-file-length rls-store-path no-jurisdiction

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

## infra-up / infra-down — skeleton-compatible aliases for the dev stack (the
## factory tooling + its UAT guides call the infra lane by these names). infra-up
## is `db-up`; infra-down STOPS the containers but keeps the data volumes — use
## `make clean` to also drop them.
infra-up: db-up

infra-down:
	$(MAKE) -C backend infra-down

## dev-tls — the full local stack in a real browser: an HTTPS front door on
## :8080 fronts the api (:8081) and the Vite dev server (:5173), so the SPA
## gets a single Secure-cookie origin. Reads the Anthropic BYOK key from
## .env.local for the live cold-start read-back. Open https://localhost:8080.
dev-tls:
	./dev/dev.sh

check build test test-v test-cover test-integration bench-perf lint arch-lint vet gen drift db-up db-init db-wait seed-reset migrate migrate-up migrate-down run psql tidy dev clean tools tools-go infra-logs infra-reset:
	$(MAKE) -C backend $@

## check-fe — the frontend gate (alias for frontend-check).
check-fe: frontend-check
## fe-dev — Vite dev server (alias for frontend-dev).
fe-dev: frontend-dev
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

## seed-dev — create/refresh the demo workspace (demo-workspace,
## admin@demo.test / demo-password-123) through the public API. Pure
## client: the stack must be running (make dev). Idempotent; re-runs
## log in instead of re-bootstrapping.
seed-dev:
	./scripts/seed-dev.sh

## verify-boot — prove a running, seeded stack end to end: seeded-admin
## login, seeded people visible over /v1, frontend production build.
## Pure client (make dev + make seed-dev first); fails loudly, never skips.
verify-boot:
	./scripts/verify-boot.sh

## eval — run the golden-dataset gates verbosely (they also run, quietly,
## inside `make check`'s unit lane — that is the hard gate).
eval:
	cd backend && go test ./internal/compose -run 'TestColdStartGolden' -v

## frontend-check — the frontend merge lane. The three token-purity gates
## (ported from the foundation skeleton) run first: cheap fail-closed greps
## on top of the vitest conformance suite, so the discipline holds even if
## the test tree regresses. The gen:api + diff pair is the
## TS type-drift gate: src/api/schema.d.ts is generated from crm.yaml, and a
## contract change that skips regeneration would silently strand the frontend
## types, so regenerate and commit them together.
frontend-check:
	frontend/scripts/check-ds-purity.sh
	frontend/scripts/check-font-lock.sh
	frontend/scripts/check-icon-glyph.sh
	cd frontend && pnpm install --frozen-lockfile && pnpm gen:api && \
		{ git diff --exit-code -- src/api/schema.d.ts || \
			{ echo "frontend types drifted from backend/api/crm.yaml — commit the regenerated src/api/schema.d.ts (printed above)"; exit 1; }; } && \
		pnpm check

## fe-install — install the frontend deps (pnpm, frozen lockfile). The FE half
## of `make install`; also what `fe-uat` / `uat_env` assume has run.
fe-install:
	cd frontend && pnpm install --frozen-lockfile

## fe-typecheck — TypeScript typecheck of the frontend (tsc project build, no
## app build). A scope-aware per-task gate for FE-only work.
fe-typecheck:
	cd frontend && pnpm install --frozen-lockfile && pnpm exec tsc -b

frontend-dev:
	cd frontend && pnpm install && pnpm dev

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

## craft-drift — verify cli/craft matches the foundation's hash manifest.
## The gate is foundation-owned (spec architecture/15 §4): it is developed in
## ../margince-foundation/skeleton/cli/craft and vendored here verbatim, so
## every build repo provably runs the same tool the verdicts' gate_version
## names. A local edit fails this target — fix the gate upstream, then
## `make craft-sync`.
craft-drift:
	@shasum -a 256 -c cli/craft/craft-manifest.sha256 --quiet && echo "craft-drift: cli/craft matches the foundation manifest"

## craft-sync — pull the foundation's current gate (source + manifest) over
## the vendored copy. Review the diff like any dependency bump.
craft-sync:
	rsync -a --delete ../margince-foundation/skeleton/cli/craft/ cli/craft/
	@$(MAKE) craft-drift

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

## uat_env — spin a per-worktree live UAT stack (mandatory UAT_SLUG=<slug>):
## the ONE shared infra, but its own database margince_uat_<slug> and api/FE
## ports derived deterministically from the slug, so two worktrees run live UAT
## at once without colliding. Logs + stop handle under .tmp/uat/<slug>/.
uat_env:
	@bash scripts/uat-env.sh up "$(UAT_SLUG)"

## uat_env_stop — stop a UAT env: make uat_env_stop UAT_SLUG=<slug> [DROP=1 also
## drops margince_uat_<slug>].
uat_env_stop:
	@bash scripts/uat-env.sh stop "$(UAT_SLUG)" $(if $(DROP),--drop,)

## hooks — install the repo's git hooks (the pre-push craft-static gate).
## Run once after cloning.
hooks:
	git config core.hooksPath .githooks
	@echo "installed: core.hooksPath=.githooks (pre-push runs craft static on changed backend files)"
