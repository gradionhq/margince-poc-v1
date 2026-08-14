# CLAUDE.md — working in `frontend/`

Scoped to this directory. The root [CLAUDE.md](../CLAUDE.md) still governs
everything else: the branch/PR loop, the license header, the commit rules.
Two things are frontend-only and neither has a gate that will catch them for
you, so they are written down here.

`craft static` sweeps the Go trees only. No `*.test.tsx` in this repo is in
scope for the craftsmanship catalog today, T11 (*tests prove behaviour or they
are noise*, no real-clock flakiness) included. In this directory the rule holds
because the author holds it.

## A test may not depend on how busy the machine is

The frontend suite has produced two distinct flake families, and reading them
the wrong way cost the team several rounds of re-running CI. Both lessons below
are load-bearing.

### A wait that expires is usually waiting for something that never arrives

When a test times out under full-suite load and passes in isolation, the first
instinct is "the runner was slow, raise the budget". Measure before believing
it. The `company-context` family (#545, #652, #782, #981) was chased as a load
problem for a month; the file actually takes 271 ms in the full suite versus
192 ms in isolation, a factor of 1.4, nowhere near the factor of 40 that
exhausting a 10s waiter would need.

The real cause was a product bug. React Query re-arms `useMutation`'s options in
a **passive** effect, so between the commit that renders an enabled control and
that effect running, the observer still holds the *previous* render's closure.
A click landing in that window ran against stale state, the mutation refused it,
and the test then waited out its full budget for a render that was never coming.
Under load the window is simply wider.

So: **a wait that dies close to its full budget is a signal that the thing never
arrived, not that it was late.** Raising the timeout hides it. Capture the
assertion's error text and the rendered DOM on the failing run; that is what
separated a refusal from a slow render, and none of the first four reports had it.

The rule that came out of it, and the gate that holds it:

- **A `mutationFn` takes what it needs as a variable. It never closes over
  render state.** The click handler belongs to the committed render, so a
  variable it passes cannot be older than the control that carried it.
- A falsy guard at the top of a `mutationFn` (`if (!form) return`) is not a
  protection, it is the thing that *fires* when a stale closure happens, and
  what it does to a user is refuse a form they have filled in. Two of the six
  sites found this way were worse than a refusal: they would have submitted
  choices nobody made.
- `src/screens/mutation-variable-coverage.test.ts` walks the TSX with the
  TypeScript compiler API and fails on the pattern. Do not work around it.

### Drive the UI in a way that does not cost wall-clock time

`userEvent` advances on real timers. Its `delay` defaults to `0`, which is still
a number, so `wait()` schedules a real `setTimeout` and every simulated keystroke
and click yields a macrotask (`user-event` 14.6.3, `utils/misc/wait.js:9`). A
test's cost therefore scales with its interaction count, on a queue it shares
with every other jsdom suite, which is what pushes the interaction-heavy screen
suites past vitest's 5s default under contention (#1144, open).

The cost is per event, not per `setup()`, so constructing an instance per
interaction is not itself the expense. Do it once per test anyway: one instance
carries the shared input-device state, and a second one silently forgets which
keys and buttons the first left held.

When writing or touching a screen test:

- Call `userEvent.setup()` **once per test**, never per interaction.
- Wait on a condition, not on a duration. `findBy*` / `waitFor` over the settled
  state, not a `SETTLE_MS` ceiling that a slow scheduler can starve.
- No `setTimeout`, no sleep, no real clock. Inject fake timers, and drive
  interactions through `userEvent.setup({ advanceTimers })` when the component
  is timer-driven.
- If a test only passes because it got the machine to itself, it is not a test
  yet. Prove it: run the file alone and inside `make fe-unit`, and compare.
- Test files split at 1000 lines, the same ceiling the Go test trees hold.
  `onboarding-facts.test.tsx`, `company-act.test.tsx` and
  `onboarding-conversation.test.tsx` are already past it. Do not grow them.

## Storybook is documentation, and it goes stale silently

Stories live beside their component as `<name>.stories.tsx`. That co-location is
what `frontend/scripts/fe-uat.mjs` keys on, and `pnpm storybook` serves the
catalog on :6006 with a Theme control that flips `data-theme` exactly the way the
shell does.

What the gates actually cover, so you know what they do not:

- `make fe-bundle` builds the catalog in CI and **is** a required check, so a
  story that fails to compile or fails to register is caught deterministically.
- `make fe-uat` is the change-scoped render gate. It fails on a render error,
  on a changed component with **no** story, and on a changed story the build
  does not register. Two things weaken it: it is a coordinator lane and **not
  required**, and `ARGS="--allow-missing"` turns the missing-story failure off.
  Nothing, then, stops a component from shipping without a story.

That second gap is the rule you keep by hand. When a change adds or alters a
component in `src/design-system/` or a screen surface:

- Add or update its `.stories.tsx` in the same commit, covering the states the
  change introduces, not just the happy one.
- Check it in **both themes** before calling the surface done. Every derived
  value is a `color-mix()` of a canonical token and follows the dark accent lift,
  so a surface can be correct in light and wrong in dark.
- Run `make fe-uat` locally on a frontend change. Being unrequired is a fact
  about CI, not permission to skip it.
- `src/design-system/README.md` is the catalog and the prose that goes with it.
  A new control, a new variant, or a changed prop contract updates that file too.
  It is what the next person reads instead of hand-rolling a second dropdown.
