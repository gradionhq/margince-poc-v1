# Spec feedback — local session scratch

When you hit a spec defect while building — a contradiction, an omission, a
vocabulary gap, an unimplementable acceptance criterion — file it here so it can
be reconciled upstream against the spec (`../margince/specs/`). Contract-first
(principle P3): the spec is authoritative, so a spec defect is recorded, not
silently worked around in the source.

**This folder is git-ignored except for this README.** Findings are local
session scratch, never committed — drop a numbered Markdown file
(`NN-short-slug.md`) with: what the spec says, why it's wrong/ambiguous/
incomplete, the affected spec path(s), what this repo did in the meantime, and
the proposed spec change.

The durable record of a resolved defect is the spec's own amendment (an
ADR status line / a DECISIONS entry) — **not** a file here. Once a defect is
resolved in the spec and the build has re-pointed any interim workaround (so no
source comment cites the ticket), delete the note.

Keep the source clean: no ticket numbers or fix narration in code comments
([AGENTS.md](../AGENTS.md) rule 4) — a comment states the invariant so it stands
alone; the reconciliation trail lives here and in git history.
