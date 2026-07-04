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

.PHONY: check build test test-integration lint arch-lint vet gen drift db-up db-init migrate dev clean

## The merge gate: everything a PR must pass.
check: build vet lint arch-lint test drift

build:
	$(GO) build ./...

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...
	# The root fitness tests scan files OUTSIDE their package (WalkDir
	# over internal/modules, schema introspection), which Go's test
	# cache cannot key — run them uncached so a new module can never
	# ride a stale green.
	$(GO) test -count=1 .

## Integration lane: real Postgres, fails loudly without it (never skips).
test-integration:
	$(GO) test -tags integration -p 1 ./...

lint:
	golangci-lint run ./...

## Layer C of the boundary stack — a HARD gate: a forbidden import edge
## fails `make check`, it is never advisory.
arch-lint:
	$(GO) run github.com/fe3dback/go-arch-lint@v1.12.0 check

## Regenerate everything derived from the contract, then fail on drift.
gen:
	$(GO) generate ./...

drift: gen
	git diff --exit-code -- '*_gen.go' internal/contracts/

db-up:
	docker start fable-pg16 2>/dev/null || \
	  docker run -d --name fable-pg16 -e POSTGRES_USER=margince_owner -e POSTGRES_PASSWORD=dev \
	    -e POSTGRES_DB=$(DB_NAME) -p $(PG_PORT):5432 pgvector/pgvector:pg16
	docker start fable-redis 2>/dev/null || \
	  docker run -d --name fable-redis -p $(REDIS_PORT):6379 redis:7
	@until docker exec fable-pg16 pg_isready -U margince_owner -q; do sleep 0.5; done
	$(MAKE) db-init

db-init:
	docker exec -i fable-pg16 psql -U margince_owner -d $(DB_NAME) < ../scripts/db-init.sql

migrate:
	$(GO) run ./cmd/migrate up --dsn "$(OWNER_DSN)"

dev: db-up migrate
	$(GO) run ./cmd/api --dsn "$(APP_DSN)"

clean:
	docker rm -f fable-pg16 fable-redis 2>/dev/null || true
