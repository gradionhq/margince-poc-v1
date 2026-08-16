#!/usr/bin/env bash
# Store-path gate for this repo's WithWorkspaceTx seam.
#
# Core 0217 (ADR-0091) retired every tenant-isolation policy, so the tenant
# predicate now lives in each statement — `workspace_id =
# NULLIF(current_setting('app.workspace_id', true), '')::uuid` — and the ONE
# thing that binds the GUC it reads is database.WithWorkspaceTx. A
# per-workspace statement issued against the bare pool therefore reads an
# unbound GUC: the predicate resolves against NULL and the statement quietly
# answers about nothing, or, if the caller passed the tenant as a parameter
# instead, it answers correctly while sitting outside the only seam that makes
# that checkable. Both are why every per-workspace statement addresses the
# transaction (`tx.Exec`/`tx.Query`), never `<recv>.pool.{Exec,Query,QueryRow}`.
#
# It is a deterministic-lane gate: no database, fails fast when a store
# addresses the pool directly. The sole sanctioned escape hatch
# — a genuinely cross-workspace/system query (e.g. the worker loops that
# iterate every workspace before entering a per-workspace tx) — is a
# `// rls-exempt: <reason>` comment on the line immediately above. Use it
# sparingly.
set -euo pipefail
cd "$(dirname "$0")/.."

dir="backend/internal/modules"

# One awk pass over every non-test module .go file; prev resets per file.
files="$(find "$dir" -name '*.go' ! -name '*_test.go' | sort)"
violations="$(echo "$files" | xargs awk '
  FNR == 1 { prev = "" }
  $0 ~ /\.pool\.(ExecContext|QueryContext|QueryRowContext|Exec|Query|QueryRow)\(/ {
    if (prev !~ /\/\/[[:space:]]*rls-exempt:/) {
      line = $0; sub(/^[[:space:]]+/, "", line)
      printf "%s:%d: %s\n", FILENAME, FNR, line
    }
  }
  { prev = $0 }
')"

if [[ -n "$violations" ]]; then
  echo "FAIL — module statements addressing the superuser pool directly (RLS bypassed):"
  echo "$violations"
  echo
  echo "Route each through database.WithWorkspaceTx and address the tx, not the pool,"
  echo "or, for a genuinely cross-workspace query, add a '// rls-exempt: <reason>' line above it."
  exit 1
fi

echo "OK: rls-store-path — no module statement addresses the superuser pool directly"
