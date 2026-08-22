# The money-scale gate's own strip pass: comments removed and continuation lines
# joined, so a wrapped expression is judged whole.
#
# Loaded after scripts/lib-commentscan.awk, which supplies commentAt(),
# blankStrings() and waived() — the reading of "where does a comment start"
# that this gate shares with check-one-spelling.awk.

function opens(s,   n, t) { t = s; n = gsub(/[([]/, "", t); return n }
function closes(s,  n, t) { t = s; n = gsub(/[)\]]/, "", t); return n }

function flush() { if (buf != "") print FILENAME ":" start ":" buf; buf = ""; depth = 0; lines = 0 }
FNR == 1 { flush(); inblock = 0 }
{
  c = $0
  # The waiver counts only where a waiver can be WRITTEN — in a comment. A
  # line carrying the marker inside a string literal was skipping the whole
  # line, which let any code on it bypass the gate under a marker its author
  # never wrote as one.
  if (waived(c, waiver)) { flush(); next }
  if (inblock) { if (match(c, /\*\//)) { inblock = 0; c = substr(c, RSTART + RLENGTH) } else next }
  t = c; sub(/^[[:space:]]+/, "", t)
  if (t ~ /^(\/\/|\*)/) next
  while (match(c, /\/\*[^*]*\*+([^\/*][^*]*\*+)*\//)) { c = substr(c, 1, RSTART - 1) substr(c, RSTART + RLENGTH) }
  if (match(c, /\/\*/)) { inblock = 1; c = substr(c, 1, RSTART - 1) }
  # `x:=minor/100//note` is a comment too. Anchored on a `//` that is not
  # part of a scheme (`https://`), which is the only form that routinely
  # appears inside a string here.
  # The same scanner decides where a trailing comment starts, so a `//`
  # inside a string is not mistaken for one — and `https://` is not either.
  at = commentAt(c)
  if (at > 0) c = substr(c, 1, at - 1)
  # And the contents of a STRING are not code. A line mentioning the shape
  # in prose — "see amountMinor / 100 in the old code" — was reported as
  # the arithmetic it describes. The same quote scanner that finds the
  # comment blanks the literals, so the identifier and the power have to be
  # in the CODE to pair.
  c = blankStrings(c)
  if (t == "") { flush(); next }
  if (buf == "") start = FNR
  buf = buf " " c
  lines++
  depth += opens(c) - closes(c)
  # An expression may also be broken after a trailing operator with no
  # bracket open — `amountMinor :=` on one line and `major * 100` on the
  # next — so a line ENDING in one keeps the statement open. Without this
  # the gate flushed before the arithmetic arrived and saw two halves,
  # neither of them a finding.
  # Only an operator that CANNOT end a statement continues one. A trailing
  # comma or colon ends a perfectly good line in a struct literal, and
  # treating those as continuations joined unrelated members — a `valueMinor`
  # field and an `ageMs * 1000` two lines below became a finding. Braces are
  # left out of the depth above for the same reason: they open a BLOCK, and
  # counting them swallowed whole function bodies.
  trailing = c
  sub(/[[:space:]]+$/, "", trailing)
  if (trailing ~ /(=|\+|-|\*|\/|&&|\|\|)$/ && lines < 6) next
  # Bounded at SIX lines. Four was the first bound and it missed the shape
  # this gate exists for, one line longer: biome wraps
  # `const amountMinor = Math.round(Number(amount) * 100)` across five when
  # the expression is long enough, and the buffer flushed with the name in
  # one half and the power in the other. A `const (` block is thirty lines,
  # so six still refuses to join one — measured on compose/report.go, whose
  # block holds `amount_minor` and a `/ 100.0` thirty lines apart with
  # nothing to do with each other. A blank line ends a statement too.
  if (depth <= 0 || lines >= 6) flush()
}
END { flush() }
