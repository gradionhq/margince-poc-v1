# Golden path: `make dev` boots everything; `make check` is the merge gate.

GO        ?= go
PG_PORT   ?= 15432
REDIS_PORT?= 16379
DB_NAME   ?= margince
# An ambient MARGINCE_OWNER_DSN redirects every migrate target here, because it is
# the same variable cmd/migrate reads when no --dsn is given — the two can never
# name different databases. That also means a staging or production owner DSN
# exported in the invoking shell turns `make migrate-down` into remote destructive
# DDL, so both recipes announce their (credential-stripped) target before running.
# `.env.local` does NOT reach these targets: only the recipes carrying
# $(LOAD_ENV_LOCAL) source it. Override per invocation instead —
# `make migrate OWNER_DSN=…` — or export MARGINCE_OWNER_DSN in the shell.
OWNER_DSN ?= $(or $(MARGINCE_OWNER_DSN),postgres://margince_owner:dev@localhost:$(PG_PORT)/$(DB_NAME))
APP_DSN   ?= postgres://margince_app:margince_app_dev@localhost:$(PG_PORT)/$(DB_NAME)

# The owner DSN with its credential removed, for saying WHICH database a migrate
# target is about to touch without putting the password on the terminal or in a CI
# log. `make migrate-down` reverts schema; an operator has to be able to see where.
# Lazy (`=`, not `:=`) so the subshell runs only for the targets that reference
# it, and the sed delimiter is `|` because `#` would start a make comment and
# truncate the call.
REDACTED_OWNER_DSN = $(shell printf '%s' '$(OWNER_DSN)' | sed -E 's|//[^@/]*@|//***@|')

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
# The benchmark targets' own database. NOT the lane's template: the bench suite
# drops the schema and seeds a quarter of a million rows, and every per-package
# clone is copied from that template — see bench_db in scripts/lib-testdb.sh.
BENCH_DB_NAME                ?= margince_bench
export MARGINCE_TEST_APP_DSN ?= postgres://margince_app:margince_app_dev@localhost:$(PG_PORT)/$(TEST_DB_NAME)
export MARGINCE_TEST_REDIS    ?= localhost:$(REDIS_PORT)
export MARGINCE_TEST_REDIS_DB ?= 15
# Blobstore (MinIO) — the integration lane fails loudly without it, never
# skips, so the endpoint/creds/bucket are part of the compose + CI contract.
# The bucket is created by the store at first connect.
MINIO_PORT   ?= 29000
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
# The root workspace, which is what every backend/tools generator runs under:
# the tools are a separate module, and their resolution must not depend on
# what the caller exported.
GOWORK_ROOT := GOWORK=$(abspath ../go.work)

.DEFAULT_GOAL := help

.PHONY: perfdoc help check build test test-v test-cover test-integration e2e-siteread e2e-ai e2e-ai-report ai-probe test-db-up test-it test-integration-serial lint arch-lint vet gen gen-workflow mcp-apps-vocab drift composition check-composition test-extensions db-up db-init db-wait seed-reset seed-dev-db seed-demo migrate migrate-up migrate-down run psql redis-cli tidy dev infra-down infra-logs infra-reset clean vuln tools tools-go hooks

help: ## List targets with their descriptions (the default goal)
	@grep -hE '^[a-zA-Z][a-zA-Z0-9_-]*:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-18s %s\n", $$1, $$2}'

# build runs ALONE first: it compiles the dependency graph into the build
# cache exactly once. Fanning it out with the others makes every pass
# compile that graph concurrently — the cache does not block a second
# compiler on an in-flight entry, so parallel-from-cold multiplies the
# work and LOSES to serial on a small runner (measured in CI). The
# analysis passes then fan out as pure readers of the warmed cache
# (test-extensions reads it too — its `composition` prerequisite runs
# once inside the fan-out sub-make before the goals split). drift and
# check-composition stay last and serial: gen REWRITES *_gen.go,
# internal/contracts/ and build/composition/ in place — a
# write-under-reader race for any pass still compiling them.
check: ## The merge gate: everything a PR must pass
	$(MAKE) build
	$(MAKE) -j4 vet lint arch-lint test test-extensions
	$(MAKE) drift
	$(MAKE) check-composition

# gen-composition runs pinned to the ROOT workspace: the tool lives in
# the separate backend/tools module, so a caller-exported GOWORK (off, or
# an unrelated workspace) would break resolution before anything builds.
composition: ## Materialize build/composition/ from the enabled set under extensions/ (every build lane depends on this)
	@$(GOWORK_ROOT) $(GO) run ./tools/gen-composition

check-composition: composition ## Reproducibility gate: a clean regeneration must reproduce composition.json byte-for-byte
	@$(GOWORK_ROOT) $(GO) run ./tools/gen-composition -verify
	@echo "OK: composition reproducible"

# EXT_COVER_OUT (optional): merge every unit's -coverprofile into ONE
# report — first-party extension coverage reaches the Sonar quality gate
# like core coverage does, instead of hiding behind an exclusion.
test-extensions: composition ## Every enabled extension's own test lane (each unit is its own module — ./... never reaches them), on the composed workspace [EXT_COVER_OUT=<file> merges a coverage profile]
	@set -e; found=0; out="$(if $(EXT_COVER_OUT),$(abspath $(EXT_COVER_OUT)))"; \
	[ -z "$$out" ] || { rm -f "$$out"; tmp=$$(mktemp); trap 'rm -f "$$tmp"' EXIT; }; \
	for mod in ../extensions/*/go.mod; do \
		[ -e "$$mod" ] || continue; found=1; \
		dir=$$(dirname "$$mod"); \
		echo "== $$dir"; \
		if [ -n "$$out" ]; then \
			(cd "$$dir" && $(GOWORK_COMPOSED) $(GO) test -coverprofile="$$tmp" -coverpkg=./... ./...); \
			if [ -s "$$out" ]; then tail -n +2 "$$tmp" >> "$$out"; else cp "$$tmp" "$$out"; fi; \
		else \
			(cd "$$dir" && $(GOWORK_COMPOSED) $(GO) test ./...); \
		fi; \
	done; \
	[ "$$found" = "1" ] || echo "test-extensions: no enabled Go extensions"

build: composition ## Compile every package (composed workspace), host AND windows/amd64
	$(GOWORK_COMPOSED) $(GO) build ./...
	# The same tree for Windows, because the desktop bundle ships THESE binaries
	# there — `api`, `worker` and `migrate` are the server's, unmodified, and a
	# folder a Windows user runs is only as portable as they are.
	#
	# Without this the gate compiles the host GOOS only, so one Unix-only symbol
	# in a backend package breaks that bundle and nothing says so until somebody
	# builds it. That is not hypothetical: syscall.O_NOFOLLOW reached
	# internal/compose and was found by a Windows build days later, not by a
	# gate. It is the same argument the integration tag gets under `vet` below —
	# type-checking a lane the gate otherwise never compiles costs seconds
	# (measured: ~4s here) and belongs in the always-on gate.
	#
	# CGO_ENABLED=0 states what cross-compiling implies anyway, so the line does
	# not change meaning on a machine that has a Windows C toolchain.
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GOWORK_COMPOSED) $(GO) build ./...

vet: composition ## Run go vet over every package, untagged AND -tags 'integration bench' (composed workspace)
	$(GOWORK_COMPOSED) $(GO) vet ./...
	# The INTEGRATION lane, type-checked. `make check` otherwise never compiles
	# a single //go:build integration file — build, vet, lint and test all run
	# untagged — while internal/compose alone holds hundreds of them. A helper
	# renamed, a signature changed, or a name that collides with one declared in
	# a tagged file compiles clean untagged and fails only when somebody runs
	# `make test-it`, which the merge gate does not. That is how a duplicate
	# declaration reached a green check-q during this branch.
	#
	# vet, not test: type-checking the tagged lane needs no database, no Docker
	# and no fixtures, so it costs seconds and belongs in the always-on gate.
	# RUNNING those tests is the integration lane's job and stays there.
	#
	# `bench` rides along and needs this MORE than the integration lane does:
	# the bench files are run by hand from a make target, so unlike the
	# integration lane nothing scheduled ever compiles them. Setting a tag only
	# ADDS files, so this one invocation still covers everything `-tags
	# integration` alone would.
	$(GOWORK_COMPOSED) $(GO) vet -tags 'integration bench' ./...

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
	# Same reason, one module up: the ai package's config gates read
	# ../config/ai-routing*.{json,yaml}, which live OUTSIDE this module and so
	# are invisible to the cache. Editing the schema or an example alone
	# therefore replays the previous pass — verified: deleting the schema's
	# provider restriction left the gate reporting ok (cached). CI is cold and
	# never saw it; the engineer who broke it locally would not either.
	$(GOWORK_COMPOSED) $(GO) test -count=1 ./internal/modules/ai/

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

# AICERT_TIMEOUT is the whole-run backstop, not the per-call guard — a single
# call is already bounded by ai.requestTimeout (300s). It has to hold the FULL
# corpus (216 calls over 14 tasks) at the pace of the slowest binding an
# operator may certify: a measured EU/Mistral pass averages 2.4s/call (~9 min),
# a z-ai/glm-5.2 premium rung averages 9.7s with a 127s worst case and needs
# ~35 min. 30m cut that run off mid-corpus at 185 calls — and the failure is a
# panic, so every task after the cut loses its record. 90m carries a 25s/call
# mean; a run that overruns THAT is hung, not slow. Override for one run with
# AICERT_TIMEOUT=<dur>.
AICERT_TIMEOUT ?= 90m

e2e-ai: composition ## Certify AI tasks vs the local binding (paid: real model + network; opt-in). BYOK key auto-loaded from .env.local. Payload trace ON by default (TRACE= to disable). TASK=x MODEL=prov:model RUNS=3 MARGINCE_AI_ROUTING=path AICERT_TIMEOUT=90m
	$(LOAD_ENV_LOCAL) MARGINCE_AICERT=1 MARGINCE_AI_ROUTING=$(MARGINCE_AI_ROUTING) MARGINCE_AICERT_TASK=$(TASK) MARGINCE_AICERT_MODEL=$(MODEL) MARGINCE_AICERT_RUNS=$(RUNS) MARGINCE_AICERT_TRACE=$(TRACE) \
	  $(GOWORK_COMPOSED) $(GO) test -tags e2e_llm ./internal/compose/aicert/ -run TestE2ECertify -v -count=1 -timeout $(AICERT_TIMEOUT)

e2e-ai-report: ## Print the certification readiness report — every shipped site, its band, and whether its record is current, stale or absent — from internal/compose/aicert/{corpus,records}/.
	$(GO) run ./internal/compose/aicert/reportcmd

# ai-probe drives ONE invocation site against input an operator supplies, through
# the same certification case `make e2e-ai` drives. It answers a different
# question: e2e-ai asks whether a model is good enough for a prompt, this asks
# whether a site survives THIS input — which is how a site can be certified and
# still fail in the field.
#
# The paths are absolute because ARGS is the operator's and cwd is backend/,
# while the corpus is under it and .tmp/ is above it. list/scaffold/fetch cost
# nothing; only `run` calls a model, and --ai-fake makes even that free.
# Artifacts land in the gitignored .tmp/aitask — a fetched page carries whatever
# the source carried.
ai-probe: composition ## Probe one AI task site against real input (free except `run`, which calls a model). BYOK key auto-loaded from .env.local. ARGS='list' | ARGS='scaffold <task>/<variant>' | ARGS='fetch <url>' | ARGS='run --site <task>/<variant> --fixture f.json --ai-routing ../config/ai-routing.yaml'
	$(LOAD_ENV_LOCAL) $(GOWORK_COMPOSED) $(GO) run ./cmd/worker aitask $(ARGS) \
	  --corpus $(CURDIR)/internal/compose/aicert/corpus --work-dir $(CURDIR)/../.tmp/aitask

# Composed for the same reason as migrate: build_template shells out to
# cmd/migrate, and a template built from the vanilla stub carries no
# extension tables — every clone taken from it would then fail (or, worse,
# skip) the extension half of the integration lane.
test-db-up: composition ## (Re)build the migrated margince_test template the parallel lane clones from
	@$(GOWORK_COMPOSED) bash -c 'cd .. && source scripts/lib-testdb.sh && parse_test_dsn && build_template && echo "test-db-up: template $$TEMPLATE_NAME migrated to head"'

test-it: composition ## Run ONE integration package (optionally RUN=Regex) on a throwaway clone: make test-it DIR=backend/internal/modules/people [RUN=TestName]
	@$(GOWORK_COMPOSED) bash ../scripts/test-integration-one.sh "$(DIR)" "$(RUN)"

# The lane's connection ceiling and budget, for a lane that runs ONE package at a
# time. Declared in scripts/lib-testdb.sh like every other lane's, and evaluated
# lazily (`=`, not `:=`) so a make invocation that never runs the serial lane
# does not pay for a shell. The harness asserts both rather than skipping: this
# target fails on any SKIP, so a capacity check that quietly opted out would take
# the whole lane red for a reason that has nothing to do with the tests.
LANE_BUDGET_ENV = $(shell bash -c 'cd .. && source scripts/lib-testdb.sh && declare_lane_budget 1 && echo LANE_CONN_BUDGET=$$LANE_CONN_BUDGET MARGINCE_TEST_POOL_MAX_CONNS=$$MARGINCE_TEST_POOL_MAX_CONNS')

test-integration-serial: composition ## Escape hatch: the old sequential lane, all packages on the shared margince_test DB (debugging a parallel-isolation issue).
	@$(GOWORK_COMPOSED) bash -c 'cd .. && source scripts/lib-testdb.sh && parse_test_dsn && ensure_template'
	@log=$$(mktemp); st=$$(mktemp); \
	{ $(GOWORK_COMPOSED) env $(LANE_BUDGET_ENV) $(GO) test -tags integration -p 1 -v ./... 2>&1; echo $$? >"$$st"; } | tee "$$log"; \
	code=$$(cat "$$st"); rm -f "$$st"; \
	if [ "$$code" -ne 0 ]; then rm -f "$$log"; exit "$$code"; fi; \
	if grep -q -- '--- SKIP' "$$log"; then \
		echo "FAIL: the integration lane must not skip — provision the dependency, never skip:"; \
		grep -- '--- SKIP' "$$log"; rm -f "$$log"; exit 1; \
	fi; \
	rm -f "$$log"; echo "OK: integration passed with 0 skips"

# PERF-3/PERF-7 at the tier the SLO binds at, and the only target that WRITES a
# record. The suite used to ride the standing integration lane at the SMB tier
# as well; it does not any more (see its own file comment), so this target and
# bench-perf-check below are the two ways it runs.
bench-perf: composition ## PERF-3/PERF-7 benchmark harness on the mid-market §6.7 tier (needs db-up; seeds 250k contacts)
	@$(GOWORK_COMPOSED) bash -c 'cd .. && source scripts/lib-testdb.sh && parse_test_dsn && bench_db $(BENCH_DB_NAME) \
	  && cd backend && MARGINCE_BENCH_TIER=mid_market MARGINCE_BENCH_RECORD=1 \
	     MARGINCE_TEST_DSN="$$BENCH_OWNER_DSN" MARGINCE_TEST_APP_DSN="$$BENCH_APP_DSN" \
	     $(GO) test -tags "integration bench" -run TestPerfBudgetsHoldOnSeededVolumeTier -count=1 -v -timeout 30m ./internal/compose/integration'
	@$(MAKE) --no-print-directory perfdoc

# The SMB budgets, recording NOTHING — what the weekly scheduled workflow runs.
# Records are a human's to publish: a machine writing its own numbers into the
# tree is the failure MARGINCE_BENCH_RECORD exists to prevent, and a scheduled
# job is exactly the machine meant.
#
# SMB ONLY, and the omission is the honest half of this target. Mid-market is
# where the PERF-7 SLO binds, so a drift alarm would ideally watch it — but its
# seed is 250k persons, 500k activities and 50k relationships, and it exceeded
# `go test`'s 30m budget when this target was first run against it (measured:
# SMB 46.6s, mid-market FAIL at 1800.7s, on a fast laptop; a 4-core runner is
# slower). A scheduled job that cannot finish does not report "slow" — it goes
# red, and scripts/scheduled-report.sh files an issue saying a budget is
# breaching, which would be a fabricated finding every week.
#
# So the alarm watches the corpus it can finish. SMB is what most installations
# look like, and drift there is real drift. Mid-market stays with `bench-perf`,
# by hand, where a person is watching the clock — and a PERF-7 row measured
# below mid-market renders `inconclusive` in the published page rather than
# claiming a bound it did not test.
bench-perf-check: composition ## PERF-3/PERF-7 on the SMB tier, no record written (needs db-up; the scheduled lane's target)
	@$(GOWORK_COMPOSED) bash -c 'cd .. && source scripts/lib-testdb.sh && parse_test_dsn && bench_db $(BENCH_DB_NAME) \
	  && cd backend && MARGINCE_BENCH_TIER=smb MARGINCE_TEST_DSN="$$BENCH_OWNER_DSN" MARGINCE_TEST_APP_DSN="$$BENCH_APP_DSN" \
	     $(GO) test -tags "integration bench" -run TestPerfBudgetsHoldOnSeededVolumeTier -count=1 -v -timeout 20m ./internal/compose/integration'

# The `bench` lane: measurements run BY HAND, never by a merge gate.
#
# Every suite reached from here carries `integration && bench`, so no MERGE gate
# runs any of them. `make vet` and both golangci passes still TYPE-CHECK them,
# which is the only thing standing between a by-hand lane and rot.
#
# bench-perf and bench-perf-check above are the one exception to "by hand": the
# scheduled workflow runs bench-perf-check weekly, because PERF-3/PERF-7 lost
# its standing merge-gate canary and drift nobody looks for is drift nobody
# finds. It still writes no record — publishing a number stays a human's act.
#
# Separate targets rather than one, because they measure unrelated promises and
# cost unrelated amounts of time: a reader chasing a slow save should not have
# to seed a mailbox to find out.
#
# `-count=1` on all three is load-bearing, not habit. Go caches a passing test
# result keyed on the binary and its inputs, and a cached PASS replays the
# ORIGINAL run's log — so a second `make bench-record` printed p50=3.276875ms a
# second time, to the nanosecond, having measured nothing. A benchmark that can
# answer from cache reports the past as the present, which is worse than not
# running: the number looks fresh and is not.
bench-record: composition ## PERF-1/PERF-4 record open + save budgets, measured over HTTP (needs db-up)
	$(GOWORK_COMPOSED) $(GO) test -tags 'integration bench' -run TestRecordOpenAndSaveBudgets -count=1 -v -timeout 15m ./internal/compose/integration
	@$(MAKE) --no-print-directory perfdoc

bench-capture: composition ## CAP-PARAM-1 capture-to-timeline latency, 60s p95 (needs db-up)
	$(GOWORK_COMPOSED) $(GO) test -tags 'integration bench' -run TestCaptureToTimelineLatencyBudget -count=1 -v -timeout 15m ./internal/compose/integration/capture
	@$(MAKE) --no-print-directory perfdoc

# The published page is re-rendered from EVERY record, not only the one the run
# just wrote, so a partial run still produces a complete and current page: the
# rows it did not measure keep their own stamps and their own dates.
#
# Its own target as well as a step, because a reader who edits the budget table
# in gen-perfdoc needs to re-render without paying for a measurement.
perfdoc: ## Re-render docs/reference/performance-budgets.md from the committed benchmark records
	@$(GOWORK_ROOT) $(GO) run ./tools/gen-perfdoc

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
#
# Both go through ../scripts/run-golangci.sh, the wrapper `make lint-modules`
# also uses: golangci's analysis cache is machine-wide and content-keyed, so in
# a tree worked in several worktrees at once it re-reports another checkout's
# issues — with that checkout's path, and therefore without the //nolint: and
# path-anchored waivers that path carried. This lane is exposed to it exactly
# as the module lane is, so it is guarded in the same place rather than twice.
lint: composition ## golangci-lint: repo-wide baseline + new-code strict set
	@test -n "$(GOLANGCI_LINT)" || { \
		echo "lint: golangci-lint not found — run 'make tools' (installs $(GOLANGCI_LINT_VERSION) to $$($(GO) env GOPATH)/bin)"; \
		exit 1; \
	}
	$(GOWORK_COMPOSED) GOLANGCI_LINT="$(GOLANGCI_LINT)" ../scripts/run-golangci.sh run ./...
	@if git rev-parse --verify -q origin/main >/dev/null; then \
		$(GOWORK_COMPOSED) GOLANGCI_LINT="$(GOLANGCI_LINT)" ../scripts/run-golangci.sh run --config .golangci.strict.yml ./...; \
	else \
		echo "SKIP strict lint: origin/main not found — fetch it to arm the new-code gate"; \
	fi

# Gate-tool resolution: prefer the install from `make tools` (CI restores
# it from cache), but only when its embedded module version (`go version
# -m`) matches the pin — a stale local install must never gate at a
# version CI does not run. Anything else falls back to a pinned `go run`,
# which re-downloads and recompiles the tool on every invocation: correct
# on a fresh machine, waste everywhere else.
#   $(call resolve-gate-tool,<binary>,<module regex>,<version>,<install path>)
# The binary branch is emitted pre-quoted: the value lands unquoted in a
# recipe line, and a GOBIN/GOPATH with spaces must not word-split there.
# The go run fallback stays bare — it is deliberately multi-word.
GATE_BIN_DIR := $(or $(shell $(GO) env GOBIN),$(shell $(GO) env GOPATH)/bin)
resolve-gate-tool = $(shell $(GO) version -m '$(GATE_BIN_DIR)/$(1)' 2>/dev/null \
	| grep -qE '[[:space:]]mod[[:space:]]+$(2)[[:space:]]+$(3)([[:space:]]|$$)' \
	&& echo '"$(GATE_BIN_DIR)/$(1)"' || echo "$(GO) run $(4)@$(3)")

# Layer C of the boundary stack — a HARD gate: a forbidden import edge
# fails `make check`, it is never advisory.
GO_ARCH_LINT_VERSION ?= v1.12.0
GO_ARCH_LINT ?= $(call resolve-gate-tool,go-arch-lint,github\.com/fe3dback/go-arch-lint,$(GO_ARCH_LINT_VERSION),github.com/fe3dback/go-arch-lint)
arch-lint: ## Import-boundary lint (go-arch-lint over .go-arch-lint.yml)
	$(GO_ARCH_LINT) check

# The stub and agent-policy generators run here explicitly (not via
# `go generate ./...`): internal/compose is generated INTO, and its own
# generate directives would need the tools resolvable from that package.
#
# EVERY line below runs pinned to the ROOT workspace, for the reason
# gen-composition already did: the generators live in the separate
# backend/tools module — including the two the contracts package's own
# directives shell out to — so what a caller happens to export as GOWORK (off,
# or the composed workspace, or an unrelated one) decides which module graph
# resolves their dependencies. Generated files are committed and drift-gated,
# so a generator whose own build inputs move with the caller's environment
# turns a clean checkout into a red gate on somebody else's machine.
#
# gen is two-phase, and the ORDER is the contract between them.
#
# Phase 1 is gen-composition. It merges every enabled unit's api/ fragments
# into build/composition/api/ — the EFFECTIVE contracts for this
# installation. It used to run last, which was safe only while nothing read
# its output; the composed contracts are inputs now, so a generator that ran
# before them would compile the vanilla surface into a composed build and
# look correct, because a stale build/composition/ from the previous run is
# already on disk. Running it first is what makes that impossible.
#
# gen-composition itself reads only committed files (the base contracts, the
# unit trees, go.work, the composition stub), so it has no dependency back on
# phase 2 and works from a deleted build/composition/.
#
# Phase 2 are the committed, drift-gated generators. They read the BASE
# contracts on purpose: their outputs are checked in and must be identical on
# every installation. The composed lane's own artifacts are emitted into
# build/composition/ beside the merged contracts (frontend/extensions.gen.ts
# today, the composed client types and docs as their slices land) — the same
# split Task 11's tsconfig alias makes for TypeScript.
gen: mcp-apps-vocab ## Regenerate everything derived from the contract
	$(GOWORK_ROOT) $(GO) run ./tools/gen-composition
	$(GOWORK_ROOT) $(GO) generate ./internal/contracts
	$(GOWORK_ROOT) $(GO) generate ./pkg/extension/crm
	$(GOWORK_ROOT) $(GO) run ./tools/gen-stubs
	$(GOWORK_ROOT) $(GO) run ./tools/gen-agentpolicy -in api/crm.yaml -out internal/compose/agentpolicy_gen.go
	$(GOWORK_ROOT) $(GO) run ./tools/gen-recordfields -in api/crm.yaml -out internal/modules/agents/recordshapes_gen.go
	$(GOWORK_ROOT) $(GO) run ./tools/gen-aitasks -contract api/ai-tasks.yaml -out-go internal/modules/ai/tasks_gen.go -out-schema ../config/ai-routing.schema.json
	$(GOWORK_ROOT) $(GO) run ./tools/gen-jobs -contract api/jobs.yaml \
		-out-specs internal/platform/jobs/specs_gen.go \
		-out-kinds internal/compose/jobkinds_gen.go

drift: gen ## Fail when any generated file drifted from the contract
	# gen regenerates the committed per-unit manifests too (via composition);
	# without them here a stale or hand-edited extensions/<name>/manifest.generated.json
	# would be silently healed by the mutating gen step instead of failing.
	git diff --exit-code -- '*_gen.go' internal/contracts/ ../config/ai-routing.schema.json '../extensions/*/manifest.generated.json' internal/modules/agents/apps/forbidden.json
	# git diff ignores UNTRACKED files, so a new (or gen-recreated after a
	# committed deletion) manifest that was never `git add`ed would slip past
	# the diff above — fail explicitly when a generated manifest is uncommitted.
	@u="$$(git ls-files --others --exclude-standard -- '../extensions/*/manifest.generated.json')"; \
		[ -z "$$u" ] || { echo "drift: uncommitted generated manifest(s):" >&2; echo "$$u" >&2; exit 1; }

# The MCP App admission vocabulary is AUTHORED in frontend/src/mcp-apps, where
# the build-time validator imports it, and copied here because go:embed cannot
# reach outside its own package directory. Two validators in two languages are a
# defect unless they cannot disagree: a document that passes the build check and
# is refused in production is an outage no CI lane can see. The copy is
# committed; TestTheAdmissionVocabularyMatchesTheFrontend is what stops it
# drifting, and `drift` fails a stale one.
mcp-apps-vocab: ## Copy the MCP App admission vocabulary in from the frontend
	cp ../frontend/src/mcp-apps/forbidden.json internal/modules/agents/apps/forbidden.json

# Scaffolding, not generation: gen-workflow writes a write-once handler +
# test stub for a developer to fill in (AC-W1) and is never part of
# `gen`/`drift` — nothing regenerates or drifts here, it just seeds one
# new pair of files per invocation.
gen-workflow: ## Scaffold a new workflow.Handler: make gen-workflow NAME=flag_idle_deals
	@test -n "$(NAME)" || { echo "gen-workflow: usage: make gen-workflow NAME=<snake_case_handler_name>"; exit 1; }
	$(GOWORK_ROOT) $(GO) run ./tools/gen-workflow $(NAME)

# The scan runs in CI (.github/workflows/ci.yml), not in the local
# `make check`, so a missing network never blocks a local build. The
# version is PINNED — `@latest` makes the scan non-reproducible (a tool
# upgrade could change findings between two runs of the same commit).
# govulncheck's module is golang.org/x/vuln; the binary installs from the
# cmd/ subpath — resolve-gate-tool takes both.
GOVULNCHECK_VERSION ?= v1.1.4
GOVULNCHECK ?= $(call resolve-gate-tool,govulncheck,golang\.org/x/vuln,$(GOVULNCHECK_VERSION),golang.org/x/vuln/cmd/govulncheck)
vuln: ## Scan dependencies for known vulnerabilities (govulncheck)
	$(GOVULNCHECK) ./...

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

# MARGINCE_SEED_DSN is not optional in practice, and the root target always
# supplies it: without a DSN the seeder silently skips teams, seats, finance
# links and facts, then fails in the ownership pass with "no seats to own
# anything". Run this through the root `make seed-demo`, which reads the
# password and the DSN for you.
seed-demo: composition ## Fill a running stack from the demo dataset (prefer the root `make seed-demo`)
	$(GOWORK_COMPOSED) $(GO) run ./tools/seed-demo -dataset "$(DATASET)" $(SEED_ARGS)

# The COMPOSED workspace is load-bearing here, not tidiness: cmd/migrate
# applies the enabled units' migrations from the composed extension set, and
# a bare `go run` resolves the committed vanilla stub instead — which returns
# no extensions, applies zero extension migrations, exits 0 and prints
# "schema is at head". Every extension lane would then be green over a
# database that has none of their tables.
migrate migrate-down: export MARGINCE_OWNER_DSN := $(OWNER_DSN)

migrate: composition ## Apply core + custom + composed extension migrations (owner DSN)
	@echo "migrate: applying to $(REDACTED_OWNER_DSN)"
	$(GOWORK_COMPOSED) $(GO) run ./cmd/migrate up

infra-down: ## Stop the dev stack but KEEP the data volumes (clean also drops them)
	$(COMPOSE) down --remove-orphans

clean: ## Tear down the dev stack, containers and data volumes
	$(COMPOSE) down --volumes --remove-orphans
	@docker rm -f fable-pg16 fable-redis 2>/dev/null || true

migrate-up: migrate ## Alias for migrate — apply all pending migrations (owner DSN)

# The version is the current unix second, not the next number in a sequence:
# two branches numbering against the same main claim one number and main stops
# loading. scripts/new-migration.sh carries the full reasoning.
#
# NAME reaches the script through the ENVIRONMENT, and the recipe expands
# $$MIGRATION_NAME rather than $(NAME). make substitutes its own variables into
# the recipe text before any shell sees it, so a quoted $(NAME) is not quoting
# at all — a name carrying a backquote or a quote of its own closes the string
# and the rest runs as shell, well before the script's validation could refuse
# it. Passing the value in the environment leaves the shell nothing to reparse.
migrate-create: export MIGRATION_NAME := $(NAME)
migrate-create: ## Scaffold a core migration pair: make migrate-create NAME=add_renewal_risk
	@bash ../scripts/new-migration.sh "$$MIGRATION_NAME"

migrate-down: composition ## Roll back the last migration(s): make migrate-down [STEPS=n]
	@echo "migrate-down: reverting $(or $(STEPS),1) on $(REDACTED_OWNER_DSN)"
	$(GOWORK_COMPOSED) $(GO) run ./cmd/migrate down --steps $(or $(STEPS),1)

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
