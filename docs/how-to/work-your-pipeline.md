# Work your pipeline

This guide is for the person who sells — no code, no API. It covers where deals live, how to
move one through the pipeline, how to close it, and how to read the numbers. Two things you
cannot do from the UI today are covered honestly at the end, so you don't go looking for them.

The screen is called **Pipeline** (under **Work** in the left rail), and a deal is what it
holds: one opportunity with one company, a value, and a stage.

## The two views

Pipeline opens on the **Board** — one column per stage, deals as cards. The segmented control
in the toolbar switches to **Table**, the same deals as rows. Both read the same set, so a
filter you set in one is still set in the other.

### What a board column tells you

Each column header carries the stage name and its **win probability** (`50%`), and under it
two figures: the stage total and, beneath, the **weighted** total — the same money multiplied
by that probability. The count reads `12 deals`.

Two honest details worth knowing:

- **The count is the true count, not the cards you can see.** The board loads 100 deals at a
  time and the header totals come from the server over *every* matching deal, so a busy stage
  can legitimately say `40 deals` above fewer cards. **Load more** fetches the rest.
- **A column holding two currencies shows no total at all** — it says
  *"several currencies — no single total"*. Adding euros to dollars produces a number that
  isn't money, so Margince refuses rather than guessing a rate.

Cards carry the deal name, the company, the value, how long since anything happened, and
badges where they apply: **stalled**, **single-threaded**, **staged**, **archived**.

### What the table gives you instead

Columns: **Name**, **Stage**, **Value**, **Expected close**, **Last signal**, **Status**. You
can sort by Value, Expected close and Last signal; the others are fixed. **Last signal** reads
*"no signal yet"* for a deal nothing has ever touched.

The table is also where selection and saved views live — see below.

## Move a deal

**On the board:** drag the card to another column. Dropping on an ordinary stage writes the
move immediately and confirms with *"Moved to Discovery"*.

**On the deal's own page:** the stage stepper under the title is a row of buttons. The stage
the deal is in now is plain text — that's a fact, not a choice — and every other stage is a
button that moves it there.

Use the stepper on a phone or tablet: **the board's drag does not work on touch**, so the
deal's own page is the way to move it there.

**In bulk:** in the Table view, tick several deals and use **Move to stage** → **Move** in the
bar that appears. Only open stages are offered, and deals already in the target stage are
skipped rather than recording a move that didn't happen.

Whoever moves a deal, Margince records who did it and when. If two people move the same deal
at once, the second one is refused rather than silently overwriting the first.

## Close a deal

Moving to a **Won** or **Lost** stage never happens on the drop. A dialog asks first:

> **Move to Lost?**
> This closes the deal as lost. Confirm first — nothing happens until you do.

**Lost** requires a reason. The **Lost reason** box must say something before **Confirm**
lights up. If you cancel — or press Escape, or click outside — anything you typed is cleared,
so the next deal you close never inherits the last one's reason.

**Won asks what is behind the win.** Margince accepts a won deal two ways, and both are
legitimate — it just refuses to record a win that says nothing at all.

The first way is a **signed contract on the deal**. Press **Confirm** and, if one is there, the
deal closes immediately with no further questions.

The second way is **saying there is no contract**. When there is none, the dialog does not give
up — it stays open and asks:

> **How was it won?**
> This deal has no signed contract attached, so tell us how it was won. The answer is kept on
> the deal and counted in reports.

Pick one of five: **On a purchase order**, **Verbally, in person or by phone**, **Renewed by
email**, **Imported from another system**, or **Something else**. Choosing *Something else*
opens a **What was it?** box that must say something before **Confirm** lights up — the other
four explain themselves, that one does not.

You are only asked when there is nothing to point at, so a deal with paper behind it is still
one click. The answer is stored on the deal, which is what lets a report later count how many
won deals had no agreement and why.

Two things to know about the contract path, because you cannot complete it from the UI yet: the
contract form (Company → **Documents** → **Add a contract**) has no field naming a **deal**, and
a contract is created in **draft** with no control to move it out. Both links are missing, so in
practice today every win goes through the reason. Recording the agreement and attaching the
signed PDF still work and are still worth doing — they just do not yet count as the evidence.

