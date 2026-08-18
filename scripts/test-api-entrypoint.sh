#!/usr/bin/env bash
# test-api-entrypoint.sh — prove the container entrypoint never leaves a
# plaintext bootstrap credential at rest on a provisioned installation.
#
# ADR-0061 §2 consumes bootstrap values exactly once, and the entrypoint is the
# only thing standing between MARGINCE_ADMIN_PASSWORD and a file on disk. Before
# the probe existed it rewrote that file on EVERY container start, for the life
# of the installation, so the deletability the file design rests on held only if
# an operator remembered to unset a variable that nothing checked.
#
# What makes this worth a test rather than a reading: every failure here is
# silent. A credential written to a provisioned installation looks exactly like
# one that was not; a probe that errors and is read as "unprovisioned" looks
# exactly like a fresh install. The entrypoint runs in a container nobody
# watches, and none of these paths raises anything a deployment would notice.
#
# `margince-migrate` and `margince-api` are stubbed on PATH — this tests the
# ENTRYPOINT's decisions, and running real migrations would test the database.
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
entrypoint="$root/scripts/deploy/api-entrypoint.sh"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
failures=0

# run PROBE_ANSWER PASSWORD [preexisting-file-contents]
#
# PROBE_ANSWER is what the stubbed `margince-migrate org-exists` reports:
# "true", "false", or "fail" to exit non-zero without answering. Echoes the
# entrypoint's exit status; leaves its output in $out and the credential file at
# $pwfile for the caller to assert on.
run() {
    local probe="$1" password="${2:-}" preexisting="${3:-}"
    rm -rf "$work/app" "$work/bin"
    mkdir -p "$work/app/secrets" "$work/bin"

    cat >"$work/bin/margince-migrate" <<STUB
#!/bin/sh
[ "\$1" = "org-exists" ] || exit 0
[ "$probe" = "fail" ] && { echo "stub: probe failed" >&2; exit 1; }
echo "$probe"
STUB
    printf '#!/bin/sh\necho "stub: api started"\n' >"$work/bin/margince-api"
    chmod +x "$work/bin/margince-migrate" "$work/bin/margince-api"

    if [ -n "$preexisting" ]; then
        printf '%s' "$preexisting" >"$work/app/secrets/admin-password"
    fi

    local status=0
    # The entrypoint writes to the absolute /app/secrets; run it under a
    # rewritten copy so the test needs no container and no root.
    sed "s#/app/secrets/admin-password#$work/app/secrets/admin-password#" "$entrypoint" >"$work/entrypoint.sh"
    chmod +x "$work/entrypoint.sh"
    out="$(
        PATH="$work/bin:$PATH" \
        MARGINCE_OWNER_DSN="postgres://owner@localhost/x" \
        MARGINCE_DSN="postgres://app@localhost/x" \
        MARGINCE_ADMIN_PASSWORD="$password" \
        "$work/entrypoint.sh" 2>&1
    )" || status=$?
    pwfile="$work/app/secrets/admin-password"
    return "$status"
}

check() { # description condition-already-evaluated
    if [ "$1" = "pass" ]; then
        printf '  ok   %s\n' "$2"
    else
        printf '  FAIL %s\n' "$2" >&2
        failures=$((failures + 1))
    fi
}

verdict() { # want-true actual description
    if [ "$1" = "$2" ]; then check pass "$3"; else check fail "$3 (got: $2)"; fi
}

echo "api-entrypoint: the credential is written only onto an unprovisioned installation"

# --- an already-bootstrapped installation ------------------------------------
run true "s3cret-passw0rd" || true
verdict absent "$([ -e "$pwfile" ] && echo present || echo absent)" \
    "a provisioned installation is never handed the supplied credential"
verdict yes "$(grep -q "already has an organization" <<<"$out" && echo yes || echo no)" \
    "and says so, because an ignored credential must not look like an applied one"

# A file an EARLIER boot wrote is spent the moment the organization exists. The
# invariant is that none is at rest, not merely that this start added none.
run true "" "left-by-an-earlier-boot" || true
verdict absent "$([ -e "$pwfile" ] && echo present || echo absent)" \
    "a credential left by an earlier boot is retired once the organization exists"

# --- a fresh installation ----------------------------------------------------
run false "s3cret-passw0rd" || true
verdict present "$([ -e "$pwfile" ] && echo present || echo absent)" \
    "an unprovisioned installation gets the credential it was given"
verdict s3cret-passw0rd "$(cat "$pwfile" 2>/dev/null || echo '')" \
    "written verbatim, with no trailing newline a password would absorb"
# GNU first, and the order is load-bearing rather than alphabetical. BSD `stat
# -c` fails cleanly and falls through; GNU `stat -f` means "filesystem status",
# so on Linux it SUCCEEDS on a file argument and prints something that is not a
# mode — a BSD-first probe never reaches the fallback and silently compares
# against garbage. It passed on a laptop and failed only in CI.
verdict 600 "$(stat -c '%a' "$pwfile" 2>/dev/null || stat -f '%OLp' "$pwfile" 2>/dev/null)" \
    "0600, so nothing else in the container can read it"

run false "" || true
verdict absent "$([ -e "$pwfile" ] && echo present || echo absent)" \
    "no variable, no file — a fresh install may bootstrap by other means"

# --- the probe cannot answer -------------------------------------------------
# The sharpest case. A failed probe is not "unprovisioned": treating it as one
# writes a plaintext credential onto a live installation, which is the single
# outcome this whole block exists to prevent.
status=0
run fail "s3cret-passw0rd" || status=$?
verdict nonzero "$([ "$status" -ne 0 ] && echo nonzero || echo zero)" \
    "a probe that cannot answer refuses to start"
verdict absent "$([ -e "$pwfile" ] && echo present || echo absent)" \
    "and writes nothing — a failed probe is not an unprovisioned installation"
verdict yes "$(grep -q "could not determine" <<<"$out" && echo yes || echo no)" \
    "naming what could not be determined rather than failing opaquely"

if [ "$failures" -ne 0 ]; then
    echo "FAIL: $failures entrypoint expectation(s) not met" >&2
    exit 1
fi
echo "OK: the bootstrap credential reaches disk only on an unprovisioned installation"
