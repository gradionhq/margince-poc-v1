# Thin delegator: the real Makefile lives in backend/ (the Go module root).
# `make check` is the merge gate; `make dev` boots everything.
# The frontend lane is separate (`make frontend-check`) — it needs node+pnpm,
# which not every backend machine has; CI runs both.

.PHONY: check build test test-integration bench-perf lint arch-lint vet gen drift db-up db-init migrate dev dev-tls clean eval tools seed-dev seed-reset verify-boot frontend-check frontend-dev frontend-e2e craft-static craft-residue craft-drift craft-sync check-image-pins contract-breaking-check test-lanes go-file-length hooks

check: craft-drift check-image-pins contract-breaking-check test-lanes go-file-length

## dev-tls — the full local stack in a real browser: an HTTPS front door on
## :8080 fronts the api (:8081) and the Vite dev server (:5173), so the SPA
## gets a single Secure-cookie origin. Reads the Anthropic BYOK key from
## .env.local for the live cold-start read-back. Open https://localhost:8080.
dev-tls:
	./dev/dev.sh

check build test test-integration bench-perf lint arch-lint vet gen drift db-up db-init seed-reset migrate dev clean tools:
	$(MAKE) -C backend $@

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
## types — regenerate and commit them together.
frontend-check:
	frontend/scripts/check-ds-purity.sh
	frontend/scripts/check-font-lock.sh
	frontend/scripts/check-icon-glyph.sh
	cd frontend && pnpm install --frozen-lockfile && pnpm gen:api && \
		{ git diff --exit-code -- src/api/schema.d.ts || \
			{ echo "frontend types drifted from backend/api/crm.yaml — commit the regenerated src/api/schema.d.ts (printed above)"; exit 1; }; } && \
		pnpm check

frontend-dev:
	cd frontend && pnpm install && pnpm dev

## frontend-e2e — the screen-acceptance harness (AC-<screen>-N + axe WCAG AA
## + perceived perf budgets) against the built app over the seed mock.
## Set BASE_URL to point the same suite at a live backend.
frontend-e2e:
	cd frontend && pnpm install --frozen-lockfile && pnpm e2e

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

## hooks — install the repo's git hooks (the pre-push craft-static gate).
## Run once after cloning.
hooks:
	git config core.hooksPath .githooks
	@echo "installed: core.hooksPath=.githooks (pre-push runs craft static on changed backend files)"
