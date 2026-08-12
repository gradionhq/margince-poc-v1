#!/usr/bin/env bash
# check-host-ports.sh — fail if any host port published by the dev compose stack
# sits inside Linux's default ephemeral range (32768–60999).
#
# Why this is a gate and not a comment: a port in that range is not ours to bind.
# The kernel draws from it for the SOURCE port of every outbound connection
# (`sysctl net.ipv4.ip_local_port_range`), so any unrelated process on the host —
# a docker pull, `go mod download`, the CI runner's own agent — can transiently
# hold one as a client port. Compose then fails with
#   failed to bind host port for 0.0.0.0:<port>: address already in use
# and, being a race, it presents as a flake that names whichever step happened to
# call `make db-up` rather than the port that actually lost. That is expensive to
# read and cheap to prevent, and the only thing preventing it is the choice of
# number — which is exactly the kind of constraint a comment stops enforcing the
# first time somebody picks a memorable port.
#
# Checks the HOST side of `"<host>:<container>"` only. The container port is
# inside the container's own namespace and is unaffected.
set -euo pipefail

compose_file="${1:-infra/docker-compose.dev.yml}"

# The kernel's range, read from the host when we can so the gate tracks a tuned
# runner rather than a number baked in here. Falls back to the documented
# default on a machine without the sysctl (macOS dev laptops), which is the
# conservative direction: the default range is the widest one in play, so a port
# that clears it clears a narrowed range too.
lo=32768
if range="$(sysctl -n net.ipv4.ip_local_port_range 2>/dev/null)"; then
  read -r lo _ <<<"$range"
fi

if [ ! -f "$compose_file" ]; then
  echo "check-host-ports: $compose_file not found" >&2
  exit 1
fi

fail=0
while IFS= read -r line; do
  case "$(echo "$line" | sed 's/^[[:space:]]*//')" in
    \#*) continue ;;  # commented out — not published
  esac
  # `- "15432:5432"` → 15432. Anything without a host:container pair (a bare
  # container port, a range, an ip-qualified binding) is left to a human;
  # printing it is cheaper than guessing at it.
  hostport="$(echo "$line" | tr -d ' "'"'" | sed 's/^-//' | cut -d: -f1)"
  case "$hostport" in
    ''|*[!0-9]*)
      echo "check-host-ports: unparsed port mapping, review by hand: $line" >&2
      continue
      ;;
  esac
  if [ "$hostport" -ge "$lo" ]; then
    echo "EPHEMERAL HOST PORT: $hostport is at or above the ephemeral floor ($lo) — pick one below it: $line" >&2
    fail=1
  fi
# The published-ports list only: the `ports:` key, then its `- "..."` items,
# ending at the next key at the same or shallower indentation. Grepping for
# `\d+:\d+` across the whole file would also catch healthcheck intervals and
# image digests.
done < <(awk '
  /^[[:space:]]*ports:[[:space:]]*$/ { inports = 1; indent = match($0, /[^ ]/); next }
  inports {
    if ($0 ~ /^[[:space:]]*$/) next
    here = match($0, /[^ ]/)
    if (here <= indent) { inports = 0; next }
    if ($0 ~ /^[[:space:]]*-/) print
  }
' "$compose_file")

if [ "$fail" -eq 0 ]; then
  echo "host ports OK (all below the ephemeral floor $lo)"
fi
exit "$fail"
