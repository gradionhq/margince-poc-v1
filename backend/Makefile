# Golden path: `make dev` boots everything; `make check` is the merge gate.

GO        ?= go
PG_PORT   ?= 55432
REDIS_PORT?= 56379
DB_NAME   ?= margince
OWNER_DSN ?= postgres://margince_owner:dev@localhost:$(PG_PORT)/$(DB_NAME)
APP_DSN   ?= postgres://margince_app:margince_app_dev@localhost:$(PG_PORT)/$(DB_NAME)

export MARGINCE_TEST_DSN     ?= $(OWNER_DSN)
export MARGINCE_TEST_APP_DSN ?= $(APP_DSN)
export MARGINCE_TEST_REDIS   ?= localhost:$(REDIS_PORT)
# Blobstore (MinIO) — the integration lane fails loudly without it, never
# skips, so the endpoint/creds/bucket are part of the compose + CI contract
# (decisions/0022). The bucket is created by the store at first connect.
MINIO_PORT   ?= 59000
export MARGINCE_TEST_BLOBSTORE_ENDPOINT   ?= localhost:$(MINIO_PORT)
export MARGINCE_TEST_BLOBSTORE_ACCESS_KEY ?= minioadmin
export MARGINCE_TEST_BLOBSTORE_SECRET_KEY ?= minioadmin
export MARGINCE_TEST_BLOBSTORE_BUCKET     ?= margince-test
# Dev-only trust switches (the X-Workspace-Slug header, AGENTS.md): make
# is the dev/test entry point, so dev is the right default HERE — a
# production deployment sets its own environment and never runs make.
export MARGINCE_ENV          ?= dev

.DEFAULT_GOAL := help

.PHONY: help check build test test-v test-cover test-integration test-db-up test-it test-integration-serial lint arch-lint vet gen drift db-up db-init db-wait seed-reset migrate migrate-up migrate-down run psql tidy dev infra-down infra-logs infra-reset clean vuln tools tools-go hooks

help: ## List targets with their descriptions (the default goal)
	@grep -hE '^[a-zA-Z][a-zA-Z0-9_-]*:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-18s %s\n", $$1, $$2}'

check: build vet lint arch-lint test drift ## The merge gate: everything a PR must pass

build: ## Compile every package
	$(GO) build ./...

vet: ## Run go vet over every package
	$(GO) vet ./...

test: ## Unit tests (the root fitness tests run uncached)
	$(GO) test ./...
	# The root fitness tests scan files OUTSIDE their package (WalkDir
	# over internal/modules, schema introspection), which Go's test
	# cache cannot key — run them uncached so a new module can never
	# ride a stale green.
	$(GO) test -count=1 .

# -v is load-bearing, not chattiness: `go test` only names skipped tests in
# verbose mode, and the zero-skip teeth below grep for them. A skip in this
# lane means a security gate (RLS, erasure) silently didn't run — that must
# read as red, never as green.
test-integration: ## Integration lane: real Postgres, fails loudly without it (never skips). Parallel — each package on its own throwaway clone db.
	@bash ../scripts/test-integration-parallel.sh

test-db-up: ## (Re)build the migrated margince_test template the parallel lane clones from
	@bash -c 'cd .. && source scripts/lib-testdb.sh && parse_test_dsn && build_template && echo "test-db-up: template $$TEMPLATE_NAME migrated to head"'

test-it: ## Run ONE integration package (optionally RUN=Regex) on a throwaway clone: make test-it DIR=backend/internal/modules/people [RUN=TestName]
	@bash ../scripts/test-integration-one.sh "$(DIR)" "$(RUN)"

