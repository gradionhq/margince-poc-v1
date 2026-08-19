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

# run PROBE [--password VALUE] [--existing VALUE]
#
# PROBE is what the stubbed `margince-migrate org-exists` reports: "true",
# "false", or "fail" to exit non-zero without answering.
#
# The two values are FLAGS rather than positions so that "the operator set no
# variable" and "the operator set it to empty" stay distinguishable — omitting
# --password leaves MARGINCE_ADMIN_PASSWORD unset in the child, which is the
# state a fresh deployment that bootstraps by other means is actually in.
# Passing an empty string instead would test a different thing while claiming
# to test that one.
#
# Returns the entrypoint's exit status; leaves its output in $out and the
# credential file at $pwfile for the caller to assert on.
run() {
    local probe="$1"; shift
    local password="" have_password=0 existing="" have_existing=0
    while [ $# -gt 0 ]; do
        case "$1" in
            --password) password="$2"; have_password=1; shift 2 ;;
            --existing) existing="$2"; have_existing=1; shift 2 ;;
            *) echo "run: unknown argument $1" >&2; return 2 ;;
        esac
    done

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

    # An EMPTY pre-existing file is a real state — a spent credential truncated
    # rather than removed — so this is gated on the flag, not on the contents.
    if [ "$have_existing" -eq 1 ]; then
        printf '%s' "$existing" >"$work/app/secrets/admin-password"
    fi

    # The entrypoint writes to the absolute /app/secrets; run it under a
    # rewritten copy so the test needs no container and no root.
    sed "s#/app/secrets/admin-password#$work/app/secrets/admin-password#" "$entrypoint" >"$work/entrypoint.sh"
    chmod +x "$work/entrypoint.sh"

    local status=0
    if [ "$have_password" -eq 1 ]; then
        out="$(
            PATH="$work/bin:$PATH" \
            MARGINCE_OWNER_DSN="postgres://owner@localhost/x" \
            MARGINCE_DSN="postgres://app@localhost/x" \
            MARGINCE_ADMIN_PASSWORD="$password" \
            "$work/entrypoint.sh" 2>&1
        )" || status=$?
    else
        out="$(
            PATH="$work/bin:$PATH" \
            MARGINCE_OWNER_DSN="postgres://owner@localhost/x" \
            MARGINCE_DSN="postgres://app@localhost/x" \
            "$work/entrypoint.sh" 2>&1
        )" || status=$?
    fi
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

# served checks the half this file is not about but must not silently lose: the
# entrypoint's JOB is to start the api. A regression that exits before
# `exec margince-api` would satisfy every credential assertion below, because
# writing nothing is exactly what a process that never got there does.
served() { # status description
    verdict 0 "$1" "$2: the entrypoint ran to completion"
    verdict yes "$(grep -q "stub: api started" <<<"$out" && echo yes || echo no)" \
        "$2: and exec'd the api"
}

# --- an already-bootstrapped installation ------------------------------------
status=0
run true --password "s3cret-passw0rd" || status=$?
served "$status" "provisioned"
verdict absent "$([ -e "$pwfile" ] && echo present || echo absent)" \
    "a provisioned installation is never handed the supplied credential"
verdict yes "$(grep -q "already has an organization" <<<"$out" && echo yes || echo no)" \
    "and says so, because an ignored credential must not look like an applied one"

# A file an EARLIER boot wrote is spent the moment the organization exists. The
# invariant is that none is at rest, not merely that this start added none.
status=0
run true --existing "left-by-an-earlier-boot" || status=$?
served "$status" "provisioned with a stale file"
verdict absent "$([ -e "$pwfile" ] && echo present || echo absent)" \
    "a credential left by an earlier boot is retired once the organization exists"

# An empty spent file is the same defect wearing a different shape: something
# truncated it instead of removing it, and it is still a path the api reads.
status=0
run true --existing "" || status=$?
served "$status" "provisioned with an emptied file"
verdict absent "$([ -e "$pwfile" ] && echo present || echo absent)" \
    "an emptied credential file is retired too, not left behind because it holds nothing"

# --- a fresh installation ----------------------------------------------------
status=0
run false --password "s3cret-passw0rd" || status=$?
served "$status" "unprovisioned"
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

# No variable at all, not an empty one — a fresh installation may bootstrap by
# other means (the ADR-0105 claim flow), and that must still start.
status=0
run false || status=$?
served "$status" "unprovisioned with no variable"
verdict absent "$([ -e "$pwfile" ] && echo present || echo absent)" \
    "no variable, no file"

# --- the probe cannot answer -------------------------------------------------
# The sharpest case. A failed probe is not "unprovisioned": treating it as one
# writes a plaintext credential onto a live installation, which is the single
# outcome this whole block exists to prevent.
status=0
run fail --password "s3cret-passw0rd" || status=$?
verdict nonzero "$([ "$status" -ne 0 ] && echo nonzero || echo zero)" \
    "a probe that cannot answer refuses to start"
verdict no "$(grep -q "stub: api started" <<<"$out" && echo yes || echo no)" \
    "and the api is never reached"
verdict absent "$([ -e "$pwfile" ] && echo present || echo absent)" \
    "and writes nothing — a failed probe is not an unprovisioned installation"
verdict yes "$(grep -q "could not determine" <<<"$out" && echo yes || echo no)" \
    "naming what could not be determined rather than failing opaquely"

if [ "$failures" -ne 0 ]; then
    echo "FAIL: $failures entrypoint expectation(s) not met" >&2
    exit 1
fi
echo "OK: the bootstrap credential reaches disk only on an unprovisioned installation"
