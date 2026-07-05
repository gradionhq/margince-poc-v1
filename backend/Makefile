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
# Dev-only trust switches (the X-Workspace-Slug header, AGENTS.md): make
# is the dev/test entry point, so dev is the right default HERE — a
# production deployment sets its own environment and never runs make.
export MARGINCE_ENV          ?= dev

.DEFAULT_GOAL := help

.PHONY: help check build test test-integration lint arch-lint vet gen drift db-up db-init migrate dev clean vuln hooks

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

test-integration: ## Integration lane: real Postgres, fails loudly without it (never skips)
	$(GO) test -tags integration -p 1 ./...

lint: ## golangci-lint (depguard, gosec, misspell, revive, gofmt)
	golangci-lint run ./...

# Layer C of the boundary stack — a HARD gate: a forbidden import edge
# fails `make check`, it is never advisory.
arch-lint: ## Import-boundary lint (go-arch-lint over .go-arch-lint.yml)
	$(GO) run github.com/fe3dback/go-arch-lint@v1.12.0 check

# The stub and agent-policy generators run here explicitly (not via
# `go generate ./...`): internal/compose is generated INTO, and its own
# generate directives would need the tools resolvable from that package.
gen: ## Regenerate everything derived from the contract
	$(GO) generate ./internal/contracts
	$(GO) run ./tools/gen-stubs
	$(GO) run ./tools/gen-agentpolicy -in api/crm.yaml -out internal/compose/agentpolicy_gen.go

drift: gen ## Fail when any generated file drifted from the contract
	git diff --exit-code -- '*_gen.go' internal/contracts/

# Not part of `make check` yet — CI wiring for the vulnerability scan
# comes later.
vuln: ## Scan dependencies for known vulnerabilities (govulncheck)
	$(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...

# `git rev-parse --git-path hooks` honors core.hooksPath, so this works
# both on a plain clone (.git/hooks) and after the root `make hooks`
# switched the repo to .githooks/ for the pre-push craft gate.
hooks: ## Install the repo pre-commit hook (gofmt + license-header gate)
	install -m 0755 ../scripts/pre-commit "$$(git rev-parse --git-path hooks)/pre-commit"

db-up: ## Start PG16 + Redis 7 containers and create the app role
	docker start fable-pg16 2>/dev/null || \
	  docker run -d --name fable-pg16 -e POSTGRES_USER=margince_owner -e POSTGRES_PASSWORD=dev \
	    -e POSTGRES_DB=$(DB_NAME) -p $(PG_PORT):5432 pgvector/pgvector:pg16
	docker start fable-redis 2>/dev/null || \
	  docker run -d --name fable-redis -p $(REDIS_PORT):6379 redis:7
	@until docker exec fable-pg16 pg_isready -U margince_owner -q; do sleep 0.5; done
	$(MAKE) db-init
db-init: ## (Re)apply scripts/db-init.sql to the running Postgres
	docker exec -i fable-pg16 psql -U margince_owner -d $(DB_NAME) < ../scripts/db-init.sql

migrate: ## Apply core + custom migrations (owner DSN)
	$(GO) run ./cmd/migrate up --dsn "$(OWNER_DSN)"

dev: db-up migrate ## db-up + migrate + api on :8080
	$(GO) run ./cmd/api --dsn "$(APP_DSN)"

clean: ## Remove the dev Postgres/Redis containers
	docker rm -f fable-pg16 fable-redis 2>/dev/null || true
