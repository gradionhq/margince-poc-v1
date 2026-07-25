# The ONE colour scheme and line shape for the dev stack's log, shared by the
# writer (scripts/dev.sh tags each process's output as it arrives) and the reader
# (scripts/dev-logs.sh). It lives in one file because two copies of a colour
# table drift, and then the same line looks like two different things depending
# on how you happened to read it.
#
# Variables the caller sets with -v:
#   mode      "tag" (writing: stdin is ONE process's raw output, prefix it)
#             "view" (reading: stdin is the combined log, re-read its tag)
#   colour    1 to emit ANSI, 0 for plain text
#   role      tag mode only: which process this stream belongs to
#   heartbeat view mode only: regex of lines to drop (empty keeps everything)
#   want      view mode only: severity floor name, "" for no filter
#   keep      view mode only: set via keeplist, the levels `want` admits
#
# Dev-only by construction: nothing here runs outside `make dev` / `make
# dev-logs`, and the servers' own stdout is untouched plain key=value or json.

# keeplist arrives as a space-separated list of level names because awk -v cannot
# pass an array; `keep` is the set the severity filter actually tests against.
BEGIN {
  n = split(keeplist, names, " ")
  for (i = 1; i <= n; i++) { keep[names[i]] = 1 }
}

# Strips CSI sequences by their final letter, not just the SGR "m": Vite emits
# cursor moves and line clears (\033[K, \033[2A) too, and one of those surviving
# into a repainted line is the same garbled output the strip-then-repaint pass
# exists to prevent.
function stripAnsi(s) {
  gsub(/\033\[[0-9;]*[a-zA-Z]/, "", s)
  return s
}

function paint(s, c) {
  if (!colour || c == "") { return s }
  return "\033[" c "m" s "\033[0m"
}

# One colour per process, so the eye separates streams without reading the tag.
function tagColour(tag) {
  if (tag == "api")    { return "36" }  # cyan
  if (tag == "worker") { return "35" }  # magenta
  if (tag == "fe")     { return "34" }  # blue
  return "90"                           # dim — boot/build chatter
}

function levelOf(s) {
  if (match(s, /level=[A-Z]+/)) { return substr(s, RSTART + 6, RLENGTH - 6) }
  return ""
}

# Severity outranks the process: an error must look like one whoever said it.
function levelColour(lvl) {
  if (lvl == "ERROR") { return "1;31" }
  if (lvl == "WARN")  { return "33" }
  if (lvl == "DEBUG") { return "90" }
  return ""
}

{
  if (mode == "tag") {
    # The writing end. Vite emits its own ANSI; levelColour returns "" for a
    # line with no level=, so that output passes through with its colours intact.
    print paint(sprintf("%-6s", role), tagColour(role)) " | " paint($0, levelColour(levelOf($0)))
    fflush()
    next
  }

  # The reading end. The file may already carry colour (the writer adds it at
  # debug level), so everything is stripped and repainted rather than nested —
  # nested codes end each other early and render as garbage.
  bar = index($0, "|")
  if (bar == 0) { next }                       # not a tagged line
  tag = stripAnsi(substr($0, 1, bar - 1))
  sub(/ +$/, "", tag)
  rest = stripAnsi(substr($0, bar + 2))

  if (role != "" && tag != role) { next }
  if (heartbeat != "" && rest ~ heartbeat) { next }

  lvl = levelOf(rest)
  if (want != "" && !(lvl in keep)) { next }

  print paint(sprintf("%-6s", tag), tagColour(tag)) " " paint(rest, levelColour(lvl))
  fflush()
}
