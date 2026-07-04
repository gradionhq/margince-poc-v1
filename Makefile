# Thin delegator: the real Makefile lives in backend/ (the Go module root).
# `make check` is the merge gate; `make dev` boots everything.
# The frontend lane is separate (`make frontend-check`) — it needs node+pnpm,
# which not every backend machine has; CI runs both.

.PHONY: check build test test-integration lint arch-lint vet gen drift db-up db-init migrate dev clean frontend-check frontend-dev

check build test test-integration lint arch-lint vet gen drift db-up db-init migrate dev clean:
	$(MAKE) -C backend $@

frontend-check:
	cd frontend && pnpm install --frozen-lockfile && pnpm check

frontend-dev:
	cd frontend && pnpm install && pnpm dev
