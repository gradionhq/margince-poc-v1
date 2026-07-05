# 09 — the scheduling contract names a meeting host the data model cannot store

**Where:** `contract/crm.yaml` `/availability` + `/bookings` vs `contract/data-model.md` §activity.

**What:** `getAvailability` takes `host_user_id` ("Host to check, defaults to the
caller") and `bookMeeting` accepts `host_user_id` in the body — the host is a
first-class scheduling concept. But the `activity` table gives the host no column:
`assignee_id` is locked to tasks by `activity_task_fields`
(`CHECK (kind = 'task' OR (due_at IS NULL AND assignee_id IS NULL AND is_done = false))`),
and `captured_by` is stamped from the authenticated principal, so it cannot carry a
book-on-behalf-of host. Free/busy over the CRM's own record is therefore
unanswerable from the documented schema.

**Build-side reading (implemented):** additive migration `0030_meeting_host` adds
`activity.host_user_id uuid NULL REFERENCES app_user(id)` with
`CHECK (host_user_id IS NULL OR kind = 'meeting')` plus a partial index on
`(workspace_id, host_user_id, occurred_at)` for the free/busy read.

**Spec fix wanted:** either add `host_user_id` (meeting-only) to data-model.md
§activity, or state that availability/booking is answerable only once a calendar
connector exists and the CRM stores no host.

Also missing: a **calendar-delegation grant**. features/04 §1 has no object/action
for "book on another user's calendar"; the build gates cross-host booking on an
unbounded row scope (admin) until the matrix names one.

Related: the meeting end is also unrepresentable (`occurred_at` only, no
duration/end column), so free/busy has to assume a meeting length; 0030 does not
add one because the contract never reads it back.
