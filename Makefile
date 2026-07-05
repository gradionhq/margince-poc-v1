# Thin delegator: the real Makefile lives in backend/ (the Go module root).
# `make check` is the merge gate; `make dev` boots everything.
# The frontend lane is separate (`make frontend-check`) — it needs node+pnpm,
# which not every backend machine has; CI runs both.

.PHONY: check build test test-integration lint arch-lint vet gen drift db-up db-init migrate dev clean frontend-check frontend-dev frontend-e2e craft-static craft-drift craft-sync hooks

check: craft-drift

check build test test-integration lint arch-lint vet gen drift db-up db-init migrate dev clean:
	$(MAKE) -C backend $@

frontend-check:
	cd frontend && pnpm install --frozen-lockfile && pnpm check

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

## craft-drift — verify cli/craft matches the foundation's hash manifest.
## The gate is foundation-owned (spec architecture/15 §4): it is developed in
## ../margince/skeleton/cli/craft and vendored here verbatim, so every build
## repo provably runs the same tool the verdicts' gate_version names. A local
## edit fails this target — fix the gate upstream, then `make craft-sync`.
craft-drift:
	@shasum -a 256 -c cli/craft/craft-manifest.sha256 --quiet && echo "craft-drift: cli/craft matches the foundation manifest"

## craft-sync — pull the foundation's current gate (source + manifest) over
## the vendored copy. Review the diff like any dependency bump.
craft-sync:
	rsync -a --delete ../margince/skeleton/cli/craft/ cli/craft/
	@$(MAKE) craft-drift

## hooks — install the repo's git hooks (the pre-push craft-static gate).
## Run once after cloning.
hooks:
	git config core.hooksPath .githooks
	@echo "installed: core.hooksPath=.githooks (pre-push runs craft static on changed backend files)"
