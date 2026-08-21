# Set up and run a partner program

This guide is for the person who runs the partner program — no code, no API. It shows where
partners live in the app, how to make a company a partner, what every field on the form means,
and how to work the pipeline afterwards. The one thing you *cannot* do from the UI — changing
the value lists themselves — is covered at the end.

New to this? Start with
[tutorials/run-a-partner-program.md](../tutorials/run-a-partner-program.md) — what partner
programs are, what you can do with them, and one deal followed from the introduction to the
money it earns. This guide is the reference you come back to for what a particular field means.

## What a partner is in Margince

A partner is not a separate record you create next to a company. It is an **extra layer on a
company that already exists**: the company keeps its name, domain, people, and timeline, and
gains partner state on top — a role, a certification status, a margin tier, and a relationship
stage. Nothing is duplicated, so the same company page shows both its commercial life and its
partner life.

That also means the first step of any partner setup is simply having the company in Margince.
If it isn't there yet, add it (or let capture create it from mail traffic) before you continue.

## Make a company a partner

1. Open the company's page (**Companies** → pick the company).
2. Open its **Partner** tab. A company that isn't a partner yet shows **"Not a partner yet"**
   and, below it, the **"Make this a partner"** form.
3. Pick a **Partner role** — this is the only required field — and fill in whatever else you
   already know.
4. Save. The company is now a partner: it appears in the partner list, and the Partner tab
   switches from the setup form to the editable partner record.

Everything on the form can be changed later from the same tab. Becoming a partner is a normal
governed write like any other: it is recorded in the audit log with who did it and when.

## What the fields mean

**Partner role** *(required)* — what kind of partner they are to us:

| Role | Meaning |
|---|---|
| **Hosting** | They run the software for their clients. |
| **Consulting** | They advise clients and bring us in. |
| **Strategic** | A broader alliance that doesn't fit the other two. |

Implementation and development work is Gradion's own turf, which is why there is no role for it.

**Certification status** — where they stand in the certification funnel. A new partner starts
as **Applied**; move them to **Certified** once they pass, and to **Suspended** if the
certification is revoked. This is a statement about certification only — suspending a partner
does not archive the company or stop anything else.

**Margin tier** — how deeply this partner is involved in selling, and the share of a deal's
value they earn on the ones they source. It is a standing property of the *partner*, not of a
deal: every deal they bring earns at their current tier.

| Tier | You are here when… |
|---|---|
| **Intro (15%)** | they make introductions and hand the opportunity over. |
| **Active Collab (20%)** | they work the opportunity alongside you. |
| **Partner closed (25%)** | they run the sale and close it themselves. |

Leave it **Not set** until a tier has actually been agreed; the field is deliberately optional
so the record never claims a deal term that doesn't exist yet. A partner with no tier accrues
no commission — a won deal attributed to them records a skip rather than a zero-value entry,
so the gap is visible instead of looking like a settled nothing.

The tier is **frozen onto each commission entry at the moment it accrues**, so re-tiering a
partner changes what their future deals earn and never rewrites what a past one already did.

**Relationship stage** — where the *relationship* is, independent of certification. The stages
read as a funnel:

| Stage | You are here when… |
|---|---|
| **Research** | you're still figuring out whether they're worth approaching (the default). |
| **Identified** | you've decided they're a real prospect. |
| **Contacted** | you've reached out, no real conversation yet. |
| **In conversation** | an actual dialogue is running. |
| **Fit confirmed** | both sides agree there's a fit. |
| **Agreement pending** | paperwork is in motion. |
| **Active** | the partnership is live. |
| **Active — referring** | live *and* actually sending business. |
| **Dormant** | it was live, but it's gone quiet. |
| **No fit** | you looked, and it's a no. Keeps the record honest instead of deleting it. |

**Next step / Next step due** — the one concrete thing that moves this relationship forward,
and when it's due. Treat an empty next step on a partner that isn't Active (or No fit) as a
smell: it means nobody owns the relationship right now.

**Served segments** — free-form, comma-separated tags for the client segments this partner
serves (say, `SMB, agencies, e-commerce`). There is no fixed list; use the same words across
partners so filtering stays useful.

## Work the program day to day

- **The partner list** — from the **Companies** list header, open **Partners**. It is the flat
  list of every partner, filterable by role and certification status. This is your program
  overview: who's applied, who's certified, who's suspended.
- **The company page stays the home** — activities, people, and deals with a partner live on
  the company page like for any other company. The Partner tab is one more tab there, not a
  separate world.
- **Two lists, two questions.** A company's **Deals** tab shows deals where it is the
  *customer*. Its **Partner** tab shows **Deals they brought** — deals belonging to *other*
  companies that came through this partner. A partner-sourced deal appears only in the second,
  because the deal belongs to the customer.
- **Commission** sits under that, one row per entry, naming the deal it was earned on. Entries
  appear on their own when a partner-sourced deal is won; nobody creates them by hand. The
  statuses (accrued → approved → paid, or reversed) are shown but **cannot yet be changed from
  the app** — deciding a commission exists in the API only.
- **A simple weekly loop:** filter the list for **Applied** and push certifications forward;
  scan for partners whose stage is behind reality and correct it; make sure every in-flight
  partner has a next step with a due date.

Agents reach partners INDIRECTLY today. A partner organization is an ordinary company to the
generic record tools, so an agent can find it, read it and see that it carries the `partner`
relationship type — but `partner` is not yet one of the record types those tools accept, so the
partner extension's own fields (tier, certification, relationship stage) are not readable or
writable that way. A deal's partner and what that partner did for it ARE agent-visible, through
the deal's own `partner_org_id` and `partner_attribution` fields — readable, and settable both
when an agent creates a deal and when it updates one.

## Changing the value lists themselves

The dropdown values — the three roles, the three certification statuses, the three margin
tiers, the ten stages — are **fixed vocabulary, not admin configuration**. There is no settings
screen for them, on purpose: they are enforced end to end, in the API contract
([`backend/api/crm.yaml`](../../backend/api/crm.yaml), the `Partner` schema), as database CHECK
constraints, and in the form's option lists
([`frontend/src/screens/partners.tsx`](../../frontend/src/screens/partners.tsx)).

If the program outgrows a list — a fourth role, a different tier structure — that is a small
code change plus a migration, not a workaround: change the enum in the contract, add a
migration for the CHECK constraint, run `make gen`, and extend the option list and its labels
in the frontend. [add-an-endpoint.md](add-an-endpoint.md) walks the contract-change loop;
[apply-migrations.md](apply-migrations.md) covers the migration half.

For per-partner data that doesn't fit the fixed fields, you don't need any of that: **Settings →
Custom fields** lets an admin add custom fields without touching code.