test-integration-serial: ## Escape hatch: the old sequential lane, all packages on the shared dev DB (debugging a parallel-isolation issue).
	@log=$$(mktemp); st=$$(mktemp); \
	{ $(GO) test -tags integration -p 1 -v ./... 2>&1; echo $$? >"$$st"; } | tee "$$log"; \
	code=$$(cat "$$st"); rm -f "$$st"; \
	if [ "$$code" -ne 0 ]; then rm -f "$$log"; exit "$$code"; fi; \
	if grep -q -- '--- SKIP' "$$log"; then \
		echo "FAIL: the integration lane must not skip — provision the dependency, never skip:"; \
		grep -- '--- SKIP' "$$log"; rm -f "$$log"; exit 1; \
	fi; \
	rm -f "$$log"; echo "OK: integration passed with 0 skips"

# The integration lane above runs the same harness on the SMB tier as a
# standing canary; this target is the tier the PERF-7 SLO binds at.
bench-perf: ## PERF-3/PERF-7 benchmark harness on the mid-market §6.7 tier (needs db-up; seeds 250k contacts)
	MARGINCE_BENCH_TIER=mid_market $(GO) test -tags integration -run TestPerfBudgetsHoldOnSeededVolumeTier -v -timeout 30m ./internal/compose/integration

# Two passes, two scopes (decisions/0020): .golangci.yml is the repo-wide
# baseline (the depguard DAG lives there and must never be diff-scoped);
# .golangci.strict.yml is the expanded skeleton-parity set, gated to new
# code via new-from-merge-base so the pre-existing backlog is not a burn-down.
lint: ## golangci-lint: repo-wide baseline + new-code strict set (decisions/0020)
	golangci-lint run ./...
	@if git rev-parse --verify -q origin/main >/dev/null; then \
		golangci-lint run --config .golangci.strict.yml ./...; \
	else \
		echo "SKIP strict lint: origin/main not found — fetch it to arm the new-code gate"; \
	fi

# Layer C of the boundary stack — a HARD gate: a forbidden import edge
# fails `make check`, it is never advisory.
GO_ARCH_LINT_VERSION ?= v1.12.0
arch-lint: ## Import-boundary lint (go-arch-lint over .go-arch-lint.yml)
	$(GO) run github.com/fe3dback/go-arch-lint@$(GO_ARCH_LINT_VERSION) check

# The stub and agent-policy generators run here explicitly (not via
# `go generate ./...`): internal/compose is generated INTO, and its own
# generate directives would need the tools resolvable from that package.
gen: ## Regenerate everything derived from the contract
	$(GO) generate ./internal/contracts
	$(GO) run ./tools/gen-stubs
	$(GO) run ./tools/gen-agentpolicy -in api/crm.yaml -out internal/compose/agentpolicy_gen.go

drift: gen ## Fail when any generated file drifted from the contract
	git diff --exit-code -- '*_gen.go' internal/contracts/

# The scan runs in CI (.github/workflows/ci.yml), not in the local
# `make check`, so a missing network never blocks a local build. The
# version is PINNED — `@latest` makes the scan non-reproducible (a tool
# upgrade could change findings between two runs of the same commit).
GOVULNCHECK_VERSION ?= v1.1.4
vuln: ## Scan dependencies for known vulnerabilities (govulncheck)
	$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

