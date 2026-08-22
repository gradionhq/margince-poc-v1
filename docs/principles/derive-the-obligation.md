# Derive the obligation

**Prefer a fitness function over a point fix, and derive its subject set from
the tree rather than listing it.** A rule maintained as a list is a rule that
silently stops covering things.

The binding form is
[*Rules learned from the review loop*](../../CLAUDE.md#rules-learned-from-the-review-loop-binding).
This page is the method for writing a gate that actually holds.

## The six rules, and what each one is defending against

1. **Fix the invariant, not the call site.** Grep every mutation and read site
   of the same column, constraint or record, and fix them as one change. The
   recurring reviewer catch here is "fixed the case under review, missed the
   sibling copy".
2. **Prefer fitness functions over point fixes.** Derive the obligation from the
   system — every tenant statement carries its workspace predicate; every CHECK
   violation maps to a 4xx; `backend/arch_test.go` derives its package lists
   from the tree. Do not maintain it as a list.
3. **Anything that returns a record is a read**, and carries the row-scope gate
   — including replay, conflict and error paths.
4. **No build-process residue in comments.** State the invariant so it stands
   alone. See [the record is the code](the-record-is-the-code.md).
5. **Never rationalize a known gap in a comment.** Restructure it away or gate
   it.
6. **A test that supplies its own version of production proves nothing about
   production.** Hand-inserted rows the real writer never writes, or a
   hand-copied adapter mirroring what compose wires. Seed through the real
   writer; reach for the real wiring. An unexpectedly uncovered new file usually
   means a test double stands where the real thing should.

## Writing a gate that holds

Every clause here exists because a gate in this tree failed it.

**Derive the subject set from the tree.** A hardcoded coverage map is a list to
maintain, and it will be short. `draftrulesparity_test.go` asserts four drafting
surfaces carry the shared rules while more than four exist — the gate is green
and the rule is not held.

**Put the escape hatch at the subject.** A `doc.go` line, a contract field, a
`//craft:ignore <check> <reason>` — where the author editing that code sees it.
A map inside the test file is invisible to exactly the person who needs it.
`agenttoolparity_test.go` is the closest thing to the house shape: it derives
its expectation from the contract rather than restating it, and its escape hatch
for ordinary tools lives in the contract. It is not a pure example, and the
impurity is instructive — a hand-maintained `composedIntents` map still sits in
the test file for the tools that compose several operations, which is precisely
the kind of list that goes quietly short. If you cannot get the hatch to the
subject, say in the test why not.

**Ratify the instance, never the category.** A waiver keyed by column, package
or rule name admits the second offender for free under the first one's reason.

**Write the defect test first.** Mutate the code to reintroduce the bug and
watch the gate go red. A gate that has never failed proves nothing — several
fitness functions in this tree passed against the exact bug they described.

**Prove the mutation was reached.** An inverse test can pass for the wrong
reason: the mutation lands in a function the path never calls, or an earlier
guard refuses the case before your assertion runs.

**Measure the before, not only the after.** A grep returning 0 after the fix
proves nothing unless you know what it returned before.

**Watch for the vacuous filter.** A subject filter that looks derived can be
inert — unbounded is not empty, and a predicate that drops nothing reads exactly
like a predicate that drops the right things. Measure what your gate *excludes*.

**Write the mirror gate.** A gate can read green over its own defect reversed.
If you assert A implies B, ask what happens when B appears without A.

## What this does not ask for

- **Not a gate for every rule.** Some obligations are genuinely judgement, and a
  gate that encodes a judgement badly is worse than the prose. If you cannot
  state the failure precisely, write the rule and say it is unguarded.
- **Not blocking on tooling.** A point fix that ships today plus an issue for
  the gate beats a gate that never lands. Say which you did.
- **Not deleting a rule because it has no gate.** An unguarded rule is weaker,
  not void.
