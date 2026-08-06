# Import your LinkedIn network

Upload your own `Connections.csv` so Margince can answer one question about an account: **does anybody
here already know somebody who works there?** This guide is **UI-first**, with the equivalent `curl`
shown alongside for scripting and verification. For the mental model — participants, the interaction
edge, and where this weaker evidence tier sits beside real interaction history — read
[explanation/relationship-graph.md](../explanation/relationship-graph.md) first.

> **Your network, not the company's.** The owner of every imported row is the **authenticated caller**,
> never a field in the file. There is deliberately no way to upload somebody else's network on their
> behalf: the whole point of the feature is that *"Lars knows them"* means Lars.

## What this is not

- **Not a LinkedIn integration.** There is no LinkedIn app to register, no OAuth app to approve, no API
  to be granted. This is your own data export, read by the importer.
- **Not scraping.** Nothing here talks to LinkedIn at all. You download the file; you upload the file.
- **Not a contact import.** The rows never become people. See
  [What the imported rows are](#what-the-imported-rows-are).

**The onboarding "Authorize with LinkedIn" step is still a placeholder.** The connect scene's LinkedIn
card shows the scopes a live integration *will* request and then says so in the dialog itself: *"our
LinkedIn app is still awaiting approval, so no connections sync yet — this only records your consent
and profile. Upload Connections.csv under Settings, which works today."* Clicking **Authorize** does
exactly one thing — `PUT /me/linkedin-account`, which stores your profile URL and a `connected_at`
timestamp. No connections are fetched. The Member Data Portability API is designed as a second writer
onto the same rows once an app is approved; the CSV path is the one that works now.

## Step 1 — Get the file from LinkedIn

LinkedIn hands every member their own export, no approval involved:

1. On LinkedIn, go to **Settings → Data privacy → Get a copy of your data**.
2. Request the archive and wait for the download link.
3. Unzip it. **The file you want is `Connections.csv`** — the archive holds a dozen others, and picking
   the wrong one fails with a parse error that explains nothing.

Don't edit the file. The importer recognizes the header by its **content** rather than by position (the
export format has changed more than once and differs by locale — English and German headers are both
recognized), and it tolerates the `Notes:` preamble LinkedIn puts above the header. A file with no
recognizable header row is refused as `422 unreadable_export` rather than half-imported.

## Step 2 — Import it

**Settings → Integrations → LinkedIn connections.**

1. Optionally paste **your LinkedIn profile URL** and click **Save profile**. This is what attributes
   the network to you by name, so the CRM says "Anna knows them" rather than "the company knows them".
   An empty value *clears* the stored URL — emptying the field means "do not record this", not "leave
   it".
2. Click **Choose Connections.csv** and pick the file. The upload starts on selection.
3. The result appears as four counts: **Connections imported**, **Matched to a contact**, **Awaiting
   your confirmation**, and — only when non-zero — **Rows skipped (no usable name)**.

The onboarding connect scene has a LinkedIn card too, but as noted above it only records your profile
and consent. Settings is where the actual import happens.

<details><summary>Same thing via <code>curl</code></summary>

The endpoint is `multipart/form-data` with a part named `file`, and it is
`x-agent-access: human-only` — a session cookie only; an agent Passport is refused by design. The
upload is bounded at **8 MB**, which is generous for a few thousand rows of short text and still
refuses a mis-picked video before it reaches the CSV reader.

```sh
curl -X POST http://localhost:8080/v1/me/linkedin-connections \
  --cookie 'crm_session=<session>' \
  -F 'file=@Connections.csv;type=text/csv' \
  | jq '{rows, imported, skipped, confirmed, suggested}'

# your own profile row, and the connection count it yielded
curl --cookie 'crm_session=<session>' http://localhost:8080/v1/me/linkedin-account \
  | jq '{connected, connected_at, profile_url, connections}'
```

Note that `confirmed` and `suggested` in the response are your **totals**, not this pass's delta. The
matcher only considers rows nobody has decided on, so re-importing the same export truthfully reports
zero *new* matches — and a card labelled "Matched to a contact" showing `0` in that state would simply
be wrong.
</details>

## What the imported rows are

