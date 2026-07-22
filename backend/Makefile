# Golden path: `make dev` boots everything; `make check` is the merge gate.

GO        ?= go
PG_PORT   ?= 55432
REDIS_PORT?= 56379
DB_NAME   ?= margince
OWNER_DSN ?= postgres://margince_owner:dev@localhost:$(PG_PORT)/$(DB_NAME)
APP_DSN   ?= postgres://margince_app:margince_app_dev@localhost:$(PG_PORT)/$(DB_NAME)

# The integration lane runs on its OWN `_test`-suffixed namespace on the shared
# containers, so `make dev` (DB `margince`, Redis logical db 0) and
# `make test-integration` never touch the same mutable state and can run
# concurrently. Postgres: a separate `margince_test` database (the parallel lane
# clones per-package throwaways from a template of the same name; the serial
# escape hatch runs directly on it). Redis: db 0 is reserved for `make dev`, so
# the test lanes default to db 15 and the parallel runner fans packages across
# 1..15 (see scripts/test-integration-parallel.sh) — no stream collisions.
TEST_DB_NAME ?= margince_test
export MARGINCE_TEST_DSN     ?= postgres://margince_owner:dev@localhost:$(PG_PORT)/$(TEST_DB_NAME)
export MARGINCE_TEST_APP_DSN ?= postgres://margince_app:margince_app_dev@localhost:$(PG_PORT)/$(TEST_DB_NAME)
export MARGINCE_TEST_REDIS    ?= localhost:$(REDIS_PORT)
export MARGINCE_TEST_REDIS_DB ?= 15
# Blobstore (MinIO) — the integration lane fails loudly without it, never
# skips, so the endpoint/creds/bucket are part of the compose + CI contract.
# The bucket is created by the store at first connect.
MINIO_PORT   ?= 59000
export MARGINCE_TEST_BLOBSTORE_ENDPOINT   ?= localhost:$(MINIO_PORT)
export MARGINCE_TEST_BLOBSTORE_ACCESS_KEY ?= minioadmin
export MARGINCE_TEST_BLOBSTORE_SECRET_KEY ?= minioadmin
export MARGINCE_TEST_BLOBSTORE_BUCKET     ?= margince-test
# Dev-only trust switches (the X-Workspace-Slug header, AGENTS.md): make
# is the dev/test entry point, so dev is the right default HERE — a
# production deployment sets its own environment and never runs make.
export MARGINCE_ENV          ?= dev

# The composed workspace (ADR-0069): every build/test lane runs against
# build/composition/go.work, so an enabled extension set under
# extensions/ is compiled in — vanilla composes empty and the output is
# unchanged (the generator's vanilla wiring is byte-identical to the
# committed composition/ stub bare go commands resolve). `make
# composition` materializes it on every lane run (fast), so a stale
# composition is never built; gen-composition itself runs under the ROOT
# workspace — it must work before the composition exists.
COMPOSITION_DIR := $(abspath ../build/composition)
GOWORK_COMPOSED := GOWORK=$(COMPOSITION_DIR)/go.work

.DEFAULT_GOAL := help

.PHONY: help check build test test-v test-cover test-integration e2e-siteread e2e-ai e2e-ai-report test-db-up test-it test-integration-serial lint arch-lint vet gen gen-workflow drift composition check-composition test-extensions db-up db-init db-wait seed-reset seed-dev-db migrate migrate-up migrate-down run psql redis-cli tidy dev infra-down infra-logs infra-reset clean vuln tools tools-go hooks

help: ## List targets with their descriptions (the default goal)
	@grep -hE '^[a-zA-Z][a-zA-Z0-9_-]*:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-18s %s\n", $$1, $$2}'

check: build vet lint arch-lint test test-extensions drift check-composition ## The merge gate: everything a PR must pass

# gen-composition runs pinned to the ROOT workspace: the tool lives in
# the separate backend/tools module, so a caller-exported GOWORK (off, or
# an unrelated workspace) would break resolution before anything builds.
composition: ## Materialize build/composition/ from the enabled set under extensions/ (every build lane depends on this)
	@GOWORK=$(abspath ../go.work) $(GO) run ./tools/gen-composition

check-composition: composition ## Reproducibility gate: a clean regeneration must reproduce composition.json byte-for-byte
	@$(GO) run ./tools/gen-composition -verify
	@echo "OK: composition reproducible"

