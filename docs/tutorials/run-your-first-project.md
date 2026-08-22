# Run your first project

## In short

A deal ends the day it is won. The work it was sold for does not: the rollout
runs for months, a second deal lands on the same work a year later, and the
email about it never stops. A **project** is where Margince keeps that body of
work — from the first conversation, through the deal, through delivery, to the
day you close it.

In Margince you can:

- **Start a project while the deal is still being pursued**, so the early
  conversations are already filed where the delivery team will look for them.
- **Give it a key** such as `NW-ERP` and have every email whose subject carries
  `[NW-ERP]` filed under it automatically.
- **Win the deal and watch the project move into delivery** by itself.
- **Write email from inside the project**, with the AI reading only what
  belongs to it.
- **Close it with a reason, and reopen it** when the scope grows.

**This page walks one project from start to finish.** For who may do what,
the key rules and the vocabulary, see
[how-to/set-up-projects.md](../how-to/set-up-projects.md). For the day-to-day
lookup — including how email finds its project and what that means for
retention — see [how-to/run-a-project.md](../how-to/run-a-project.md).

No code, no API. You need a sign-in, a company in Margince (the walkthrough
uses a fictional customer, *Nordwind Logistik*, recorded as a company), and
one contact at that company.

## 1. Why a project exists at all

Open **Pipeline** and look at any deal. It has a value, a stage, a close date.
When it is won it becomes a line in a report and stops moving.

Now think about what an ERP rollout at Nordwind actually is: a scoping
workshop before the proposal, the proposal, the contract, six months of
delivery, a go-live, hypercare, and — if it goes well — a phase-2 deal for the
warehouse module. One deal cannot hold that. Two deals cannot hold it either,
because the conversation in between belongs to neither.

A project holds it. It is started on a company, it carries several deals over
time, and everything filed under it — mail, notes, tasks, contracts — stays
together on one page.

Open **Projects** in the left navigation. On a fresh installation you see the
empty state:

> **No projects yet**
> A project is the body of work a deal is about. It starts during the deal,
> in the initiative phase, and outlives close-won: once the deal is won,
> delivery is tracked here.
> Give a project a key and any email whose subject carries [KEY] is filed
> under it automatically.

That paragraph is the whole model. The rest of this page is it in practice.

## 2. Create the project during the deal

You do not need to win anything first. The moment Nordwind says "send us a
proposal", the project exists.

1. Open **Pipeline** and press **New deal**.
2. Fill in **Deal name** (`Nordwind ERP licences and rollout`) and **Value**
   (`180000`).
3. Pick the **Company** — *Nordwind Logistik*. The **Project** field below it
   is disabled until a company is chosen, because a project is started on a
   company.
4. Open **Project** and choose **New project…**. Two more fields appear:
   **Project name** and **Key**.
5. Enter the project name — `Nordwind ERP rollout`. Give it a different name
   from the deal: later, when you search for the project, a deal with the same
   name sits next to it in the results and the two are hard to tell apart.
6. Press **Create**.

You land on the deal page. Beside the company name you should see a chip
reading **Nordwind ERP rollout** — the project this deal belongs to. Click it.

The project page opens. Under the name you should see the company, the owner
(**Unassigned** unless you set one), and the phase: **Initiative**. On the
right, the **Phase history** reads *Started in Initiative* with today's date
and your name.

Created at deal creation like this, the project is in **Initiative** — an
idea, not yet a pursuit. That is deliberate: the project exists before you
know whether the deal will happen.

## 3. Give it a key

A key is the short handle you and the customer will write in email subjects.
Set it now, before the first mail goes out.

1. On the project page press **Edit project**.
2. In **Key**, type `NW-ERP`. The hint under the field says it plainly:
   *Optional short handle, e.g. ACME-CRM. Write [KEY] in an email subject and
   the mail is filed under this project.*
3. Press **Save**.

The key now shows next to the phase under the project name, and in the
**Projects** list under the project's name.

A key must start with a letter and be 2–24 characters of letters, digits,
`_` and `-`; it must be unique across the installation (the check is
case-insensitive, so `nw-erp` and `NW-ERP` are the same key). The form
refuses anything else before you can save.

You can also type the key when you create the project from the deal form —
the **Key** field is there too. This page sets it afterwards only to show
where it lives.

## 4. Work the deal phase

The proposal goes out; Nordwind is evaluating. Move the project from
**Initiative** to **Pursuing** so the page says what is true.

1. On the project page, in the **Phase** row, press **Pursuing**.
2. A dialog titled **Move to Pursuing** opens. It says: *The move is recorded
   in the phase history with the reason you give.* Type a reason —
   `Scoping workshop booked; proposal in progress.` — and press **Move**.

The phase under the name now reads **Pursuing**, and the **Phase history** on
the right shows *Initiative → Pursuing* with the date, your name and your
reason in quotation marks. The list of phases above it shows how long the
project spent in each one.

While the deal is pursued, log what happens on the **deal**: open the deal,
use **Log activity** (a note *Kickoff call with Nordwind IT*, for example) and
press **Log**. The note lands on the deal's timeline.

It does **not** appear on the project's timeline yet. A note on a deal is
filed under the deal; the project's **Timeline** shows only what is filed
under the project. To move it, press **Relink** on the note, search for
*Nordwind ERP rollout*, pick the **project** (not the deal of the same family)
and press **Relink**. Now open the project: the timeline shows the note, the
**Activities** counter reads 1, and the coverage line under the figures reads
*1 attributed · 0 awaiting a decision · …*. Email does this filing by itself
once the key is in the subject; see step 7.

## 5. Win the deal

Nordwind signs on a purchase order.

