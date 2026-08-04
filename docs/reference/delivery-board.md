# Filing findings on the delivery board

The delivery board is GitHub org **Project 9 "Margince Delivery"**
(<https://github.com/orgs/gradionhq/projects/9>). It tracks the spec repo's
delivery tickets *and* this repo's findings on one surface, so a finding that
never reaches the board is invisible to planning.

## When to file

Engineer's judgment — anything found while working (a bug, a gap, a hardening
task, a follow-up too big for the current PR) that you are **not** fixing in
the change you're shipping. The other two routes stay as they are: a fix you
make now is recorded by its own commit and PR, and a **spec** defect is
reconciled upstream against `margince-foundation` (contract-first, P3), not
filed here. A `TODO` in code requires an issue ref — this is where that issue
comes from.

## Where

A real GitHub issue in **`gradionhq/margince-poc-v1`**, then added to
Project 9. Filing the issue without adding it to the board is the recurring
mistake — the board is how findings get planned, so both steps are the job.

**This repo is public.** Nothing in the title or body may reference private
material: no `margince-foundation` file paths, no local `/Users/...` paths,
no secrets, no customer data. Cite the spec by chapter/ADR/pin ID
(e.g. "ADR-0054 §3"), never by path.

Issue mutations are remote writes: in a sandboxed agent session, run the `gh`
commands below with host escalation (same rule as pushing and PR creation).

## How

```sh
# 1. Create the issue (labels: bug | enhancement | security | documentation — closest fit, optional)
gh issue create -R gradionhq/margince-poc-v1 \
  --title "<the finding, stated as a claim>" \
  --body-file <body.md> --label bug

# 2. Add it to the board; capture the item id
gh project item-add 9 --owner gradionhq \
  --url <issue-url> --format json --jq .id

# 3. Set Status (new items land status-less; Backlog is the intake column)
gh project item-edit --project-id PVT_kwDOAhSpyc4BbGvi --id <item-id> \
  --field-id PVTSSF_lADOAhSpyc4BbGvizhV5pdk --single-select-option-id f75ad846
```

Field and option IDs (`gh project field-list 9 --owner gradionhq` re-derives
them if the board changes):

| Field | Field ID | Options |
|---|---|---|
| Status | `PVTSSF_lADOAhSpyc4BbGvizhV5pdk` | Backlog `f75ad846` · Ready `61e4505c` · In progress `47fc9ee4` · In review `df73e18b` · Done `98236657` |
| Size | `PVTSSF_lADOAhSpyc4BbGvizhV5ptI` | XS `6c6483d2` · S `f784b110` · M `7515a9f1` · L `817d0097` · XL `db339eb2` |

Status **Backlog** is the default for a fresh finding; set **Ready** only when
it is scoped enough to pick up as-is. Size is optional — set it when you can
estimate honestly, leave it for triage when you can't. Listing the board needs
`gh project item-list 9 --owner gradionhq --limit 1000` (it holds ~900 items).

If the finding belongs under an epic (epics are issues labeled `epic` in
`margince-foundation`), link it as a sub-issue via GraphQL using each issue's
`node_id` (`gh issue view <n> --json id`):

```sh
gh api graphql -f query='mutation($p:ID!,$c:ID!){
  addSubIssue(input:{issueId:$p, subIssueId:$c}){subIssue{title}}}' \
  -f p=<epic-node-id> -f c=<finding-node-id>
```

## Title and body

The title states the finding as a claim a reader can evaluate from the list
view — "REST agent gate stages approvals before validating the body", not
"Approval bug". The body gives what a stranger needs to pick the issue up in a
fresh session: what you observed and where (file/endpoint/behaviour),
why it matters, reproduction steps if it's a bug, and any known constraints
on the fix. No session narration or build-process residue — the finding must
stand alone. Several small related findings from one investigation may share
one issue (split later if any grows); unrelated findings get their own.