They are **ghosts** (CG-DDL-2): graph substrate, and nothing else. The migration that created the table
states it as the safety property the whole feature rests on — an export is a list of third parties who
never agreed to be in anyone's CRM, and turning three thousand of them into contacts would be a consent
problem and a data-quality catastrophe at the same time.

Concretely, an imported row:

| | |
|---|---|
| **Is invisible to** | search, lists, the people screens, and the assistant's record tools |
| **Cannot be written to** | no timeline, no activities, no fields — nothing can write to a ghost |
| **Cannot be reached by outreach** | no email, no sequence, no send path resolves one |
| **Belongs to** | the authenticated caller, always — `POST /me/linkedin-connections`, never `/users/{id}/…` |
| **Exists to answer** | exactly one question: does anyone here already know someone at this company |

The one thing a ghost ever contributes to a real record is deliberately narrow: when you **confirm** a
match, the *connection's own* profile URL is written to that contact as a `linkedin` handle — and
nothing else. The ghost's name, employer, position and connection date stay where they are. (It is the
connection's URL, not yours: stamping your own address on every contact you confirm would put the wrong
person's address on the record. A row already carrying a `linkedin` handle is left alone — that handle
is somebody's statement, and confirming a match is not grounds to replace it, so the response tells you
which happened rather than leaving you to guess.)

## What matching decides

Matching runs immediately after the upload, so the response can say what your import actually achieved.
It follows the same rule the rest of the people module obeys: **only an email address is an exact person
key.**

| Evidence | Outcome | Why |
|---|---|---|
| **Exact email** matches a contact's address | **Confirmed automatically** | An address is identity here, exactly as it is on the capture path. Treating it as a suggestion would ask a human to re-confirm a fact the system is already certain of everywhere else. |
| **Exact name + matched employer**, no other candidate | **Confirmed automatically** | The two strings are the same string, the employer agrees, and nobody else here is called that. Asking about it trains people to click through the queue without reading, which is what makes the genuinely uncertain ones dangerous. |
| **Folded name + matched employer** ("André" vs "Andre") | **Suggested** — goes to the approval inbox | Whether two spellings are one person is a judgement, not a comparison. |
| **Ambiguous name** — two contacts of the same name at the same employer | **Nothing** | Picking one would be a guess wearing a confirmation's clothes. |
| Name only, no employer match | **Nothing** on the person side | Name alone is not a match in any market and least of all in this one. There are two Andreas Müllers at every large German firm. |

Two further rules the matcher applies:

- The employment must be **live today** — `archived_at IS NULL` and `(ended_at IS NULL OR ended_at >
  today)`. A future end date is still employment: a person leaving next month is at their desk today.
- It will not propose a contact some *other* ghost of yours is already confirmed against. One contact
  cannot be two different LinkedIn connections of the same colleague, and offering that choice invites a
  wrong click.

**Nothing here ever creates a person.** A ghost that matches nothing stays a ghost, and its only
contribution is the account-level count — which needs no identity at all.

### Where the suggestions go

A match a human has to judge is an **approval**, not a queue of its own. Suggested matches stage into
the ordinary approval inbox as `linkedin_match` proposals — one per match, not one per import, because
the decisions are independent and a batch proposal would force you to take thirty links to get the
three you wanted. The proposal carries the export's own spelling of the connection (name and employer)
plus the contact it is proposed against, because that is what you judge the guess on.

Rejection is **durable**. The approval row persists and the matcher skips a ghost that already carries a
decided proposal, so refusing "André is Andre" once means never being asked again — including after a
re-import, which is the case that matters when you refresh a five-thousand-row export.

## Why nothing matched yet

Zero matches on a fresh workspace is expected, not broken. Your export is uploaded during onboarding;
the people and accounts it *could* match arrive over the following hours as mail capture runs. On a real
5,064-row export, 54 of the workspace's contacts appeared in the file by name and the upload-time pass
matched 13 — the rest were people and employers the CRM learned about minutes later.

Two mechanisms close that gap, and neither needs you to re-upload:

- **The event path** (`cg:linkedin-match`). `person.created`, `person.updated` and the organization
  events already reach the outbox because the write shape puts them there, so manual entry, capture, a
  site read, a merge and an import all trigger a re-match without any of them knowing the matcher
  exists. Organization events matter for a sharper reason: most unmatched ghosts are waiting on an
  *employer*, not on a name, so an account appearing unblocks a batch at once.
