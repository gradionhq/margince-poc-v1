# `extensions/notes` — scope

The demo unit for PR1. Its job is to make every capability the tier gains **visible and clickable**,
so PR1's acceptance is a human driving the SPA, not a green test suite.

> **The screen below is not part of this PR.** `extensions/<name>/frontend/` is still refused at
> generation and no unit ships a screen (DESIGN.md §4.5), so `#/ext/notes` serves the generic
> contract-derived descriptor card. Everything else in this document — the tables, the six operations,
> the secrets, the job and the tools — is what the branch ships. Read the screen sketch and the
> click-through as the acceptance the frontend slice's own PR carries.

**`fixtures/extensions/crm-hello` is not touched.** It stays the minimal CI fixture — smallest unit
that exercises scan → compose → boot, copied under `extensions/` by the CI lane
(`fixtures/extensions/crm-hello/crmhello.go:5`). Growing it would cost the tier its "smallest path"
probe. `notes` is a *first-party enabled unit* alongside `de` and `yogi`.

---

## 1. The one-screen premise

Everything lands on one screen at `#/ext/notes` — **Demo Notepad**. Deliberately mundane: the point
is the tier, and a domain-shaped demo would invite arguing about the domain.

```
┌─ Demo Notepad ──────────────────────────────────────────────┐
│                                                             │
│  Connection            ● connected                          │
│  Signing key      (stored — never displayed)  [ Replace ][×]│  ← secrets
│  [ sign this payload…            ] [ Sign ]                 │
│  → hmac-sha256  4f1c9a…e207                                 │  ← proves USE, not export
│                                                             │
│  ── Notes ───────────────────────────────────────────────   │
│  [ type a note…                              ] [ Add ]      │  ← api + migrations
│                                                             │
│  • 09:14  hello from the demo extension          [ × ]      │
│  • 09:10  ⟳ heartbeat — tick #7                             │  ← jobs
│  • 09:05  ⟳ heartbeat — tick #6                             │
│                                                             │
│  Last tick 4m ago · next in ~1m                             │
└─────────────────────────────────────────────────────────────┘
```

## 2. Capability → what the user actually does

| Surface | Demo behavior | How a human verifies it |
|---|---|---|
| **`migrations/`** | owns `ext_notes_note` | add a note, restart the stack, it is still there |
| **`api/`** | six POSTs — `/v1/ext/notes/list`, `/notes/add`, `/notes/remove`, `/signing-key`, `/signing-key/status`, `/signature` — and its own RBAC object `ext_notes_note` | a read-only seat sees the list, **`Add` is not rendered** |
| **`secrets`** | store a signing key; HMAC-sign a payload with it | paste a key → "connected"; sign a string → signature returned. The key is **never** emitted, not even masked |
| **`Jobs`** | tick appends `heartbeat — tick #N` | leave the screen open; a row appears with no user action |
| **`Tools`** | `list_notes`, served, auto-execute + read | ask the agent "what's in my demo notepad" |

Five surfaces, nothing that needs explaining to whoever is watching.

Every operation is a POST, and there is no `/notes` base path: a served extension operation IS a
governed tool invocation and its arguments are the request body, so the method validator admits only
POST/PUT/PATCH (see `extensions/notes/api/crm.yaml`). "list", "add" and "remove" are three verbs
on three paths, not three methods on one — an operator following a `GET /v1/ext/notes` gets
a 404.

## 3. What each surface must prove, precisely

**`migrations/`** — the table is `ext_notes_note`, not `note`. Namespacing is the claim, so the demo
should include a **negative test**: a migration attempting `notes_note` (unprefixed) or a table name
long enough to blow the 63-byte derived-identifier budget must **fail generation with its position**,
per the obligation deferred from `backend/pkg/extension/extension.go:40`. Rows are workspace-scoped
under RLS like core tables.

**`api/`** — the RBAC object is what proves the whole contract chain actually ran. `useCan` types on
`RbacObject` from `components["schemas"]["RbacObject"]` (`frontend/src/app/capability.ts:23`), and under
the two-lane type story (DESIGN §4.5) the demo screen compiles against the **composed** types under
`build/composition/frontend/`. **If the overlay did not merge, the object is not in the union and the
demo screen does not typecheck** — the ordering constraint enforcing itself, worth keeping rather than
working around.

