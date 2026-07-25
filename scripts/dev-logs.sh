#!/usr/bin/env bash
# A readable live view of the dev stack's log. `make dev` tags every line with
# the process that wrote it (api / worker / fe / boot); this script colours those
# tags and the severity, and hides the job-queue heartbeat that otherwise buries
# the signal.
#
# It is a DEV convenience only. The servers' own output is unchanged — plain
# key=value text (or json via --log-format) with no ANSI in it — because that is
# what a production log collector ingests. Nothing here runs outside `make dev`.
#
#   make dev-logs                 # follow, colourised, heartbeat hidden
#   make dev-logs ROLE=worker     # one process only
#   make dev-logs LEVEL=warn      # warnings and errors only
#   make dev-logs ALL=1           # keep the heartbeat too
#   make dev-logs FOLLOW=0 N=200  # last 200 lines, then exit
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

log=".tmp/dev/${DEV_SLUG:-_base}/dev.log"
role="${ROLE:-}"
level="${LEVEL:-}"
all="${ALL:-0}"
follow="${FOLLOW:-1}"
lines="${N:-200}"

if [[ ! -f "$log" ]]; then
  echo "No dev log at ${log} — is the stack up? Start it with 'make dev'${DEV_SLUG:+ DEV_SLUG=$DEV_SLUG}." >&2
  exit 1
fi

case "$level" in
  ""|debug|info|warn|error) ;;
  *) echo "FAIL: LEVEL=$level — want debug, info, warn or error" >&2; exit 1 ;;
esac
case "$role" in
  ""|api|worker|fe|boot) ;;
  *) echo "FAIL: ROLE=$role — want api, worker, fe or boot" >&2; exit 1 ;;
esac

# Colour only when a terminal is reading. Piped into a file or a grep, the
# output stays plain — ANSI escapes in a pipeline are noise, not colour.
colour=1
[[ -t 1 ]] || colour=0

# The job-queue (River) heartbeat: fixed internal messages it emits on a timer
# whatever the app is doing. They are real log lines, not errors, but at
# MARGINCE_LOG_LEVEL=debug they repeat every few seconds and push the request
# and error lines off the screen. Hidden by default, restored with ALL=1 —
# never dropped silently, so a heartbeat that stops being routine can still be
# looked at.
heartbeat='producer: Producer job counts|producer: Distributed batch of jobs|jobcompleter\.|leadership\.Elector|Client: Job stats'

# shellcheck disable=SC2016 # the awk program is deliberately single-quoted: $0/$1 are awk fields, not shell
# The colour scheme and line shape live in scripts/lib/devlog.awk, shared with
# the writer in dev.sh so the two ends cannot drift apart.
awk_lib="scripts/lib/devlog.awk"

# LEVEL names a floor, so LEVEL=warn shows warnings AND errors: asking for
# warnings and being shown no errors would be actively misleading.
keep_for_level() {
  case "$1" in
    debug) echo "DEBUG INFO WARN ERROR" ;;
    info)  echo "INFO WARN ERROR" ;;
    warn)  echo "WARN ERROR" ;;
    error) echo "ERROR" ;;
    *)     echo "" ;;
  esac
}

run_awk() {
  # ALL=1 is expressed by handing the library an empty heartbeat pattern, so the
  # "hide it" decision lives in exactly one place.
  local hb="$heartbeat"
  [[ "$all" == "1" ]] && hb=""
  awk -v mode=view -v colour="$colour" -v role="$role" \
      -v heartbeat="$hb" -v want="$level" \
      -v keeplist="$(keep_for_level "$level")" \
      -f "$awk_lib" -
}

if [[ "$follow" == "1" ]]; then
  # -n seeds the view with recent history so the screen is not blank until the
  # next line arrives.
  tail -n "$lines" -f "$log" | run_awk
else
  tail -n "$lines" "$log" | run_awk
fi