**The reason is not shown back on the deal afterwards**
([#2105](https://github.com/gradionhq/margince-poc-v1/issues/2105)). It is recorded and reports
can read it; the record page does not display it yet.

**Closing is one deal at a time, on purpose.** The bulk bar offers open stages only: a lost
reason is specific to one deal, and one reason standing for a dozen would be a lie in the
record.

### Reopening

A closed deal's stepper is entirely inert — every stage greyed out. The way back is the
**Reopen** button in the header, which appears only on a won or lost deal. It asks which open
stage to return to, and clears the close date and the frozen exchange rate as it goes.

## Create and edit

**New deal** sits in the toolbar of both views. **Deal name**, **Currency** and **Stage** are
required; Value, Company and Expected close are optional. The stage list offers **open stages
only** — a deal cannot be born won or lost. The deal is created in whichever pipeline the
picker is currently showing; there is no pipeline field on the form.

**Edit deal** on the record page changes everything else: name, value, currency, owner,
company, partner, forecast category, expected close, and **Wait until**. Stage and status are
not here — those move through the stepper, the board, or Reopen.

**Forecast category** is your judgement about a deal, and it drives the Forecast report:

| Category | Use it when |
|---|---|
| **Commit** | you are standing behind this one for the period. |
| **Best case** | it could land, but you would not promise it. |
| **Pipeline** | it is real but early. |
| **Omitted** | deliberately out of the forecast. |

Two more appear in reports but are never yours to pick: **Slipped** (the server's own reading
of a Commit or Best case whose close date has passed or gone missing) and **No category yet**.

## Stalled deals, and how to silence one honestly

A deal is flagged **stalled** when it is open and **nothing has touched it for 60 days**.
"Touched" means real activity — a mail, a meeting, a note.

When a customer genuinely asked you to wait, set **Wait until** on the deal to the date you
expect to pick it back up. While that date is in the future the stalled flag is suppressed, and
it returns by itself afterwards. That is the honest way to quiet the flag: the record says why
it is quiet, and it un-quiets on its own.

**Home** shows every stalled open deal at the bottom, and the **Stalled only** filter on
Pipeline narrows either view to them.

## Narrow the list, and keep the narrowing

Filters, on both views: **Stage**, **Company**, **Stalled only**, **My deals**,
**Partner-sourced**, the **Pipeline** picker, and a **Show archived** toggle.

**There is no search box on this screen.** To find a deal by name, use the global
**Search everything…** at the top, or narrow with the filters.

Once a list is narrowed, **Save view** appears in the Table toolbar. Name it, and it becomes a
tab beside **Newest**, holding your sort, your filters, the archived toggle, the page size and
the pipeline you were looking at. Saved views are **private to you** — there is no sharing.

The button only shows once something is actually narrowed, so you cannot save "the default
list" as a view.

## Act on several deals at once

In the Table view, each open deal has a checkbox. Tick some, and a bar appears saying
`3 selected` with three verbs: **Assign** a new owner, **Move** to an open stage, and
**Archive**.

**Closed and archived deals have no checkbox.** Archiving something already closed is
meaningless, and moving a closed deal between open stages would be a silent reopen.

Each verb writes one deal at a time. If some rows fail — usually because someone else changed
them while you were choosing — the bar names them and keeps exactly those selected, so you can
retry them once the list refreshes.

**Archive asks first**, and tells you the truth: *"They leave every list and report, and there
is no way to bring one back from here yet."*

## Read the numbers

**Home** opens with **Open pipeline**: one line per currency, giving the raw total, the
weighted total, and the count of open deals. Won deals are not in it — that is revenue, not
pipeline. If your permissions hide some deals, the line says so rather than quietly
understating.

**Reports** (under Intelligence) holds three deal reports plus quotas:

- **Deals by stage** — every stage, unweighted next to weighted, split by currency.
- **Forecast** — tiles per category (Commit, Best case, Pipeline, Omitted, Slipped) for each
  currency.
- **Open deals per company** — where the open pipeline is concentrated.
- **Quotas** — human-set revenue targets with attainment computed from closed-won deals.
  Margince never guesses a target for you.

Every deal report has **Explain this number**. It opens the actual rows the headline is built
from, so a figure you distrust can be taken apart rather than argued about. Weighting is
rounded per deal and then summed, which is why the drill-down always reconciles exactly.

## Set up pipelines and stages (admin)

**Settings → Data model → Pipelines**. Each pipeline is a named ladder; one is the default.
A stage carries a **Name**, a **Position**, a **Semantic** (Open / Won / Lost) and a
**Win probability** — that percentage is what the board header prints and what every weighted
figure is computed from.

Two refusals worth knowing before you plan a change:

- **A stage still holding deals cannot be removed.** Move them first.
- **The Won and Lost pair cannot be removed at all.** Every pipeline needs somewhere to end.

Removing a stage keeps past stage changes readable; history is not rewritten.

If you can see this panel but not change it, it says so: *"Read-only view — you may not change
pipelines or their stages."*

## What you cannot do today

Stated plainly, so you don't hunt for them:

- **Two deals cannot be merged.** The Duplicates queue covers people, companies and leads only.
  If one opportunity was captured twice, keep the better record and archive the other — moving
  any offers or notes across by hand first, because archiving does not move them.
  ([#2033](https://github.com/gradionhq/margince-poc-v1/issues/2033))
- **An archived deal cannot be restored.** **Show archived** lets you *see* it, and it is
  read-only. Archiving is the one action here with no way back, which is why it asks first.
  ([#2034](https://github.com/gradionhq/margince-poc-v1/issues/2034))
- **The board cannot be dragged on touch devices.** Use the deal page's stage stepper.
- **There is no text search within the deals list.** Use global search.
- **A contract cannot be tied to a deal, or moved out of draft.** So a win always goes through
  the reason rather than the paperwork. See *Close a deal* above.
  ([#2088](https://github.com/gradionhq/margince-poc-v1/issues/2088))
- **A won deal does not show why it had no contract.** The answer is recorded, just not
  displayed. ([#2105](https://github.com/gradionhq/margince-poc-v1/issues/2105))

## Where deals meet the rest of Margince

- **Companies** — a deal belongs to one company, and that company's page shows its deals,
  its people, its contracts and its timeline. Contracts live under its **Documents** tab —
  though one recorded there cannot yet be tied to a specific deal (see *Close a deal*).
- **Leads** — qualifying a lead can open a deal in the same step, seating the contact on it.
  See [set-up-a-partner-program.md](set-up-a-partner-program.md) for the partner side of a
  deal, including what "Brought us this deal" versus "Helped on a deal we already had"
  means for commission.
- **Offers** — priced from the deal's own currency, which is why **New offer** is refused
  until the deal has one.
- **Agents** — an assistant can read deals and propose moves. A move to Won or Lost is staged
  for your confirmation rather than performed, and appears under
  **Awaiting your confirmation** on the deal.