test-extensions: composition ## Every enabled extension's own test lane (each unit is its own module — ./... never reaches them), on the composed workspace
	@set -e; found=0; \
	for mod in ../extensions/*/go.mod; do \
		[ -e "$$mod" ] || continue; found=1; \
		dir=$$(dirname "$$mod"); \
		echo "== $$dir"; \
		(cd "$$dir" && $(GOWORK_COMPOSED) $(GO) test ./...); \
	done; \
	[ "$$found" = "1" ] || echo "test-extensions: no enabled Go extensions"

build: composition ## Compile every package (composed workspace)
	$(GOWORK_COMPOSED) $(GO) build ./...

vet: composition ## Run go vet over every package (composed workspace)
	$(GOWORK_COMPOSED) $(GO) vet ./...

test: composition ## Unit tests (the root fitness tests run uncached)
	$(GOWORK_COMPOSED) $(GO) test ./...
	# The tool chain is its own module, so ./... above never reaches it —
	# its tests (generator determinism, scaffold behavior) run here or
	# nowhere.
	$(GOWORK_COMPOSED) $(GO) test ./tools/...
	# The root fitness tests scan files OUTSIDE their package (WalkDir
	# over internal/modules, schema introspection), which Go's test
	# cache cannot key — run them uncached so a new module can never
	# ride a stale green.
	$(GOWORK_COMPOSED) $(GO) test -count=1 .

# -v is load-bearing, not chattiness: `go test` only names skipped tests in
# verbose mode, and the zero-skip teeth below grep for them. A skip in this
# lane means a security gate (RLS, erasure) silently didn't run — that must
# read as red, never as green.
test-integration: composition ## Integration lane: real Postgres, fails loudly without it (never skips). Parallel — each package on its own throwaway clone db.
	@$(GOWORK_COMPOSED) bash ../scripts/test-integration-parallel.sh

# The paid BYOK e2e targets below (e2e-siteread, e2e-ai) call a real cloud model,
# so they need the provider key (GEMINI_API_KEY, ANTHROPIC_API_KEY, …). `make dev`
# gets it by sourcing repo-root .env.local (scripts/dev.sh); these targets do the
# same, so they run against the same local binding out of the box instead of
# failing with "provider … needs an api key". Absent file → no-op (the offline
# fake / MARGINCE_E2E_MODEL path still works); the file wins over an inline env
# var, matching dev.sh. cwd is backend/, so .env.local is one level up.
ENV_LOCAL      := $(CURDIR)/../.env.local
LOAD_ENV_LOCAL := set -a; [ -f $(ENV_LOCAL) ] && . $(ENV_LOCAL); set +a;

e2e-siteread: composition ## Deep-read quality floor vs the real gradion.com (paid: real model + network; opt-in). BYOK key auto-loaded from .env.local; judge a model with MARGINCE_E2E_MODEL=provider:model or MARGINCE_AI_ROUTING.
	$(LOAD_ENV_LOCAL) $(GOWORK_COMPOSED) $(GO) test -tags e2e_llm ./internal/compose/ -run TestSiteReadE2E -v -count=1 -timeout 20m

# TestE2ECertify has no MARGINCE_E2E_MODEL-style fallback of its own — it
# hard-fails without a routing file — so, unlike e2e-siteread, e2e-ai
# supplies one: the per-engineer config/ai-routing.yaml `make install`
# already seeds, so `make e2e-ai` runs against your local binding out of
# the box. $(CURDIR) makes it absolute: `go test` runs the binary with its
# cwd set to the aicert package directory, not to backend/, so a relative
# "../config/..." would resolve from the wrong place. Override with
# MARGINCE_AI_ROUTING=<path> for a different one.
MARGINCE_AI_ROUTING ?= $(CURDIR)/../config/ai-routing.yaml

# TRACE names a directory for the payload trace — every candidate and judge
# call's post-stripper request+response (the ai_call_payload shape), dumped to
# a JSONL file whose path is printed to stdout for fast prompt tuning. ON by
# default (`TRACE=1`) because the corpus is a fixed hand-authored scenario set
# and the content is post-SecretStripper and written local-only (repo-root
# .tmp/aicert, gitignored) — no customer data, nothing to leak. `TRACE=<dir>`
# picks a directory; `TRACE=` (empty) turns it off.
TRACE ?= 1
ifeq ($(TRACE),1)
override TRACE := $(CURDIR)/../.tmp/aicert
endif

e2e-ai: composition ## Certify AI tasks vs the local binding (paid: real model + network; opt-in). BYOK key auto-loaded from .env.local. Payload trace ON by default (TRACE= to disable). TASK=x MODEL=prov:model RUNS=3 MARGINCE_AI_ROUTING=path
	$(LOAD_ENV_LOCAL) MARGINCE_AICERT=1 MARGINCE_AI_ROUTING=$(MARGINCE_AI_ROUTING) MARGINCE_AICERT_TASK=$(TASK) MARGINCE_AICERT_MODEL=$(MODEL) MARGINCE_AICERT_RUNS=$(RUNS) MARGINCE_AICERT_TRACE=$(TRACE) \
	  $(GOWORK_COMPOSED) $(GO) test -tags e2e_llm ./internal/compose/aicert/ -run TestE2ECertify -v -count=1 -timeout 30m

e2e-ai-report: ## Print the certification matrix (task x provider x model) from the committed records under internal/compose/aicert/records/.
	$(GO) run ./internal/compose/aicert/reportcmd

test-db-up: ## (Re)build the migrated margince_test template the parallel lane clones from
	@bash -c 'cd .. && source scripts/lib-testdb.sh && parse_test_dsn && build_template && echo "test-db-up: template $$TEMPLATE_NAME migrated to head"'

test-it: composition ## Run ONE integration package (optionally RUN=Regex) on a throwaway clone: make test-it DIR=backend/internal/modules/people [RUN=TestName]
	@$(GOWORK_COMPOSED) bash ../scripts/test-integration-one.sh "$(DIR)" "$(RUN)"

test-integration-serial: composition ## Escape hatch: the old sequential lane, all packages on the shared margince_test DB (debugging a parallel-isolation issue).
	@bash -c 'cd .. && source scripts/lib-testdb.sh && parse_test_dsn && ensure_template'
	@log=$$(mktemp); st=$$(mktemp); \
	{ $(GOWORK_COMPOSED) $(GO) test -tags integration -p 1 -v ./... 2>&1; echo $$? >"$$st"; } | tee "$$log"; \
	code=$$(cat "$$st"); rm -f "$$st"; \
	if [ "$$code" -ne 0 ]; then rm -f "$$log"; exit "$$code"; fi; \
	if grep -q -- '--- SKIP' "$$log"; then \
		echo "FAIL: the integration lane must not skip — provision the dependency, never skip:"; \
		grep -- '--- SKIP' "$$log"; rm -f "$$log"; exit 1; \
	fi; \
	rm -f "$$log"; echo "OK: integration passed with 0 skips"

# The integration lane above runs the same harness on the SMB tier as a
# standing canary; this target is the tier the PERF-7 SLO binds at.
bench-perf: composition ## PERF-3/PERF-7 benchmark harness on the mid-market §6.7 tier (needs db-up; seeds 250k contacts)
	MARGINCE_BENCH_TIER=mid_market $(GOWORK_COMPOSED) $(GO) test -tags integration -run TestPerfBudgetsHoldOnSeededVolumeTier -v -timeout 30m ./internal/compose/integration

# golangci-lint is the one gate tool this Makefile installs rather than
# runs via pinned `go run` (arch-lint/vuln/oasdiff do): `go run` would
# rebuild a ~60MB binary on every gate run. Resolving it is therefore
# this Makefile's job, not the engineer's shell config — `make tools`
# drops it in GOPATH/bin, which is on nobody's PATH by default, so a bare
# `golangci-lint` made `make check` fail after a clean `make install`
# with a confusing "No such file or directory".
#
# The pinned install wins over PATH deliberately: a golangci-lint from
# Homebrew (or any other version parked earlier in PATH) would otherwise
# silently lint at a version CI does not run, which is exactly what the
# GOLANGCI_LINT_VERSION pin exists to prevent. PATH is the fallback, so a
# deployment that resolves the pinned binary some other way still works.
GOLANGCI_LINT_BIN := $(shell $(GO) env GOPATH)/bin/golangci-lint
GOLANGCI_LINT ?= $(shell test -x $(GOLANGCI_LINT_BIN) && echo $(GOLANGCI_LINT_BIN) || command -v golangci-lint 2>/dev/null)

# Two passes, two scopes: .golangci.yml is the repo-wide
# baseline (the depguard DAG lives there and must never be diff-scoped);
# .golangci.strict.yml is the expanded strict set, gated to new
# code via new-from-merge-base so the pre-existing backlog is not a burn-down.
lint: composition ## golangci-lint: repo-wide baseline + new-code strict set
	@test -n "$(GOLANGCI_LINT)" || { \
		echo "lint: golangci-lint not found — run 'make tools' (installs $(GOLANGCI_LINT_VERSION) to $$($(GO) env GOPATH)/bin)"; \
		exit 1; \
	}
	$(GOWORK_COMPOSED) $(GOLANGCI_LINT) run ./...
	@if git rev-parse --verify -q origin/main >/dev/null; then \
		$(GOWORK_COMPOSED) $(GOLANGCI_LINT) run --config .golangci.strict.yml ./...; \
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
	$(GO) run ./tools/gen-aitasks -contract api/ai-tasks.yaml -out-go internal/modules/ai/tasks_gen.go -out-schema ../config/ai-routing.schema.json
	GOWORK=$(abspath ../go.work) $(GO) run ./tools/gen-composition

drift: gen ## Fail when any generated file drifted from the contract
	git diff --exit-code -- '*_gen.go' internal/contracts/ ../config/ai-routing.schema.json

# Scaffolding, not generation: gen-workflow writes a write-once handler +
# test stub for a developer to fill in (AC-W1) and is never part of
# `gen`/`drift` — nothing regenerates or drifts here, it just seeds one
# new pair of files per invocation.
gen-workflow: ## Scaffold a new workflow.Handler: make gen-workflow NAME=flag_idle_deals
	@test -n "$(NAME)" || { echo "gen-workflow: usage: make gen-workflow NAME=<snake_case_handler_name>"; exit 1; }
	$(GO) run ./tools/gen-workflow $(NAME)

# The scan runs in CI (.github/workflows/ci.yml), not in the local
# `make check`, so a missing network never blocks a local build. The
# version is PINNED — `@latest` makes the scan non-reproducible (a tool
# upgrade could change findings between two runs of the same commit).
GOVULNCHECK_VERSION ?= v1.1.4
vuln: ## Scan dependencies for known vulnerabilities (govulncheck)
	$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

# Fresh-machine bootstrap. golangci-lint is the one gate binary `make check`
# runs from an install rather than a pinned `go run`; the lint target
# resolves it out of GOPATH/bin itself (see GOLANGCI_LINT above), so no
# PATH entry is required. This version must stay identical to the pin in
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
	@echo "gate binaries ready in $$($(GO) env GOPATH)/bin — 'make check' finds them there; add it to PATH only for direct use"

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

seed-dev-db: ## Apply the dev DB seed for API-less demo data (scripts/seed-dev.sql); run after seed-dev
	$(COMPOSE) exec -T postgres psql -U margince_owner -d $(DB_NAME) -v ON_ERROR_STOP=1 < ../scripts/seed-dev.sql

migrate: ## Apply core + custom migrations (owner DSN)
	$(GO) run ./cmd/migrate up --dsn "$(OWNER_DSN)"

infra-down: ## Stop the dev stack but KEEP the data volumes (clean also drops them)
	$(COMPOSE) down --remove-orphans

clean: ## Tear down the dev stack, containers and data volumes
	$(COMPOSE) down --volumes --remove-orphans
	@docker rm -f fable-pg16 fable-redis 2>/dev/null || true

migrate-up: migrate ## Alias for migrate — apply all pending migrations (owner DSN)

migrate-down: ## Roll back the last migration(s): make migrate-down [STEPS=n]
	$(GO) run ./cmd/migrate down --dsn "$(OWNER_DSN)" --steps $(or $(STEPS),1)

run: composition ## Run the api on :8080 (no db-up/migrate) — go run ./cmd/api
	$(GOWORK_COMPOSED) $(GO) run ./cmd/api --dsn "$(APP_DSN)"

psql: ## Open a psql shell on the dev database (owner role)
	$(COMPOSE) exec postgres psql -U margince_owner -d $(DB_NAME)

redis-cli: ## Open a redis-cli shell on the dev Redis
	$(COMPOSE) exec redis redis-cli

tidy: ## Sync module deps (go mod tidy)
	$(GO) mod tidy

test-v: composition ## go test -v across the module
	$(GOWORK_COMPOSED) $(GO) test -v ./...

test-cover: composition ## Go unit tests with coverage summary
	$(GOWORK_COMPOSED) $(GO) test -cover ./...

db-wait: ## Block until Postgres answers pg_isready
	@until $(COMPOSE) exec -T postgres pg_isready -U margince_owner -d $(DB_NAME) >/dev/null 2>&1; do sleep 1; done; echo "postgres ready"

infra-logs: ## Tail the dev stack logs
	$(COMPOSE) logs -f

infra-reset: ## Stop + wipe volumes, then bring the stack back up (clean + db-up)
	$(MAKE) clean && $(MAKE) db-up

tools-go: tools ## Alias for tools — install the Go gate binaries