- **The `linkedin_rematch` sweep**, which fans out per workspace **every hour**. The job contract says
  why hourly rather than daily: the window it covers is a workspace's first day — an export uploaded
  during onboarding waits on a capture backfill that finishes in minutes, and a rep who imported their
  network in the morning should not wait until tomorrow to see it on an account.

Both passes look only at **unmatched** ghosts, so a confirmation or a rejection is never revisited and a
caught-up workspace costs one query. Both run under **your own** authority rather than a system
principal's — a system principal is unbounded by design, which would turn a one-row CSV into an oracle
(upload a guessed address, wait, read the match status, learn whether a contact you cannot see exists).

## The payoff — where your network reaches

**Settings → Integrations → Where your network reaches** (`GET /me/linkedin-reach`) is the account-level
answer the import is for. Per account it reports:

| Column | Meaning |
|---|---|
| **Account** | the organization, linking to its company page |
| **You know** | how many of *your* connections work there |
| **Already contacts** | how many of those are confirmed matches, shown as `{on file} of {total}` |

The gap between the two columns is the finding: **people you know at this account who are not in the
CRM.** Rows are ranked by connection count, then name, then id, so two reads of an unchanged network
return the same order. A footnote states what the view cannot show — how many accounts were truncated
by the page limit, and how many connections resolved to no account at all.

<details><summary>Same thing via <code>curl</code></summary>

```sh
curl --cookie 'crm_session=<session>' 'http://localhost:8080/v1/me/linkedin-reach?limit=25' \
  | jq '{accounts_total, unresolved_connections,
         accounts: [.accounts[] | {display_name, connections, contacts_on_file}]}'
```
</details>

### Why the reach total and the import total differ — and both are true

They count different things, on purpose:

- The **import summary** counts rows in your file: `rows`, `imported`, `skipped`.
- The **reach view** counts only connections that were **placed at an organization you can read**.
  Everything else lands in `unresolved_connections`.

So a connection is "unresolved" in three distinct situations, and the view deliberately cannot tell them
apart: the employer matched no account on file, the employer string was unusable, or the employer
resolved to an account **outside your row scope**. That last collapse is load-bearing rather than lazy.
The two numbers can be subtracted, so a connection appearing in neither the visible accounts nor the
unresolved total would itself announce *"this employer resolved to something you are not allowed to
see"* — an account-enumeration oracle you could drive by uploading one row per guessed company name.
Counting it as unresolved makes it indistinguishable from a company nobody here has on file, which is
the same answer every row-scoped list in the product gives.

`accounts_total` counts every account reached, not just the page returned — a truncated list read as the
whole network would understate reach, which is the one thing this view exists to state.

## Re-importing a refreshed export

Re-importing **updates** rather than duplicates. People re-export regularly, and a second import that
doubled everyone's network would make the reach counts meaningless. The upsert:

- Keys on `(owner, normalized name, normalized company, connected-on date)` for CSV rows — an explicit
  best-effort dedupe key, not an identity claim. The connection date is in it precisely because two
  same-named people at one company almost certainly did not connect on the same day.
- Repairs stale keys **before** upserting. `normalized_company` is a derived part of that key, so rows
  written under an older normalizer would no longer collide with what the current import computes. This
  is not hypothetical: cleaning LinkedIn's headline company field once altered the key for every row
  whose company carried a tagline, and re-importing the same export produced 209 duplicate connections
  on a real workspace, double-counting every reach number they fed.
- Lets a **later export win on the profile URL** (someone who changed their vanity address is reachable
  at the new one) and keeps an existing email when the new row has none.
- **Revives** a connection an earlier export had dropped, by clearing its tombstone.

Unusable rows are **counted, never silently dropped** — an import that quietly ignored half a file while
reporting success is worse than one that fails:

| What you see | What happened |
|---|---|
| `skipped > 0` | Rows with no usable name. They identify nobody, so they are counted and passed over. |
| `imported < rows - skipped` | A row matched the **erasure suppression list** by address and was refused. An erased subject must not walk back in through a colleague's next export — an import that did not consult the list would undo an Art. 17 request with a file upload. It is not reported as imported, because that would tell you your file landed data the system deliberately destroyed. |
| `422 unreadable_export` | No recognizable LinkedIn header row. Export the file from LinkedIn without editing it. |
| `422 invalid_multipart` / `422 required` | Not sent as `multipart/form-data`, over the 8 MB limit, or missing the `file` part. |

