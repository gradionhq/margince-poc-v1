# Set up projects

This guide is for the person who decides how projects are used in the team — no
code, no API. It says who can do what with a project, how to choose keys, when
to create a project, how visibility and sharing work, and what the fixed
vocabularies are. The things you *cannot* do from the UI are listed at the end.

New to this? Start with
[tutorials/run-your-first-project.md](../tutorials/run-your-first-project.md) —
one ERP rollout followed from the first conversation to close. For the
day-to-day page (the project page section by section, and how email finds its
project) see [run-a-project.md](run-a-project.md).

## What a project is in Margince

A project is **the body of work a deal is about**. It is started on a company,
it carries several deals over time, and it outlives any one of them: it exists
before the first deal is won and keeps running through delivery after it.

It is not a folder you create at close-won. It is born in the **Initiative**
phase while the deal is still open, so the early conversations are already
filed where the delivery team will look. Everything filed under it — mail,
notes, tasks, contracts, documents, stakeholders — shows on one page, and the
page carries the project's whole phase history with the reason for every move.

A project has at most one company. A deal has at most one project, and the two
must name the same company — attaching a deal to a project on another company
is refused.

## Who can do what

Permissions come from the role's grant on the **project** object, plus the
role's row scope for writes. The seeded roles:

| Role | Create | Read | Edit, move phase, seat stakeholders | Archive | May edit |
|---|---|---|---|---|---|
| **Rep** | yes | yes | yes | no | own, team-owned and unowned projects |
| **Manager** | yes | yes | yes | yes | own, team-owned and unowned projects |
| **Management** | yes | yes | yes | yes | every project |
| **Admin** / **Ops** | yes | yes | yes | yes | every project |
| **Read only** | no | yes | no | no | nothing |

Two consequences:

- **Every seat that holds the read grant reads every project**, whatever its
  row scope. A project is a customer-identity record like a contact, company,
  lead or deal: the consultant delivering a project they neither own nor were
  granted still opens it. Row scope restricts **writes** only — a rep edits
  the projects they own, a teammate owns, or nobody owns; the **Owner** field
  is what that check reads. The reasoning is in
  [explanation/rbac-roles-and-teams.md](../explanation/rbac-roles-and-teams.md).
- A rep can create and run a project but cannot archive one. Archiving removes
  the project from the live list and frees its key, and it cannot be undone
  from the UI, so it stays with manager, management, admin and ops.

An admin assigns roles in **Settings → People & access**; the member's card
there shows, under **What this member sees**, what their role grants on
**Projects**. The grants themselves are seeded per role and are not editable
in the app.

## Key conventions

A key is the short handle written in email subjects — `[NW-ERP]` — and shown
next to the project's name everywhere. The rules the product enforces:

- Starts with a letter; then 1–23 more characters from letters, digits, `_`
  and `-` (2–24 characters in total).