> **⚠ SUPERSEDED by Task 11 — this self-enforcing guard NO LONGER EXISTS. Read this before writing the
> UAT script.**
>
> The claim above assumed an extension's RBAC object would reach the generated `RbacObject` union. It
> cannot, and the reason is deliberate: `$.components.schemas.RbacObject` is a **core** node, and Task 9's
> fragment-ownership rule lets a unit extend only nodes it created itself. Additive-only ownership is what
> makes an installation's contract reproducible; an enum-append action would spend it. (Task 10 §8.1
> established this; Task 11 acted on it.)
>
> So `capability.ts` widens instead: `RbacObject = CoreRbacObject | ` + "`ext_${string}`" + `. The direct
> consequence, stated plainly because it is a **deleted acceptance property**, not a footnote:
>
> **`useCan("ext_notes_note", "read")` now compiles whether or not the overlay merged.** A missing
> merge, a misspelled `x-rbac-object`, a fragment that never loaded — none of them are type errors any
> more. The demo screen will build and render, and the gate will simply deny, which looks exactly like
> "you don't hold that grant".
>
> Nothing was lost on the CORE side: a misspelled core object is still a type error, because a string
> that does not begin with `ext_` must be a member of the enum.
>
> **What the UAT must do instead** — the ordering constraint has to be checked at run time, since it is no
> longer checked at compile time. Two substitutes, both cheap, and the demo should carry both:
>
> 1. **The merged contract, asserted directly.** `make gen` then grep `build/composition/api/crm.yaml`
>    for the unit's path and `build/composition/frontend/extensions.gen.ts` for
>    `rbacObject: "ext_notes_note"`. If the overlay did not merge, both are absent. This is the
>    honest replacement for "it does not typecheck", and it is *stronger*: it names which artifact is
>    missing rather than failing at a call site.
> 2. **The `/me` snapshot, asserted on screen.** The object must appear in `authorization.objects` for
>    the demo principal — Task 10's `TestExtensionRbacObjectReachesTheMeSnapshot` proves the server half.
>    A screen whose gate denies while the object is *absent* from `/me` is the "the overlay did not
>    merge" failure; a screen whose gate denies while the object is *present and false* is the ordinary
>    "no grant" state. The UAT should distinguish them, because the compile step no longer does.
>
> The composed lane still enforces the **route** half of the chain at compile time: `src/api/client.ts`
> is parameterised by the merged contract's `paths` under `tsconfig.composed.json`, so a call to a route
> the overlay did not merge IS still a type error (Task 11 §F3 has the two-lane probe). It is only the
> RBAC *object* that lost its compile-time guard.

It must also prove the *server* half, which is a separate seam: the object has to reach `coreObjects`
through the published vocabulary seam and appear in the `/me` grants snapshot. A screen that typechecks
but gates on an object the client never learns the user holds renders nothing, and would look like a
frontend bug.

**`secrets`** — the demo proves the capability by **use, not disclosure**. The key is sealed via the port;
no endpoint returns it or any part of it, masked or otherwise. Signing a payload is what demonstrates the
unit can wield a credential it never emits — the same shape a real connector needs (an HMAC webhook
signature, a request signature), so the demo exercises the actual production pattern rather than a
display affordance nothing would ship. Store and use both land in `system_log`; that audit trail is part
of the deliverable. Demo the *namespace* wall too — a second unit must not read `notes`'s key.

**`Jobs`** — the tick is the only thing on the screen that happens without a user. It writes one row and
returns. The demo should also carry a **deliberately failing tick** (behind a toggle) so the operator
can see what a panicking or slow extension job does to the worker — bounded and logged, or the design
is wrong.

**`frontend/`** — `App.tsx:103` is a hardcoded `switch (screen)` over statically imported screens. The
slice adds a **fall-through**: unmatched screen → look up the composed extension registry from
`extensions.gen.ts`. Two things to pin while doing it:
- Vite must resolve `extensions/<name>/frontend/**` from `build/composition/` — alias plus
  `server.fs.allow`. Easy to miss and fails only at dev-server time.
- The vanilla tree must still build with the registry **empty**, and `#/ext/notes` must 404 cleanly.

## 4. Acceptance — the click-through

This is the UAT attached to the PR, per `.tmp/TEMPLATE.md`:

1. `make composition && make run` with `notes` present → boot inventory lists it
2. Navigate `#/ext/notes` → screen renders, "not connected"
3. Paste a signing key → "connected"; sign a payload → signature returned, key never displayed
4. Add a note → appears; reload → still there
5. Wait one tick interval → heartbeat row appears unprompted
6. Ask the agent to list notes → returns them
7. Switch to a read-only seat → list visible, `Add` gone
8. `rm -rf extensions/notes && make composition` → **byte-identical to the committed vanilla stub**,
   `#/ext/notes` 404s, `make check-composition` green

**Step 8 is the important one.** It is the tier's core guarantee — an empty tree reproduces vanilla
byte-for-byte — and it is the one most likely to be broken by making `api/crm.yaml` and
`extensions.gen.ts` real instead of placeholders.

## 5. Explicit non-goals

- No core-object writes. The demo touches only `ext_notes_*`.
- No outbound network. `ScopeSend` is refused for served tools anyway
  (`backend/internal/compose/extensiontools.go`), and the demo must not argue with that.
- No second demo unit in PR1 — *except* one throwaway fixture proving the secrets namespace wall (§3).
- Nothing Zalo. PR2 owns that.

## 6. Open, pending review

1. **Tick interval** — short enough to demo (60s?), long enough not to be noise in logs.
2. **Does the demo's RBAC object need seeding into a default role**, or does an admin grant it by hand?
   Affects step 7 of the click-through.
3. **The heartbeat tick is a fan-out** (DESIGN §4.4): the dispatcher kind enqueues one workspace-child
   per live workspace. In a single-workspace dev install that is one child, so the demo should make the
   fan-out *visible* — the heartbeat row naming its workspace — or it silently demonstrates the
   single-tenant case and the multi-tenant guarantee goes untested.
4. ~~Is `notes` shipped enabled in the vanilla tree?~~ **Decided 2026-08-08: yes, shipped enabled**,
   alongside `de` and `yogi`. A demo that is not exercised is not a demo.

   One consequence to handle rather than discover: `notes` is now part of the **vanilla composed
   set**, so `make check-composition`'s byte-identity gate — which proves an *empty* `extensions/` tree
   reproduces the committed stub — must keep being exercised against a genuinely empty tree, not
   against "the tree minus notes". Step 8 of §4 stays exactly as written.
