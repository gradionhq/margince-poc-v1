# Partner programs

## In short

Some deals come to you because somebody else brought them — an agency that
recommended you, a consultancy that sells you into its clients, a hosting
company whose customers need your software.

A partner program is how you keep track of those people and pay them. In
Margince you can:

- **Mark a company as a partner** and record what kind of partner they are.
- **Agree a share** — 15%, 20% or 25% of the deals they bring you.
- **Tag a deal with the partner who brought it**, and say what they did:
  brought it to you, or helped with one you already had.
- **See what a partner is owed.** Win a deal they brought and Margince works
  out their share by itself and adds it to their ledger.
- **Answer "what have we earned?"** — the question every partner eventually
  asks — from one screen on their company page.

It is a prototype. Two things are missing, and both are covered at the end:
you cannot mark a commission as paid from the app, and partners cannot log in
to see their own numbers.

**This page explains how partner programs work and shows you one deal from
start to finish.** For the setup form field by field — every role, every
status, all ten relationship stages — see
[how-to/set-up-a-partner-program.md](../how-to/set-up-a-partner-program.md).

## The one thing to get right

An ordinary deal has one company in it: the one buying.

A partner deal has two:

- the **customer** — who is buying, and
- the **partner** — who brought it to you, and gets a share.

They are different companies. If you find yourself putting the same company in
both, stop: that is a company buying for itself, and nobody is owed anything.

Margince keeps the two apart on every screen. A company's **Deals** tab shows
what *it* is buying. Its **Partner** tab shows what it has *brought you*. Two
lists, two questions.

## 1. Make a company a partner

Open the company, go to its **Partner** tab, and choose **Make this a
partner**.

Two fields matter to begin with:

**Partner role** — what sort of partner they are. *Hosting* if they run the
software for their clients, *Consulting* if they advise clients and bring you
in, *Strategic* for anything broader.

**Margin tier** — their share of the deals they bring:

| | |
|---|---|
| **Intro (15%)** | they make the introduction and hand it over |
| **Active Collab (20%)** | they work the deal alongside you |
| **Partner closed (25%)** | they run the sale and close it themselves |

**If you leave the tier blank, they never earn anything.** The record looks
complete, deals get attributed to them, and no money is ever calculated.
Margince will not guess a rate you have not agreed — but nothing warns you
either, so set it when you set the role.

The rest of the form — certification, relationship stage, next step — is for
managing the relationship rather than the money.
[The how-to explains each one.](../how-to/set-up-a-partner-program.md)

## 2. Say who brought the deal

Open the deal and edit it, or fill these in as you create it. Two fields, and
they only appear if you have at least one partner:

**via Partner** — who brought it. Only actual partners are offered, so you
cannot pick an ordinary customer by mistake.

**What the partner did** — appears once you have chosen someone:

- **Brought us this deal (earns commission)** — they found it.
- **Helped on a deal we already had (no commission)** — they pitched in, and
  it is recorded, but there is no share in it for them.

Skip the second field and Margince assumes they brought it, which is the usual
case.

Set **Company** to the customer and **via Partner** to the partner. The deal
then reads:

> **€10,000.00 · Northgate GmbH · via VietnamPartner JSC**

The value, who is buying, and who brought them. You can add **via Partner** as
a column in the deals list too, from the column picker. It cannot be sorted
yet.

## 3. Win it

Move the deal to **Won** as you normally would — drag its card into the Won
column.

If there is no signed contract on the deal, Margince asks **"How was it
won?"** — verbally, on a purchase order, and so on. That is a record-keeping
question, not a commission one. Any answer lets the win through.

That is all you do. Winning the deal is what pays the partner.

## 4. See what they earned

Back on the partner's company page, the **Partner** tab now has two panels
under their record.

**Deals they brought** — everything attributed to them, won or still open,
with the customer each one was for. This is the only place these deals show up
on this company's page, because the deals themselves belong to the customers.

**Commission** — what they have earned. One row per deal, with the amount, the
rate, and the deal value it came from. A €10,000 deal at *Partner closed
(25%)* shows **€2,500.00**.

Both the deal and the customer are links, so any number can be traced back to
the work behind it. The commission appears a second or two after the win —
refresh if the panel still looks empty.

## Reading the ledger

Every entry has a status:

| | |
|---|---|
| **Accrued** | earned, not yet agreed — where every entry starts |
| **Approved** | signed off |
| **Paid** | the money has gone out |
| **Reversed** | cancelled, because the deal was reopened |

**Reopening a won deal does not delete the commission.** Margince adds a
reversal row and marks the original *Reversed*, leaving both on the page. Win
it again and a new entry appears. So a deal that was won, reopened and won
again shows three rows — that is right, not a mistake. The ledger is a history
of what happened, not a snapshot of what is true today.

The rate is fixed at the moment a deal is won. Move a partner to a different
tier and it changes what their *next* deals earn; it never rewrites what an
old one already paid.

## What this prototype cannot do yet

- **You cannot approve or pay from the app.** Those steps exist behind the
  scenes but have no buttons. Use the Commission panel as the record of what
  is owed, and pay it however you normally pay people.
- **Partners cannot see any of this.** There is no partner login. Everything
  here is yours internally.
- **The API is more permissive than the app.** Deals created through the API
  can name any company as the partner, even one that is not a partner — in
  which case nothing is ever earned. The web form only offers real partners.
- **Assistants can read a deal's partner** and what they did, but cannot yet
  read or change the partner record itself.

## Where next

- [Setting up partners, field by field](../how-to/set-up-a-partner-program.md)
- [Working deals in general](../how-to/work-your-pipeline.md)
