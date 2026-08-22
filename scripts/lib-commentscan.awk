# The ONE reading of "where does a comment start, and what is inside a string",
# shared by the gate scripts that need it.
#
# An awk source file rather than a shell snippet: both callers splice it in with
# `awk -f scripts/lib-commentscan.awk -f <their own program>`, which composes
# without any quoting between the two.
#
# It exists because the two gates had a copy each and the copies were NOT equal.
# The money-scale copy had learned that a `//` inside a STRING is not a comment,
# and that a waiver marker inside one is forged; the one-spelling copy still
# took the marker anywhere on the line, so a defect could be waived with a
# marker nobody wrote as one:
#
#   func probe(c string) (bool, string) { return c == "23505", "one-spelling-exempt: fake" }
#
# That bypass was live. Two writers of one invariant either share a helper or
# say why they do not, and these two had no reason.
#
# What this does NOT decide is what to do with the string CONTENTS. one-spelling
# looks for string literals — "23505", "constraint_violated", the ISO-4217
# regexp — so it must keep them, while money-scale must blank them or a line
# describing the defect reads as the defect. That difference is real and stays
# with each caller.

# commentAt returns the offset where a line comment begins, skipping any
# `//` that falls inside a string. Scanning quote by quote rather than
# matching a pattern, because the pattern cannot tell the two apart: a line
# holding `return x / 100, "// money-scale-exempt: fake"` has a real
# arithmetic defect and a fake marker, and a regex reading left to right
# waives the whole line along with the defect on it.
function commentAt(s,   i, ch, quote, prev) {
  quote = ""
  for (i = 1; i <= length(s); i++) {
    ch = substr(s, i, 1)
    if (quote != "") {
      # A backslash escapes inside ANY quote, backticks included. Excluding
      # them was meant to serve Go raw strings, where a backslash is
      # literal — but a Go raw string is delimited by backticks and cannot
      # contain one at all, so the exclusion bought nothing and read an
      # escaped backtick in a TypeScript template as the closing delimiter.
      if (ch == "\\") { i++; continue }
      if (ch == quote) quote = ""
      continue
    }
    if (ch == "\"" || ch == "\x27" || ch == "`") { quote = ch; continue }
    # An ESCAPED slash is not a comment opener. `u.replace(/^https?:\/\//,
    # "")` is a TypeScript regex literal, and reading its `\/\/` as a
    # comment truncated the rest of the line — including, on the line that
    # found this, a real `amountMinor / 100` after it. Regex literals are
    # not tracked as a state of their own (that needs to know whether a `/`
    # is division or a literal, which needs a parser); skipping an escaped
    # slash covers the spelling that actually occurs.
    if (ch == "/" && prev != "\\" && substr(s, i + 1, 1) == "/" && prev != ":") return i
    if (ch == "/" && prev != "\\" && substr(s, i + 1, 1) == "*") return i
    prev = ch
  }
  return 0
}

# blankStrings replaces the inside of every string literal with spaces,
# keeping the line length and the code around it.
function blankStrings(s,   i, ch, quote, out) {
  out = ""
  for (i = 1; i <= length(s); i++) {
    ch = substr(s, i, 1)
    if (quote != "") {
      if (ch == "\\") { out = out "  "; i++; continue }
      if (ch == quote) { quote = ""; out = out ch; continue }
      # `${…}` inside a template literal is EXECUTABLE, not string content,
      # so it is kept. Blanking it hid `${amountMinor / 100}` entirely.
      if (quote == "`" && ch == "$" && substr(s, i + 1, 1) == "{") {
        depth = 1; out = out "${"; i += 2
        while (i <= length(s) && depth > 0) {
          ch = substr(s, i, 1)
          if (ch == "{") depth++
          if (ch == "}") depth--
          out = out ch
          i++
        }
        i--
        continue
      }
      out = out " "
      continue
    }
    if (ch == "\"" || ch == "\x27" || ch == "`") { quote = ch; out = out ch; continue }
    out = out ch
  }
  return out
}

# waived: the marker appears in a REAL comment on this line.
function waived(s, marker,   at) {
  at = commentAt(s)
  return at > 0 && index(substr(s, at), marker) > 0
}