1. Open the deal and press **Won** in the **Stage** row.
2. A confirmation opens: **Move to Won?** Press **Confirm**.
3. If the deal has no signed contract attached, the dialog stays open and
   asks **How was it won?** Pick **On a purchase order** and press **Confirm**
   again. ([work-your-pipeline.md](../how-to/work-your-pipeline.md#close-a-deal)
   explains why winning asks this.)

The deal now reads **won**. Click the project chip.

The project's phase reads **Delivering**. You did not press anything on the
project: winning a deal moves its project into delivery inside the same
change, so no report ever sees a won deal on a project still being pursued.
The **Phase history** shows *Pursuing → Delivering* with your name, and the
figures at the top have moved — **Open deal value** is `€0.00`, **Won deal
value** is `€180,000.00`.

Two limits on this automatic move are worth knowing now:

- It fires only from **Initiative** or **Pursuing**. A project already in
  **Delivering** stays there — a second deal landing on running work is not a
  restart.
- It never reopens a **Closed** project. A renewal that closes years later
  does not silently resurrect an engagement somebody deliberately ended; the
  deal reads won, the project stays closed, and reopening is a decision a
  person makes with a reason (step 6).

## 6. Live in delivery, then close and reopen

Delivery runs for months. The project page is where it is tracked:

- **Deals** lists every deal on this project, won and open, with its value.
  **New deal** here starts another deal on the same project and company.
- **Open commitments** lists open tasks filed under the project, soonest due
  first, with an **overdue** badge where it applies.
- **Stakeholders**, **Contracts** and **Documents** on the right fill in as
  people are seated on the project, agreements name it, and files are attached
  to it.
- **Timeline** is the mail and activity filed under the project. The filter
  row above it (**Activity kind**, **Search this timeline**, **From**, **To**)
  narrows it; the **Activities / Changes / All** switch shows the mail, the
  record's own field and phase changes, or both.

When go-live is signed off:

1. Press **Closed** in the **Phase** row.
2. The dialog **Move to Closed** says: *Closing ends the project's delivery.
   It can be reopened later, and the reason stays on record.* The **Reason**
   field is required here — *A closed project needs a reason* — and
   **Close project** stays disabled until you type one. Enter
   `Go-live signed off by Nordwind on 22 Aug; hypercare handed to support.`
   and press **Close project**.

The phase reads **Closed**. The history records *Delivering → Closed* with
your reason.

Three months later Nordwind orders the warehouse module. Press **Delivering**
in the **Phase** row, give the reason (`Warehouse module phase 2 added to
scope.`) and press **Move**. The project is back in delivery; the history
keeps both the close and the reopen, so the gap reads as what it was.

Every phase move works like this, in either direction. The four phases are
fixed — **Initiative**, **Pursuing**, **Delivering**, **Closed** — but the
order is not enforced. Only closing demands a reason; the other moves record
one if you give it.

## 7. The AI inside a project

Open the company page (**Companies** → *Nordwind Logistik*) and press
**Write email**.

The composer opens with a **Draft to** picker, and beneath it a **Project**
picker. Open it: it offers **No project** and every live project on this
account, key first — `NW-ERP · Nordwind ERP rollout`. Choose it.

A line appears under the picker: **Scoped to NW-ERP**. That line means two
things at once:

- **What the AI reads.** Press **Draft with AI** after choosing a recipient.
  The draft is grounded only in what is filed under this project or under no
  project at all; mail filed under a *different* project on the same account
  is left out. The subject it proposes names the project, and the **Based
  on:** line under it lists the records it drew from.
- **Where the mail is filed.** When you send, the message is linked to the
  project, so it shows on the project's timeline and the next reply inherits
  that filing.

If the account has exactly one live project, the picker pre-selects it. With
two or more, it starts at **No project** and you choose.

You do not have to use the picker at all. Put `[NW-ERP]` in the subject of any
email — from Margince or from your mail client — and the message is filed
under the project when it is captured. Replies inherit the thread's project.
The rules in full, and what filing does to the retention of that email, are
in [how-to/run-a-project.md](../how-to/run-a-project.md#referencing-a-project-from-email).

> **Filing an email under a project is permanent for retention.** Under the
> German pack an email filed under a project counts as business
> correspondence and is kept for six years from the end of the calendar year it
> was sent or received. Moving it off the project
> later does not undo that. Relink deliberately.

## 8. Agents over MCP

Everything above is available to an agent connected over MCP, governed by the
same permissions the signed-in person holds:

- `read_project_360` reads the whole project page — phase history with time
  per phase, deals, stakeholders, contracts, documents, open commitments,
  timeline and the coverage figures.
- `catch_me_up_on` with a `project_id` answers "what has been going on?" for
  the project, reading only what is filed under it or under no project.
- `prepare_handoff` assembles what the delivery side needs from the sales
  side — owner, client contacts, what was sold, by when, what is promised —
  and names each gap the records leave.
- `advance_project_phase` moves a phase, with the same closing-needs-a-reason
  rule. An agent's move is staged and a person approves it before it runs.
- The generic record tools (`create_record`, `read_record`, `update_record`,
  `list_records`, `search_records`) accept `project` as a record type, and a
  deal can be listed by its `project_id`.

The tool catalog is [reference/mcp-info.md](../reference/mcp-info.md).
Connecting a client is [how-to/connect-an-mcp-client.md](../how-to/connect-an-mcp-client.md).

## Where to next

- [how-to/set-up-projects.md](../how-to/set-up-projects.md) — who can create,
  edit, archive and share; key conventions; when to create a project.
- [how-to/run-a-project.md](../how-to/run-a-project.md) — the lookup page for
  daily work, including every rule by which email finds its project.
