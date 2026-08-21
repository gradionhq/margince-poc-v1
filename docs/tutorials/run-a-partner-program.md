# Run a partner program

Some of your deals arrive because somebody else brought them. This tutorial follows one such
deal from the introduction to the money owed, so that by the end you can answer the question a
partner will eventually ask you: *what have we earned?*

You will make a company a partner, attribute a deal to them, win it, and watch the commission
appear on its own. It takes about fifteen minutes and touches four screens.

This is a walkthrough, not a reference. Every field on the partner form, with its full
vocabulary, is in [how-to/set-up-a-partner-program.md](../how-to/set-up-a-partner-program.md) —
read that when you want the complete list; read this to learn the shape of the thing.

**Before you start** you need a login and at least two companies in Margince: one that will be
the partner, and one that will be the customer. That distinction is the whole idea, so it is
worth being deliberate about it now.

## The one idea worth getting straight

A partner is **not** a customer who happens to be called a partner. In an ordinary deal there
is one company: the one buying. In a partner-sourced deal there are two:

- the **customer**, who is buying — the company the deal belongs to, and
- the **partner**, who brought it — a different company, who earns a share.

If both are the same company, something is wrong. That is a company buying for itself, not a
partner bringing you business, and no commission is owed to anybody.

Margince keeps them apart everywhere: a company's **Deals** tab lists deals where it is the
*customer*, and its **Partner** tab lists deals it *brought*. Two different lists, two
different questions, and a partner page where both are populated is a partner who both buys
from you and sells for you.

## 1. Make a company a partner

Open the company that will be your partner — **Companies**, then pick it — and go to its
**Partner** tab. A company that isn't a partner yet says **"Not a partner yet"** and offers
**"Make this a partner"**.

Fill in two things and leave the rest for now:

- **Partner role** — the only required field. *Hosting* if they run the software for their
  clients, *Consulting* if they advise clients and bring you in, *Strategic* for a wider
  alliance.
- **Margin tier** — the share they earn on deals they bring:

  | Tier | Choose it when |
  |---|---|
  | **Intro (15%)** | they make the introduction and hand it over. |
  | **Active Collab (20%)** | they work the opportunity alongside you. |
  | **Partner closed (25%)** | they run the sale and close it themselves. |

Save. The tab switches from the setup form to the partner record, and the company now appears
under **Partners** in the Companies list header.

**Leave the margin tier unset and no commission is ever calculated for them.** That is
deliberate — a tier is a commercial agreement, and Margince will not invent one — but it means
a partner you forgot to tier earns nothing while looking perfectly set up. If you take one
thing from this tutorial, take that.

## 2. Give them a deal

Go to **Deals** and create one, or open a deal that already exists and edit it. Two fields
matter here, and they only appear once at least one company is a partner:

- **via Partner** — the partner who brought it. The list offers partners only, so you cannot
  accidentally name an ordinary customer.
- **What the partner did** — appears once you have picked a partner:
  - **Brought us this deal (earns commission)** — they sourced it. This is what pays.
  - **Helped on a deal we already had (no commission)** — they influenced it. Recorded, not
    paid.

Leave the second field alone and Margince treats the deal as *brought* — the common case, and
the field says so.

Set the **Company** to your *customer* and **via Partner** to your *partner*. Two different
companies. Give it an amount and save.

The deal now reads, under its name: **€10,000.00 · Northgate GmbH · via VietnamPartner JSC** —
the value, the customer, and who brought it. The deals list can show the partner as a column
too: open the column picker and add **via Partner**. (That column cannot be sorted yet; the
API sorts by a fixed set of five fields that does not include it.)

## 3. Win it

Move the deal to **Won** the way you would any other — on the board, drag its card into the Won
column and confirm.

If the deal has no signed contract attached, Margince asks **"How was it won?"** before
accepting it — *Verbally, in person or by phone*, *On a purchase order*, and so on. That answer
is kept on the deal and counted in reports; it is not a commission question, and any of the
options lets the win through.

Nothing else is required of you. Behind the scenes the win is what triggers commission: a deal
that was *brought* by a partner who *has* a margin tier produces a commission entry
automatically, priced at that partner's tier against the deal's value at the moment it was won.
It appears within a second or two — the calculation happens just after the win is recorded, not
inside it, so give the page a refresh if the ledger looks empty.

## 4. See what they earned

Go back to the partner's company page and open its **Partner** tab. Two panels sit under the
partner record:

**Deals they brought** lists every deal attributed to them — open ones as well as won, sourced
as well as influenced — with the customer each was brought for. This is their pipeline with
you, and it is the only place those deals appear on this company's page: they belong to the
customers, so the company's own **Deals** tab does not show them.

**Commission** is the ledger: one row per entry, naming the deal it was earned on, the amount,
the rate, and the deal value it was calculated from. A €10,000 deal for a partner on *Partner
closed (25%)* shows **€2,500.00** — accrued.

Both the deal name and the customer are links, so a figure can always be traced back to the
work that produced it.

## What the statuses mean

A commission entry moves through four states:

| Status | Meaning |
|---|---|
| **Accrued** | earned, not yet agreed. This is where every entry starts. |
| **Approved** | somebody signed it off. |
| **Paid** | the money went out. |
| **Reversed** | the entry was cancelled — see below. |

**Today the app shows these but cannot change them.** Approving and paying exist in the API and
are not yet wired to buttons, so a ledger read from the Partner tab is a record of what is
owed, not a place to settle it. Until that ships, treat the panel as the source of truth for
*what was earned* and handle payment in whatever you use for payments.

## When a won deal is reopened

Reopen a won deal and its commission is **not deleted**. Margince adds a reversal row and marks
the original as *Reversed*, leaving both visible. Win the deal again and a fresh entry accrues.

That is the point of a ledger: it records what happened rather than what is currently true, so
a partner asking "what happened to that one?" can be shown both halves. You will see three rows
for a deal that was won, reopened, and won again — and that is correct, not a duplicate.

The rate is frozen when an entry accrues. Re-tiering a partner changes what their *future*
deals earn and never rewrites what a past one already did.

## What is not here yet

Worth knowing before you promise anything to a partner:

- **No approve or pay buttons** — as above.
- **No partner-facing view.** Partners cannot log in and see their own pipeline; everything
  here is internal.
- **Nothing enforces that a partner is a partner** on the API. The web form only offers real
  partners, but a deal created through the API can name any company, and if that company has no
  partner row it will never earn anything.
- **Assistants can read a deal's partner** and what they did, but cannot yet read or change the
  partner record itself — the tier, the certification, the stage.

## Where next

- Every field, with its full vocabulary and the ten relationship stages:
  [how-to/set-up-a-partner-program.md](../how-to/set-up-a-partner-program.md).
- Running deals in general, of which this is one flavour:
  [how-to/work-your-pipeline.md](../how-to/work-your-pipeline.md).