# Fresh-machine bootstrap. golangci-lint is the one gate binary `make check`
# expects on PATH; this version must stay identical to the pin in
# .tool-versions and the golangci-lint-action version in ci.yml — bump all
# three together. arch-lint and vuln already run their tools via pinned
# `go run`, so those installs just pre-warm the module cache (and give the
# binaries a PATH home for direct use). Installs are unconditional: a cached
# binary at a different version must be overwritten, or the pin doesn't bind.
GOLANGCI_LINT_VERSION ?= v2.12.2
# The contract-breaking gate (scripts/check-contract-breaking.sh) runs oasdiff
# via pinned `go run`, so this install is a convenience binary for direct use —
# keep the version identical to the pin in that script; bump both together.
OASDIFF_VERSION ?= v1.22.0
tools: ## Install every gate binary at its pinned version (fresh-machine bootstrap)
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	$(GO) install github.com/fe3dback/go-arch-lint@$(GO_ARCH_LINT_VERSION)
	$(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	$(GO) install github.com/oasdiff/oasdiff@$(OASDIFF_VERSION)
	@echo "gate binaries ready in $$($(GO) env GOPATH)/bin — ensure it is on PATH"

# `git rev-parse --git-path hooks` honors core.hooksPath, so this works
# both on a plain clone (.git/hooks) and after the root `make hooks`
# switched the repo to .githooks/ for the pre-push craft gate.
hooks: ## Install the repo pre-commit hook (gofmt + license-header gate)
	install -m 0755 ../scripts/pre-commit "$$(git rev-parse --git-path hooks)/pre-commit"

# The ONE path to dev infrastructure: the compose stack. Pre-compose
# sessions hand-ran `fable-pg16`/`fable-redis` containers on the same host
# ports; db-up removes those legacy names so the stack can bind (their data
# is throwaway — migrate + seed-dev rebuild it).
COMPOSE ?= docker compose -f ../infra/docker-compose.dev.yml

db-up: ## Start the dev stack (infra/docker-compose.dev.yml) and create the app role
	@docker rm -f fable-pg16 fable-redis 2>/dev/null || true
	$(COMPOSE) up -d --wait
	$(MAKE) db-init
db-init: ## (Re)apply scripts/db-init.sql to the running Postgres
	$(COMPOSE) exec -T postgres psql -U margince_owner -d $(DB_NAME) -v ON_ERROR_STOP=1 < ../scripts/db-init.sql

seed-reset: ## Wipe the demo workspace (scripts/seed-reset.sql); make seed-dev rebuilds it
	$(COMPOSE) exec -T postgres psql -U margince_owner -d $(DB_NAME) -v ON_ERROR_STOP=1 < ../scripts/seed-reset.sql

migrate: ## Apply core + custom migrations (owner DSN)
	$(GO) run ./cmd/migrate up --dsn "$(OWNER_DSN)"

dev: db-up migrate ## db-up + migrate + api on :8080 (attachments on the compose MinIO)
	MARGINCE_BLOBSTORE_ENDPOINT=localhost:$(MINIO_PORT) \
	MARGINCE_BLOBSTORE_ACCESS_KEY=minioadmin \
	MARGINCE_BLOBSTORE_SECRET_KEY=minioadmin \
	MARGINCE_BLOBSTORE_BUCKET=margince-dev \
	$(GO) run ./cmd/api --dsn "$(APP_DSN)"

infra-down: ## Stop the dev stack but KEEP the data volumes (clean also drops them)
	$(COMPOSE) down --remove-orphans

clean: ## Tear down the dev stack, containers and data volumes
	$(COMPOSE) down --volumes --remove-orphans
	@docker rm -f fable-pg16 fable-redis 2>/dev/null || true

migrate-up: migrate ## Alias for migrate — apply all pending migrations (owner DSN)

migrate-down: ## Roll back the last migration(s): make migrate-down [STEPS=n]
	$(GO) run ./cmd/migrate down --dsn "$(OWNER_DSN)" --steps $(or $(STEPS),1)

run: ## Run the api on :8080 (no db-up/migrate) — go run ./cmd/api
	$(GO) run ./cmd/api --dsn "$(APP_DSN)"

psql: ## Open a psql shell on the dev database (owner role)
	$(COMPOSE) exec postgres psql -U margince_owner -d $(DB_NAME)

tidy: ## Sync module deps (go mod tidy)
	$(GO) mod tidy

test-v: ## go test -v across the module
	$(GO) test -v ./...

test-cover: ## Go unit tests with coverage summary
	$(GO) test -cover ./...

db-wait: ## Block until Postgres answers pg_isready
	@until $(COMPOSE) exec -T postgres pg_isready -U margince_owner -d $(DB_NAME) >/dev/null 2>&1; do sleep 1; done; echo "postgres ready"

infra-logs: ## Tail the dev stack logs
	$(COMPOSE) logs -f

infra-reset: ## Stop + wipe volumes, then bring the stack back up (clean + db-up)
	$(MAKE) clean && $(MAKE) db-up

tools-go: tools ## Alias for tools — install the Go gate binaries