## Lifecycle and privacy

**When your account is deactivated, your ghosts go with you.** The deactivation transaction deletes
every `linkedin_connection` you own, atomic with your session and passport revocation — no window in
which the account is gone and the address book is not. It is *deleted*, not tombstoned, because a
tombstone still holds the names. The reasoning is recorded in the ownership gate: it is your private
address book of third parties whose only tie to this installation was your employment, so it cannot
outlive that employment.

**When a subject is erased, their ghosts go too.** An Art. 17 erasure deletes the subject's ghosts in
the same single transaction as the rest of the cascade — a ghost holds the subject's name, employer and
address, imported from a colleague's export without the subject ever being asked, and it is invisible to
every person-keyed clause because a ghost is not a person row. Deletion runs on **suggestion-grade**
evidence rather than only a confirmed match: matched to them, carrying their address, carrying their
LinkedIn URL, or bearing their name at an employer they actually work for. The asymmetry is deliberate —
deleting one ghost too many costs a re-import of a file you still have, while keeping one too few leaves
a named person's data behind after we certified it destroyed. Details in
[explanation/privacy-and-consent.md](../explanation/privacy-and-consent.md).

**Your network is yours.** Every operation in this guide is `/me/…` and `x-agent-access: human-only`:
there is no API path to another member's LinkedIn account or connections, for any seat, **admin
included**. A colleague's professional network is theirs, and an admin has no more business editing that
URL than editing their personal address book. No agent passport can drive any of it either. Two honest
qualifications:

- A **suggested match** becomes an approval, and who may decide an approval is the inbox's ordinary rule
  — the grants the effect needs, plus visibility of the **contact** the proposal is about. So a
  colleague who can already read that contact can see that one proposal, which names the connection's
  own spelling of their name and employer. That is settled deliberately: who-knows-whom is
  workspace-shared metadata, guarded by "you only see edges for a person you can see at all".
- The **audit row and the outbox event** for an import name **no connection at all** — only `rows`,
  `imported` and `skipped`. Recording the names there would defeat the invisibility that is the whole
  safety property of a ghost. Likewise, saving your profile records the URL in *your own* audit row but
  keeps it out of the fanned-out event: a subscriber needs to know the authorization moved, not what
  your LinkedIn address is.

## Verify end-to-end

1. **The file was read.** The result card shows `Connections imported` > 0, and `Rows skipped` matches
   your expectation of how ragged the export was.
2. **Nothing became a contact.** Search for an imported connection's name in the people screen and in
   the global search — no result. `GET /people` does not list them.
3. **Matching obeyed the rule.** A connection whose exported address is already a contact's address
   shows as confirmed; a same-name-different-spelling one is waiting in the **approval inbox** as a
   `linkedin_match` proposal, not in a settings queue.
4. **A rejection sticks.** Reject one proposal, re-upload the same file, and confirm you are not asked
   again.
5. **Re-import does not duplicate.** Upload the same file twice; `Connections imported` stays flat
   across the two runs rather than doubling, and **Where your network reaches** shows the same counts.
6. **The unresolved total is honest.** On a fresh workspace with no accounts, the reach card says all
   *N* of your connections work somewhere that is not an account on file yet — not "none yet".
7. **Matches appear as the CRM fills up.** Let mail capture run (or create a contact by hand at a
   matching employer) and confirm the count moves without re-uploading — within the hour at worst.

## Where to go next

- The model this feeds: participants, the interaction edge, warmth, and deal coverage —
  [explanation/relationship-graph.md](../explanation/relationship-graph.md).
- Where the *strong* evidence tier comes from — connecting a mailbox so real interaction history exists
  at all: [connect-a-mailbox.md](connect-a-mailbox.md) and
  [explanation/capture-connectors.md](../explanation/capture-connectors.md).
- What happens to all of this under an Art. 17 request:
  [explanation/privacy-and-consent.md](../explanation/privacy-and-consent.md).
