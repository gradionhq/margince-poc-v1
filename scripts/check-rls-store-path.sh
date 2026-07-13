#!/usr/bin/env bash
# RLS store-path gate (adapted from the foundation skeleton to this repo's
# WithWorkspaceTx seam). The dev/app pool runs as a superuser role that
# BYPASSES FORCE RLS, so a per-workspace statement issued against the bare
# pool has ZERO tenant isolation — the `WHERE workspace_id=$1` predicate is
# not a substitute. Every per-workspace statement must run inside
# database.WithWorkspaceTx (which SETs app.workspace_id + LOCAL ROLE
# margince_app) and address the transaction (`tx.Exec`/`tx.Query`), never
# `<recv>.pool.{Exec,Query,QueryRow}`.
#
# This is the cheap, DB-free floor under the runtime proof in
# rls_coverage_integration_test.go: it fails fast in the deterministic lane
# when a store addresses the pool directly. The sole sanctioned escape hatch
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
