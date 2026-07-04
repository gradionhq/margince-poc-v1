# Thin delegator: the real Makefile lives in backend/ (the Go module root).
# `make check` is the merge gate; `make dev` boots everything.
# The frontend lane is separate (`make frontend-check`) — it needs node+pnpm,
# which not every backend machine has; CI runs both.

.PHONY: check build test test-integration lint arch-lint vet gen drift db-up db-init migrate dev clean frontend-check frontend-dev craft-static hooks

check build test test-integration lint arch-lint vet gen drift db-up db-init migrate dev clean:
	$(MAKE) -C backend $@

frontend-check:
	cd frontend && pnpm install --frozen-lockfile && pnpm check

frontend-dev:
	cd frontend && pnpm install && pnpm dev

## craft-static — the deterministic code-craftsmanship gate (ADR-0045) over the
## whole backend. The pre-push hook (.githooks/pre-push) runs the diff-scoped
## subset automatically; this target is the full manual sweep.
craft-static:
	go run -C cli/craft . static --root ../../backend

## hooks — install the repo's git hooks (the pre-push craft-static gate).
## Run once after cloning.
hooks:
	git config core.hooksPath .githooks
	@echo "installed: core.hooksPath=.githooks (pre-push runs craft static on changed backend files)"
