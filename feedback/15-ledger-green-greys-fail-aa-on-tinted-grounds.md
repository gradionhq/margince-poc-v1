# 15 — Ledger-Green text greys fail WCAG AA on tinted grounds

**Where:** `design/00-design-language.md §2` token table / ADR-0040 vs the
EP09.21 automated AA gate (and EP09's own note: "design tokens … must
satisfy contrast").

**What fails:** at the design language's compact sizes (11–13px meta/caption
text), `--textTertiary #9AA6A0` is ~2.6:1 on `bgPage` (needs 4.5:1), and
`--textSecondary #68756E` is ~4.36:1 on `bgCard #EEF1F0` — both AA failures
for exactly the roles §2 assigns them (meta, captions, secondary text on
inset surfaces). Found by the axe-core CI gate the moment it went live.

**What the build did in the meantime:** the canonical values stay pinned in
the token layer (tokens.test.ts still enforces the §2 canon); a derived
`--textMeta #5E6C65` (dark: `#8FA099`) carries all small-text roles and the
UI passes WCAG 2.2 AA on every core screen.

**Proposed spec change:** re-tune the §2 grey ramp so the named roles meet
AA at their designated sizes (or bless the derived meta shade into the
canon), and update `mockups/app.css :root` to match.