- Unique across the installation, compared case-insensitively.
- Optional. A project with no key is still filed by the other rules in
  [run-a-project.md](run-a-project.md#referencing-a-project-from-email), but
  nobody can address it from a subject line.
- Archiving a project frees its key for reuse.

Conventions the product does not enforce but that make keys work in practice:

- **Customer first, then the work:** `NW-ERP`, `NW-WMS`, `BRANDT-FLEET`. A
  key is read by people who were not in the conversation.
- **Never a common word.** Keys are matched only inside square brackets, so a
  key like `RE` or `STATUS` cannot swallow ordinary mail — but it will still
  confuse a reader. Keep them distinctive.
- **Decide the key before the first email goes out.** Mail captured before
  the key existed is not re-filed afterwards; it stays wherever the other
  rules put it until somebody relinks it.
- **Tell the customer.** The key only files mail when it is in the subject.
  One line in the kickoff mail — *please keep `[NW-ERP]` in the subject* —
  is what makes the rest automatic.

## When to create a project

**At deal creation (recommended).** The deal form's **Project** field offers
every live project on the deal's company and a **New project…** entry that
creates one on the spot, with a name and a key. The project starts in
**Initiative**; move it to **Pursuing** once the deal is genuinely in play.
The benefit is that proposal-stage mail is already filed under the project on
the day delivery starts.

**At close-won.** When a deal is won and names no project, and its company has
exactly one live project, the deal page shows a **Start delivery** callout
offering to attach the deal to that project and move the project into
delivery. Press **Start delivery** to accept; you land on the project page with
the phase already moved. This covers the deal that was opened before anyone
thought of a project.

**From the Projects list.** **Projects → New project** creates one directly,
with a company picker. Use this for work that is not (yet) tied to a deal.

Whichever way it is created, the moment a deal on the project is won the
project moves into **Delivering** by itself — but only from **Initiative** or
**Pursuing**. A project already delivering stays as it is, and a **Closed**
project is never reopened silently by a win; reopening is a human move with a
reason.

## Visibility and sharing

Every seat with the project read grant already sees every project, so a share
is not how somebody gets to *read* one. A share does two other things: it
lets somebody **edit** a project their row scope does not reach — a delivery
lead on another team who has to move the phase or seat stakeholders — and it
reaches a seat whose role carries no project grant at all. Open the project
and press **Share**. The sharing page grants **Read** or **Write** on exactly
this one record, to a person or a team, with an optional expiry and a reason.
A share is capped at your own access and never widens anything else about
that person's scope.

**Ownership** is set on the project form (**Owner**: *Me*, *Unassign*, or
keep the current owner). Reassigning one project at a time is an edit. Moving
*every* live project one person owns to another — a handover or a leaver —
exists only in the API today: `POST /projects/transfer-ownership`, which a
signed-in person calls (agents may not). Each moved project gets its own
audit entry with the owner before and after.

## The fixed vocabularies

**Phase** — where the project is. Four values, fixed; movement in either
direction is allowed; only closing needs a reason.

| Phase | You are here when… |
|---|---|
| **Initiative** | the work is an idea — a deal may exist, but nothing has been agreed to pursue it. The birth phase. |
| **Pursuing** | a deal for this work is genuinely in play: proposal out, evaluation running. |
| **Delivering** | a deal is won and the work is owed. Winning a deal puts the project here by itself. |
| **Closed** | the work has ended, with a reason on record. Can be reopened later. |

**Stakeholder role** — what a person is to this project. The project page
lists the seats; seating somebody is an API write today
(`PUT /projects/{id}/stakeholders`), not a form in the app, and no MCP tool
offers it.

| Role | You are here when… |
|---|---|
| **Sponsor** | they pay for it or own the outcome on the customer side. |
| **Project lead** | they run it on the customer side. |
| **Delivery lead** | they run it on your side. |
| **Subject-matter expert** | they are consulted on a part of it. |

Neither list has a settings screen. Both are enforced end to end — in the API
contract ([`backend/api/crm.yaml`](../../backend/api/crm.yaml), the `Project`
and `ProjectStakeholder` schemas) and as database constraints — so adding a
value is a small code change plus a migration, not a workaround.

## Agents and MCP

An agent connected over MCP works under the same grants as the person whose
passport it carries. It can read a project (`read_project_360`, `read_record`
with record type `project`), create or update one through the generic record
tools, and move a phase with `advance_project_phase` — the last is staged
for a person to approve before it runs. It can also archive one through
`archive_record` — confirmation-required, so a person approves that too. It
cannot transfer ownership; that endpoint is human-only.

## What you can't change from the UI

- **Legal hold.** A project can be placed under legal hold, which freezes the
  correspondence filed under it against erasure and retention sweeps. The hold
  is set by an operator directly in the database, the same as for a person,
  company or deal; there is no switch in the app.
- **Bulk owner transfer** — API only, as above.
- **Un-archiving.** Archiving is final from the UI.
- **The phase and stakeholder-role lists** — code and migration, as above.
- **Retention of filed mail.** Filing an email under a project marks it as
  business correspondence under the German pack — six years from the end of
  the calendar year it was sent or received — and
  moving it off the project later does not undo that. Nothing in the UI
  shortens it; see the callout in
  [run-a-project.md](run-a-project.md#referencing-a-project-from-email).
