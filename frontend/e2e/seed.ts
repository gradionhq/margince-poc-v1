import type { Page, Route } from "@playwright/test";
import { type GrantSpec, meFixture } from "../src/app/mefixture";
import { type MockProject, projectMock } from "./projectmock";

// The AC specs drive an admin, and the UI now scopes every write affordance on
// the grant map /me carries rather than on the role name — so the mock has to
// answer with real grants or every button under test disappears.
//
// Listed explicitly rather than "everything": a spec that reaches a surface
// this list forgot fails loudly here, which is how the omission gets noticed.
// Extend it when a new AC needs a new object.
const E2E_ADMIN_GRANTS: GrantSpec = {
  automation: ["create", "read", "update", "delete"],
  overlay_connection: ["create", "read", "update", "delete"],
  pipeline: ["create", "read", "update", "delete"],
  custom_field: ["create", "read", "update", "delete"],
  webhook_subscription: ["create", "read", "update", "delete"],
  capture_settings: ["read", "update"],
  embedding_reindex: ["read", "update"],
  fx_rate: ["create", "read", "update"],
  ai_model_rate: ["create", "read", "update"],
  saved_view: ["create", "read", "update", "delete"],
  // The Voice DNA surface, which gates every write on this one object (core
  // migration 0042 grants admin/ops/manager all four). Without it the
  // `settings/voice` sweep measures a card with no controls at all — the
  // read-only posture a `read_only` seat gets, not the admin the specs drive.
  voice_profile: ["create", "read", "update", "delete"],
  // The installation's own profile — name, timezone, base currency — which the
  // organization card gates every write on. The unsaved-guard suite needs it:
  // that card's draft is the one that OUTLIVES its dialog, so it is the only
  // settings edit a reader can still be holding while they navigate away.
  installation_settings: ["read", "update"],
  // The consent registry's own gate, and so the Privacy & audit ENTRY's: the
  // server reads purposes under `person:read` (consent/store.go), not under a
  // role. Read alone, because no spec exercises a person write from here and a
  // grant this fixture does not need is a grant it should not claim.
  person: ["read"],
};

// The coherent seed (mirrors design/seed-fixtures.md entities: Anna Weber,
// Brandt Automotive, the fleet-retrofit deal) mocked at the network edge so
// the harness is hermetic and the explainer arithmetic reconciles across
// screens. BASE_URL mode skips this and hits a live backend instead.

export const stages = [
  {
    id: "s1",
    workspace_id: "w",
    pipeline_id: "pl",
    name: "Qualify",
    position: 1,
    semantic: "open",
    win_probability: 20,
  },
  {
    id: "s2",
    workspace_id: "w",
    pipeline_id: "pl",
    name: "Proposal",
    position: 2,
    semantic: "open",
    win_probability: 40,
  },
  {
    id: "s3",
    workspace_id: "w",
    pipeline_id: "pl",
    name: "Negotiation",
    position: 3,
    semantic: "open",
    win_probability: 60,
  },
  {
    id: "s4",
    workspace_id: "w",
    pipeline_id: "pl",
    name: "Won",
    position: 4,
    semantic: "won",
    win_probability: 100,
  },
  {
    id: "s5",
    workspace_id: "w",
    pipeline_id: "pl",
    name: "Lost",
    position: 5,
    semantic: "lost",
    win_probability: 0,
  },
];

// One working lead for the leads list and page: named, owned by u1, scored,
// promotable (it has an email), with the identity fields the inline rows edit.
export const seededLead = {
  id: "l-1",
  workspace_id: "w",
  full_name: "Jonas Petersen",
  email: "jonas@nordwind.example",
  title: "Head of Operations",
  company_name: "Nordwind Logistik",
  status: "contacted",
  score: 46,
  owner_id: "u1",
  captured_by: "human:u1",
  source: "inbound",
  version: 3,
  created_at: "2026-07-01T08:00:00Z",
  updated_at: "2026-07-05T08:00:00Z",
};

export const anna = {
  id: "p-anna",
  workspace_id: "w",
  full_name: "Anna Weber",
  title: "Head of Procurement",
  emails: [{ id: "e1", email: "anna.weber@brandt.example", is_primary: true }],
  captured_by: "connector:gmail",
  source: "gmail",
  version: 1,
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-06-20T08:00:00Z",
};

export const brandt = {
  id: "o-brandt",
  workspace_id: "w",
  display_name: "Brandt Automotive GmbH",
  industry: "Automotive",
  size_band: "201-500",
  classification: "customer",
  captured_by: "human:u1",
  source: "manual",
  version: 1,
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-06-01T08:00:00Z",
};

export const deals = [
  {
    id: "d-fleet",
    workspace_id: "w",
    name: "Fleet retrofit",
    amount_minor: 4_800_000,
    currency: "EUR",
    pipeline_id: "pl",
    stage_id: "s2",
    organization_id: "o-brandt",
    project_id: null as string | null,
    status: "open",
    stalled: true,
    // Every mutable record carries its row version, because the advance and the
    // patch send it back as their precondition. A fixture without one lets a
    // write ship unpinned in the test while the real client refuses it.
    version: 2,
    source: "manual",
    captured_by: "human:u1",
    created_at: "2026-05-01T08:00:00Z",
    updated_at: "2026-06-01T08:00:00Z",
    last_activity_at: "2026-05-01T08:00:00Z",
  },
  {
    id: "d-service",
    workspace_id: "w",
    name: "Service contract",
    amount_minor: 1_250_000,
    currency: "EUR",
    pipeline_id: "pl",
    stage_id: "s1",
    organization_id: "o-brandt",
    project_id: null as string | null,
    status: "open",
    stalled: false,
    version: 5,
    source: "manual",
    captured_by: "human:u1",
    created_at: "2026-06-15T08:00:00Z",
    updated_at: "2026-06-20T08:00:00Z",
    last_activity_at: "2026-06-28T08:00:00Z",
  },
];

// The body of work the fleet deals are about, born in `initiative` while the
// deal is still open and attached to no deal yet — the project page's empty
// state and the deal form's project picker both read from it.
export const seededProject: MockProject = {
  id: "pr-fleet",
  workspace_id: "w",
  name: "Flottenumbau Brandt",
  key: "BRANDT-FLEET",
  organization_id: "o-brandt",
  owner_id: "u1",
  phase: "initiative",
  closed_reason: null,
  description: null,
  target_end_date: null,
  version: 1,
  source: "manual",
  captured_by: "human:u1",
  created_at: "2026-05-01T08:00:00Z",
  updated_at: "2026-05-01T08:00:00Z",
  last_activity_at: null,
};

// One persisted Morning-Brief run over the two seeded deals — the §10.1
// composite with its factor decomposition, so the home queue's arithmetic
// reads coherently against the deal amounts above.
export const briefRun = {
  id: "br-1",
  generated_at: "2026-07-05T05:30:00Z",
  as_of: "2026-07-05T05:00:00Z",
  candidate_count: 2,
  revenue_norm_minor: 4_800_000,
  items: [
    {
      id: "bi-1",
      deal_id: "d-fleet",
      rank: 1,
      composite: 0.74,
      feature_vector: {
        winnability: 0.4,
        revenue: 1,
        timing: 0.75,
        momentum: 1,
        warmth: 0.47,
      },
      evidence_ids: ["ev-1", "ev-2"],
      state: "new",
      state_at: null,
    },
    {
      id: "bi-2",
      deal_id: "d-service",
      rank: 2,
      composite: 0.41,
      feature_vector: {
        winnability: 0.2,
        revenue: 0.26,
        timing: 0.5,
        momentum: 0,
        warmth: 0.3,
      },
      evidence_ids: ["ev-3"],
      state: "new",
      state_at: null,
    },
  ],
};

export const approval = {
  id: "ap-1",
  workspace_id: "w",
  kind: "send_email",
  status: "pending",
  proposed_by: "agent:runner",
  summary: "Send the follow-up to Anna Weber",
  proposed_change: { subject: "Follow-up", body: "Hi Anna" },
  confidence: 0.62,
  evidence: [
    { evidence_snippet: "shall we sync next week?", source_type: "activity" },
  ],
  created_at: "2026-07-05T05:00:00Z",
};

// The closed automation starter library (B-EP09.15): two types, one integer
// parameter each — the editor derives its form from params_schema alone.
export const automationCatalog = [
  {
    key: "stalled_deal_nudge",
    name: "Stillstands-Erinnerung",
    description: "Staged a follow-up when a deal stalls.",
    trigger: "deal.stalled",
    action: "send_email",
    tier: "confirmation_required",
    params_schema: {
      type: "object",
      properties: {
        due_in_days: { type: "integer", minimum: 1, maximum: 30, default: 3 },
      },
      required: ["due_in_days"],
    },
  },
  {
    key: "task_on_stage_entry",
    name: "Aufgabe bei Phasenwechsel",
    description: "Creates a task when a deal enters a stage.",
    trigger: "deal.stage_changed",
    action: "create_task",
    tier: "auto_execute",
    params_schema: {
      type: "object",
      properties: {
        due_in_days: { type: "integer", minimum: 1, maximum: 30, default: 7 },
      },
      required: ["due_in_days"],
    },
  },
];

// Pre-seeded instance — the wire carries no origin, so this stands in for
// the agent-authored case and must render like any other row.
export const seededAutomation = {
  id: "au-1",
  key: "task_on_stage_entry",
  name: "Aufgabe nach Phasenwechsel",
  status: "enabled",
  params: { due_in_days: 7 },
  version: 3,
  created_at: "2026-06-20T08:00:00Z",
};

export const passports = [
  {
    id: "pp-1",
    label: "Marcus' Claude",
    scopes: ["read", "draft"],
    created_at: "2026-06-01T08:00:00Z",
    expires_at: "2026-08-01T08:00:00Z",
    last_used_at: "2026-07-04T18:00:00Z",
    revoked_at: null,
  },
  {
    id: "pp-2",
    label: "Alter Runner",
    scopes: ["read"],
    created_at: "2026-05-01T08:00:00Z",
    expires_at: "2026-06-01T08:00:00Z",
    last_used_at: null,
    revoked_at: "2026-05-20T08:00:00Z",
  },
];

// actor_id carries the TYPED principal id, the way storekit stamps it —
// "human:<uuid>", "agent:<id>", "connector:<name>" — and the read path resolves
// the display names beside it. An unprefixed id here is not a simplification: it
// is a shape the server never writes, and it made the "Du" branch look covered
// while being unreachable in the product.
export const auditEntries = [
  {
    id: "al-1",
    workspace_id: "w",
    actor_type: "human",
    actor_id: "human:u1",
    actor_name: "Lena Fischer",
    action: "update",
    entity_type: "deal",
    entity_id: "d-fleet",
    occurred_at: "2026-07-05T07:00:00Z",
  },
  {
    // An agent under ANOTHER human's authority, so the row reads as that person
    // rather than as the viewer — which is what lets the actor-filter assertion
    // below distinguish "Du" from a named teammate.
    id: "al-2",
    workspace_id: "w",
    actor_type: "agent",
    actor_id: "agent:runner",
    passport_id: "pp-1",
    on_behalf_of: "u2",
    on_behalf_of_name: "Marcus Brandt",
    action: "send_email",
    entity_type: "activity",
    entity_id: null,
    occurred_at: "2026-07-05T06:00:00Z",
  },
  {
    // A bare connector: no grant presented, so no human to name and no gap to
    // report. Its own id is what a reader gets.
    id: "al-3",
    workspace_id: "w",
    actor_type: "connector",
    actor_id: "connector:gmail",
    action: "create",
    entity_type: "person",
    entity_id: "p-anna",
    occurred_at: "2026-07-05T05:00:00Z",
  },
];

// The three reads behind Settings → AI and Settings → Maintenance. They are
// fixtures rather than catch-all `page([])` answers because the catch-all is
// not a thin response — it is the WRONG SHAPE, and the two sweeps below cannot
// see what they cannot render: `/admin/job-health` answered `{data,page}` (no
// `kinds`, which crashed the card), `/ai/usage` answered a body with no
// `budget` (an error state), and `/ai/calls` answered an empty page. The AI
// tab's two widest tables therefore never rendered on the very route the 390px
// sweep visits, and the queue report never rendered at all. Every field below
// is required by its contract schema (AiUsage / AiCallListResponse /
// JobHealth) — an under-filled fixture would just move the lie.

export const aiUsage = {
  days: [
    {
      date: "2026-07-04",
      tasks: [
        {
          task: "capture_classify",
          tier: "cheap_cloud",
          calls: 412,
          cached_hits: 96,
          tokens_in: 184_320,
          tokens_out: 21_460,
          cost_est_minor: 118,
        },
        {
          task: "enrich",
          tier: "premium",
          calls: 37,
          cached_hits: 2,
          tokens_in: 96_100,
          tokens_out: 44_820,
          cost_est_minor: 942,
        },
      ],
    },
    {
      date: "2026-07-05",
      tasks: [
        {
          task: "capture_classify",
          tier: "cheap_cloud",
          calls: 388,
          cached_hits: 104,
          tokens_in: 171_005,
          tokens_out: 19_884,
          cost_est_minor: 109,
        },
        {
          task: "summarize",
          tier: "local_small",
          calls: 51,
          cached_hits: 0,
          tokens_in: 60_240,
          tokens_out: 12_015,
          cost_est_minor: 0,
        },
      ],
    },
  ],
  budget: {
    monthly_tokens: 4_000_000,
    spent_tokens: 609_844,
    band: "normal",
    currency: "USD",
  },
};

// Two terminal calls, one clean and one that retried and degraded — the second
// is what puts a badge column and an error sentinel into the widest row, which
// is the row a narrow viewport has to survive.
export const aiCalls = {
  data: [
    {
      id: "0d9f8c2e-6b41-4d2a-9a77-1f3c5b8e0a11",
      occurred_at: "2026-07-05T06:14:00Z",
      task: "capture_classify",
      tier: "cheap_cloud",
      provider: "deepseek",
      model_id: "deepseek-chat",
      served_model: "deepseek-chat-0724",
      calls_attempted: 1,
      tokens_in: 1_284,
      tokens_out: 212,
      reasoning_tokens: 0,
      cached_tokens: 0,
      latency_ms: 940,
      cache_hit: false,
      degraded: false,
      error_sentinel: null,
      has_payload: true,
    },
    {
      id: "b71c4a55-2f08-4c93-8d61-77aa9e4c2b30",
      occurred_at: "2026-07-05T05:58:00Z",
      task: "enrich",
      tier: "premium",
      provider: "anthropic",
      model_id: "claude-sonnet",
      served_model: "claude-sonnet-4-6",
      calls_attempted: 3,
      tokens_in: 12_940,
      tokens_out: 3_118,
      reasoning_tokens: 1_002,
      cached_tokens: 8_400,
      latency_ms: 7_310,
      cache_hit: true,
      degraded: true,
      error_sentinel: "provider_timeout",
      has_payload: false,
    },
  ],
  page: { next_cursor: null },
  payload_capture_enabled: true,
  tasks: ["capture_classify", "enrich", "summarize"],
};

// A queue with something waiting, a dispatcher beside it, and one dead job —
// the dead count is what raises the card's alert, so an all-zero fixture would
// leave the loudest thing on the page unmeasured.
export const jobHealth = {
  generated_at: "2026-07-05T06:20:00Z",
  kinds: [
    {
      kind: "capture_ingest",
      queue: "default",
      fleet_wide: false,
      waiting: 12,
      running: 2,
      retrying: 1,
      dead: 0,
      oldest_waiting_age_seconds: 195,
    },
    {
      kind: "enrich_organization",
      queue: "enrich",
      fleet_wide: false,
      waiting: 0,
      running: 0,
      retrying: 0,
      dead: 2,
      oldest_waiting_age_seconds: null,
    },
    {
      kind: "workspace_dispatch",
      queue: "dispatch",
      fleet_wide: true,
      waiting: 0,
      running: 1,
      retrying: 0,
      dead: 0,
      oldest_waiting_age_seconds: null,
    },
  ],
  recent_failures: [
    {
      kind: "enrich_organization",
      state: "discarded",
      attempt: 5,
      max_attempts: 5,
      failed_at: "2026-07-05T04:41:00Z",
      reason: "The provider refused the request.",
    },
    {
      kind: "capture_ingest",
      state: "retryable",
      attempt: 2,
      max_attempts: 5,
      failed_at: "2026-07-05T06:02:00Z",
      reason: "The mail provider was unreachable.",
    },
  ],
};

export const publicSlots = [
  { start: "2026-07-06T09:00:00Z", end: "2026-07-06T09:30:00Z" },
  { start: "2026-07-06T10:00:00Z", end: "2026-07-06T10:30:00Z" },
];

// The overlay fixtures (B-EP09.23): the incumbent connection, its two health
// reads, and the RFC-7807 refusal shape a mirrored write answers with. Field
// names mirror overlay.test.tsx's unit fixtures (the contract's camelCase on
// OverlayConnection/OverlaySyncStatus, snake_case on OverlayBudget.sources) —
// keep the two in sync if the contract shape changes under either.
export const overlayConnection = {
  incumbent: "hubspot",
  region: "eu1",
  status: "active",
  connectedAt: "2026-07-20T10:00:00Z",
  scopes: ["crm.objects.contacts.read", "crm.objects.deals.read"],
};

export const overlaySyncStatus = {
  objects: [
    {
      object: "person",
      lastSyncedAt: "2026-07-25T08:00:00Z",
      state: "fresh",
      backfillComplete: true,
    },
    {
      object: "organization",
      lastSyncedAt: "2026-07-25T08:00:00Z",
      state: "fresh",
      backfillComplete: true,
    },
    {
      object: "deal",
      lastSyncedAt: "2026-07-25T07:00:00Z",
      state: "pending_sync",
      backfillComplete: false,
    },
  ],
};

export const overlayBudget = {
  window: "2026-07-25T08:00:00Z/PT1H",
  consumed: 120,
  limit: 1000,
  band: "ok",
  sources: { force_fresh: 10, poller: 100, capture: 10 },
  // The server's own "can't attribute a share" sentinel — printed verbatim by
  // the UI (overlay-health.tsx), never recomputed, so the mock must answer it
  // literally rather than a plausible-looking number.
  headroom: "~unknown",
  search: {
    window: "2026-07-25T08:00:00Z/PT1S",
    consumed: 2,
    limit: 20,
    band: "ok",
  },
};

// The mirror user mapping, on the same tab as the connection: the seed's own
// admin holds a matched HubSpot seat, so the overlay tab renders its settled
// state rather than an empty table nobody would ship with.
export const overlayOwners = {
  incumbent: "hubspot",
  owners: [
    {
      incumbent_user_id: "hs-7",
      name: "Lars Brandt",
      email: "lars@brandt.example",
    },
  ],
  truncated: false,
};

// One row of the admin mapping table, shaped as the contract's
// OverlayUserMapEntry. The nullable halves are spelled out rather than left to
// inference: the stateful write handlers below have to be able to CLEAR a
// mapping, and an inferred `string` cannot express the unmapped state the
// endpoint actually produces.
type OverlayUserMapEntry = {
  user_id: string;
  name: string;
  email: string;
  incumbent_user_id: string | null;
  incumbent_user_name: string | null;
  incumbent_user_email: string | null;
  match_source?: "email" | "manual";
  unmapped_reason: string;
};

export const overlayUserMap: {
  incumbent: string;
  entries: OverlayUserMapEntry[];
  next_cursor: string | null;
} = {
  incumbent: "hubspot",
  entries: [
    {
      user_id: "u1",
      name: "Lars Brandt",
      email: "lars@brandt.example",
      incumbent_user_id: "hs-7",
      incumbent_user_name: "Lars Brandt",
      incumbent_user_email: "lars@brandt.example",
      match_source: "email",
      unmapped_reason: "none",
    },
  ],
  next_cursor: null,
};

// PUT/DELETE /overlay/user-map/{id} against the page's own mapping state. The
// real verbs MOVE a row — a manual pin, or an unmap that also stops automatic
// re-matching — so the mock moves it too and the card's post-write reload shows
// what the write did. An id nobody seeded is the endpoint's 404 and a verb it
// does not serve is a 405, because a mapping write that silently succeeds is
// indistinguishable from one that worked.
function overlayUserMapWrite(
  route: Route,
  entries: OverlayUserMapEntry[],
  userId: string,
  method: string,
): Promise<void> {
  if (method !== "PUT" && method !== "DELETE") {
    return route.fulfill({ status: 405 });
  }
  const entry = entries.find((candidate) => candidate.user_id === userId);
  if (!entry) {
    return route.fulfill({ status: 404 });
  }
  if (method === "DELETE") {
    entry.incumbent_user_id = null;
    entry.incumbent_user_name = null;
    entry.incumbent_user_email = null;
    entry.unmapped_reason = "blocked_by_admin";
    delete entry.match_source;
    return route.fulfill({ status: 204 });
  }
  const incumbentUserId = String(
    route.request().postDataJSON().incumbent_user_id ?? "",
  );
  const owner = overlayOwners.owners.find(
    (candidate) => candidate.incumbent_user_id === incumbentUserId,
  );
  entry.incumbent_user_id = incumbentUserId;
  entry.incumbent_user_name = owner?.name ?? null;
  entry.incumbent_user_email = owner?.email ?? null;
  entry.match_source = "manual";
  entry.unmapped_reason = "none";
  return route.fulfill({ status: 204 });
}

function unsupportedBySor(detail: string) {
  return { title: "Unprocessable Entity", detail, code: "unsupported_by_sor" };
}

function page(data: unknown[]) {
  return { data, page: { next_cursor: null } };
}

export type MockApiOptions = Readonly<{
  // "native" (the default) is the full-capability spine every existing AC
  // runs against; "overlay" swaps /me's system_of_record.mode and layers the
  // incumbent-mirror routes on top — same fixtures, so a caller that doesn't
  // pass this option keeps working unchanged (B-EP09.23).
  sor?: "native" | "overlay";
  // "authenticated" (the default) is what every existing AC needs.
  // "unauthenticated" answers /me with 401 so the app renders the login screen
  // instead — the surface a signed-out user actually meets, and the one the
  // §3.8/axe sweeps could not reach because they all start behind a session.
  session?: "authenticated" | "unauthenticated";
  // The federated providers /auth/capabilities reports. The default is `[]` and
  // that is a product fact, not a convenience: the OIDC flow has not shipped, so
  // the running app must never offer a provider button (§19), and the empty list
  // is what proves it. A test that wants to SEE the federated block seeds it
  // here — this option is the only place in this repo where a provider exists.
  oidcProviders?: ReadonlyArray<{ key: string; label: string }>;
}>;

export async function mockApi(
  target: Page,
  options?: MockApiOptions,
): Promise<void> {
  if (process.env.BASE_URL) {
    return; // live-backend mode: no mocking
  }
  // Mutable so DELETE /overlay/connection can flip the workspace back to
  // native within the SAME test (AC-overlay-6) — /me below reads this on
  // every call, the same per-page-state pattern `automations`/`brief` use.
  let sorMode: "native" | "overlay" = options?.sor ?? "native";
  // The auth gate (App.tsx) short-circuits to the signup screen when no
  // workspace slug is resolved, before it ever probes /me — so a hermetic run
  // must seed a slug in localStorage or every authed screen renders auth. The
  // value is a dev-side setting, not tenant authority (the mocked /me is).
  await target.addInitScript(() => {
    globalThis.localStorage.setItem("margince.workspaceSlug", "seed");
  });
  // hermetic runs: no external font fetches
  await target.route("https://fonts.googleapis.com/**", (route) =>
    route.abort(),
  );
  await target.route("https://fonts.gstatic.com/**", (route) => route.abort());

  // per-page automation state so the create→paused→enable flow is coherent
  let automations = [{ ...seededAutomation }];
  // per-page deal patch state so an overlay-mode edit's re-read (the screen
  // invalidates ["deal", id] after a save) reflects the write instead of
  // reverting to the seed — the same "mirror re-read reflects write-back"
  // shape overlay.Provider.Update gives via mirrorWriteResult.
  const dealPatches: Record<string, Partial<(typeof deals)[number]>> = {};
  // per-page brief state so act/dismiss marks stick within a test
  const brief = {
    ...briefRun,
    items: briefRun.items.map((item) => ({ ...item })),
  };
  // per-page connection state so a DELETE is reflected on the next GET —
  // the real Disconnect (overlay/teardown.go's revokeConnection) flips the
  // row's status to "revoked" and never deletes it (overlay/connection.go's
  // Service.Get: a revoked connection still reads back, its status column
  // carrying that fact), so the mock must answer the same shape instead of
  // vanishing the row or reporting 404 — a 404 means no connection was ever
  // inserted, a different state than "disconnected".
  let connection = { ...overlayConnection };
  // per-page mapping state so a map/unmap actually MOVES something. A generic
  // 200 would let the card report a successful write and then reload the
  // untouched fixture — the workflow would pass having done nothing, which is
  // the one outcome a mapping test must not be able to reach.
  const userMapEntries = overlayUserMap.entries.map((entry) => ({ ...entry }));
  // per-page activity state for the rows a spec LOGS and then expects to see
  // again — on the deal it was logged against, and on the project it is
  // relinked to. Only rows linked to a deal or a project are remembered, so
  // the timelines the other suites read as empty stay empty.
  const loggedActivities: {
    id: string;
    kind: string;
    subject: string;
    body: string | null;
    occurred_at: string;
    thread_key: null;
    links: { entity_type: string; entity_id: string }[];
    source: string;
    captured_by: string;
    version: number;
    created_at: string;
    updated_at: string;
  }[] = [];
  const projectState = projectMock({
    seeded: seededProject,
    organizationName: brandt.display_name,
    deals: () => deals.map((deal) => ({ ...deal, ...dealPatches[deal.id] })),
    activities: () => loggedActivities,
  });

  await target.route(/\/v1\//, async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace(/^\/v1/, "");
    const method = route.request().method();
    const json = (body: unknown, status = 200) =>
      route.fulfill({
        status,
        contentType: "application/json",
        body: JSON.stringify(body),
      });

    if (path === "/me") {
      // 401 is an AUTHENTICATION state, and the boundary must tell it apart from
      // an unavailable server (§4) — so this is a clean 401 with a problem body,
      // never an abort, which would land the caller on the connection screen.
      if (options?.session === "unauthenticated") {
        return json(
          { type: "about:blank", title: "Unauthorized", status: 401 },
          401,
        );
      }
      const me = meFixture({ allow: E2E_ADMIN_GRANTS });
      return json({
        ...me,
        user: {
          ...me.user,
          id: "u1",
          email: "lars@brandt.example",
          locale: "de-DE",
        },
        system_of_record: { mode: sorMode },
      });
    }
    // The two anonymous reads the unauthenticated surface makes. Both answer
    // before a session exists, by design: the surface has to show a stranger the
    // installation's posture and its working sign-in methods.
    if (path === "/auth/capabilities") {
      // oidc_providers is empty by default because the OIDC flow does not exist
      // (§19), and an empty list is what proves no provider button renders. A
      // test that wants the federated block on screen passes { oidcProviders }
      // — the markup exists, so the gate has to be this capability rather than
      // the absence of a component.
      return json({
        password: true,
        password_reset: true,
        oidc_providers: options?.oidcProviders ?? [],
      });
    }
    if (path === "/assistant/profile") {
      return json({
        name: "Margince",
        kind: "ai",
        state: "configured",
        inference_mode: "hybrid",
        providers: ["anthropic", "ollama"],
      });
    }
    // The overlay routes: always registered (a native workspace can still
    // read a stale/absent connection), but the three write verbs below only
    // change behavior FROM the native one while sorMode is "overlay" — a
    // caller that never passes { sor: "overlay" } sees the native mock
    // completely unchanged.
    if (path === "/overlay/connection" && method === "GET") {
      return json(connection);
    }
    if (path === "/overlay/connection" && method === "DELETE") {
      // The real disconnect purges the mirror and flips workspace.x_sor_mode
      // for the whole installation — the mock's stand-in for that flip is
      // this flag, read fresh by /me above on the app's next refetch
      // (OverlayCard's onSuccess invalidates every query, /me included).
      sorMode = "native";
      // The connection row itself survives disconnect (revoked, not
      // deleted) — a refetch of the card must see the same revoked-state
      // reconnect affordance the real backend answers, not a stale "active"
      // that the real backend can never produce.
      connection = { ...connection, status: "revoked" };
      return route.fulfill({ status: 202 });
    }
    if (path === "/overlay/sync-status") {
      return json(overlaySyncStatus);
    }
    if (path === "/overlay/budget") {
      return json(overlayBudget);
    }
    if (path === "/overlay/user-map" && method === "GET") {
      return json({ ...overlayUserMap, entries: userMapEntries });
    }
    if (path.startsWith("/overlay/user-map/")) {
      return overlayUserMapWrite(
        route,
        userMapEntries,
        path.slice("/overlay/user-map/".length),
        method,
      );
    }
    if (path === "/overlay/owners" && method === "GET") {
      return json(overlayOwners);
    }
    if (path === "/overlay/reconcile" && method === "POST") {
      // Queues a sweep for the worker's next tick — never runs it in-request,
      // so the response carries no body to report as "finished".
      return route.fulfill({ status: 202 });
    }
    if (sorMode === "overlay" && path === "/people" && method === "POST") {
      // Create is unsupported for every mirrored type (the write mapping
      // leaves owner_id unset, so a created incumbent record would be
      // unowned and invisible — overlay/provider_writes.go's SupportsWrite).
      return json(
        unsupportedBySor(
          "Creating a person isn't supported while reading from HubSpot.",
        ),
        422,
      );
    }
    if (
      sorMode === "overlay" &&
      path.startsWith("/deals/") &&
      path.endsWith("/advance")
    ) {
      // Stage advance stays unsupported outright (no overlay stage map) —
      // OVA-MAP-W6.
      return json(
        unsupportedBySor(
          "Advancing a deal isn't supported while reading from HubSpot.",
        ),
        422,
      );
    }
    if (await projectState.handle(route, path, method, json)) {
      return;
    }
    if (path.startsWith("/deals/") && method === "PATCH") {
      // In overlay mode Update DOES write back through the incumbent seam and
      // succeed (overlay/provider_writes.go Update); natively it is an
      // ordinary write. Either way the mock echoes the patched fields onto the
      // matching seeded deal and remembers the patch so a follow-up GET (the
      // screen's post-save refetch) reflects it too.
      const id = path.slice("/deals/".length);
      const base = deals.find((deal) => deal.id === id) ?? deals[0];
      const body = route.request().postDataJSON();
      dealPatches[id] = { ...dealPatches[id], ...body };
      return json({ ...base, ...dealPatches[id] });
    }
    if (path === "/company/context/capabilities") {
      return json({
        rollout: "onboarding",
        read_enabled: true,
        tasks_enabled: true,
        onboarding_enabled: true,
      });
    }
    // The installation's own company. A described installation is the state
    // every AC below assumes: the shell gates on this, and a 404 would (rightly)
    // redirect them all into onboarding. Onboarding's own AC reaches the wizard
    // by route regardless. Shaped as the contract's CompanyProfile — the generic
    // list fallthrough is not a company, and the form would read display_name
    // off it and crash.
    if (path === "/company") {
      return json({
        organization_id: "o-self",
        display_name: "Brandt Automotive GmbH",
        legal_name: "Brandt Automotive GmbH",
        registered_address: "Werkstraße 4, 70435 Stuttgart",
        register_vat: "DE811234567",
        industry: "Automotive",
        website: "brandt.example",
      });
    }
    if (path === "/people" && method === "GET") {
      return json(page([anna]));
    }
    if (path === "/people" && method === "POST") {
      const body = route.request().postDataJSON();
      return json(
        {
          ...anna,
          id: "p-new",
          full_name: String(body.full_name),
          title: body.title ?? null,
          emails: body.emails ?? [],
          captured_by: "human:u1",
          source: "manual",
        },
        201,
      );
    }
    if (path === "/people/p-new") {
      return json({ ...anna, id: "p-new", full_name: "Peter Neu" });
    }
    if (path === "/organizations" && method === "POST") {
      const body = route.request().postDataJSON();
      return json(
        {
          ...brandt,
          id: "o-new",
          display_name: String(body.display_name),
          industry: body.industry ?? null,
          captured_by: "human:u1",
          source: "manual",
        },
        201,
      );
    }
    if (path === "/organizations/o-new") {
      return json({ ...brandt, id: "o-new", display_name: "Neue Firma GmbH" });
    }
    if (path === "/leads" && method === "POST") {
      const body = route.request().postDataJSON();
      return json(
        {
          id: "l-new",
          workspace_id: "w",
          full_name: body.full_name ?? null,
          email: body.email ?? null,
          company_name: body.company_name ?? null,
          status: "new",
          score: 0,
          captured_by: "human:u1",
          source: "manual",
          version: 1,
          created_at: "2026-07-06T08:00:00Z",
          updated_at: "2026-07-06T08:00:00Z",
        },
        201,
      );
    }
    if (path === "/leads/l-new") {
      return json({
        id: "l-new",
        workspace_id: "w",
        full_name: "Lena Neu",
        email: "lena@neu.example",
        company_name: null,
        status: "new",
        score: 0,
        captured_by: "human:u1",
        source: "manual",
        version: 1,
        created_at: "2026-07-06T08:00:00Z",
        updated_at: "2026-07-06T08:00:00Z",
      });
    }
    if (path === "/deals" && method === "POST") {
      const body = route.request().postDataJSON();
      return json(
        {
          ...deals[0],
          id: "d-new",
          name: String(body.name),
          // Echoed, both halves, exactly as the write sent them. A deal created
          // with no currency comes back with none — a fixture that supplied one
          // would hide from every e2e run the unpriced deal the real server
          // returns.
          amount_minor: body.amount_minor ?? null,
          currency: body.currency ?? null,
          stage_id: String(body.stage_id),
        },
        201,
      );
    }
    if (path === "/people/p-anna") {
      return json(anna);
    }
    // The person record page's ONE composite read (PO-EXT-3). Its `person` is
    // required by the contract, so a body without one is not a thin response —
    // it is a response the page cannot render, which is what the page did here
    // until this handler existed.
    //
    // In overlay mode the mirror holds none of the sections folded from natively
    // captured interactions, so they are NAMED as withheld rather than answered
    // empty: "you cannot see this here" and "there is none" are different facts.
    if (method === "GET" && /^\/people\/[^/]+\/360$/.test(path)) {
      return json({
        as_of: "2026-06-20T09:00:00Z",
        person: anna,
        last_inbound_at:
          sorMode === "overlay" ? undefined : "2026-06-18T08:00:00Z",
        last_outbound_at:
          sorMode === "overlay" ? undefined : "2026-06-19T08:00:00Z",
        sections_omitted:
          sorMode === "overlay"
            ? [
                "activities",
                "strength",
                "network",
                "next_steps",
                "moments",
                "claims",
                "conversation_memory",
                "relationship_changes",
                "since_last_visit",
                "last_touch",
                "commercial",
              ]
            : [],
      });
    }
    if (method === "GET" && /^\/people\/[^/]+\/brief$/.test(path)) {
      return json({
        person_id: "p-anna",
        generated_at: "2026-06-20T09:00:00Z",
        generated_by: { kind: "agent", agent: "brief" },
        sentences: [],
      });
    }
    if (method === "GET" && /^\/people\/[^/]+\/consent\/guard$/.test(path)) {
      return json({ entries: [] });
    }
    if (path === "/people/p-anna/consent" && method === "GET") {
      return json({ state: [], events: [] });
    }
    if (path === "/organizations" && method === "GET") {
      return json(page([brandt]));
    }
    if (path === "/organizations/o-brandt") {
      return json(brandt);
    }
    if (path === "/leads" && method === "GET") {
      return json(page([seededLead]));
    }
    if (path === "/leads/l-1" && method === "GET") {
      return json(seededLead);
    }
    // The administered lead vocabularies and the lead-handling posture, as a
    // fresh installation ships them.
    if (path === "/lead-sources" && method === "GET") {
      return json({
        data: [
          ["manual", "Created manually", "neutral"],
          ["inbound", "Inbound", "high"],
          ["webform", "Web form", "high"],
          ["referral", "Referral", "high"],
          ["import", "Import", "low"],
          ["crawl", "Web research", "low"],
        ].map(([key, label, intent], i) => ({
          id: `src-${key}`,
          key,
          label,
          intent,
          sort_order: (i + 1) * 10,
          active: true,
          system: true,
          lead_count: 0,
          version: 1,
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
        })),
        discovered: [],
      });
    }
    if (path === "/lead-disqualify-reasons" && method === "GET") {
      return json({
        data: ["Not a good fit", "Bad timing", "No budget"].map((label, i) => ({
          id: `reason-${i + 1}`,
          label,
          sort_order: (i + 1) * 10,
          active: true,
          system: true,
          lead_count: 0,
          version: 1,
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
        })),
      });
    }
    if (path === "/leads/settings" && method === "GET") {
      return json({
        first_response_enabled: false,
        first_response_target_minutes: 240,
      });
    }
    if (path === "/leads/l-1" && method === "PATCH") {
      const body = route.request().postDataJSON();
      return json({ ...seededLead, ...body, version: seededLead.version + 1 });
    }
    if (path === "/leads/l-1/score") {
      return json({ score: seededLead.score, explained: false });
    }
    if (path === "/leads/l-1/promote-preview") {
      return json({ outcome: "create" });
    }
    if (path === "/leads/l-1/promote" && method === "POST") {
      return json({
        person: {
          ...anna,
          id: "p-new",
          full_name: "Jonas Petersen",
          converted_from_lead_id: "l-1",
        },
        merged: false,
        lead_id: "l-1",
      });
    }
    if (path === "/users") {
      return json(
        page([
          {
            id: "u1",
            email: "lena@seed.test",
            display_name: "Lena Fischer",
            status: "active",
            is_agent: false,
          },
        ]),
      );
    }
    if (path === "/pipelines") {
      return json(
        page([
          {
            id: "pl",
            workspace_id: "w",
            name: "Sales",
            is_default: true,
            position: 0,
            stages,
          },
        ]),
      );
    }
    if (path === "/deals" && method === "GET") {
      // The LIST reflects the writes too. A detail read that shows the advance
      // while the list still shows the old stage is a fixture disagreeing with
      // itself, and the board reads the list.
      return json(
        page(deals.map((deal) => ({ ...deal, ...dealPatches[deal.id] }))),
      );
    }
    const advanceRoute = /^\/deals\/([^/]+)\/advance$/.exec(path);
    if (advanceRoute && method === "POST") {
      // A write is REMEMBERED, not just answered. This handler used to return a
      // moved stage and version while every later read still served the seed, so
      // a re-read after an advance handed back the version the write had already
      // superseded — which the client would then send as its next precondition.
      //
      // And it ENFORCES the precondition rather than accepting anything, the way
      // the lead PATCH above already does: a UI that forgets If-Match, or sends a
      // version the row has moved past, fails this harness loudly instead of
      // quietly succeeding. That is the whole point of a fixture standing in for
      // a server that would refuse.
      const target = deals.find((deal) => deal.id === advanceRoute[1]);
      if (!target) {
        return json({ title: "Not Found" }, 404);
      }
      const current = dealPatches[target.id]?.version ?? target.version;
      if (route.request().headers()["if-match"] !== String(current)) {
        return json(
          {
            title: "Conflict",
            detail: "version skew — reload and retry",
            code: "version_skew",
          },
          409,
        );
      }
      // A win needs evidence, exactly as deals/deal_advance.go's
      // ensureWinEvidence demands it: a signed contract (none is seeded) or a
      // stated reason. Refused with the same field code, so the dialog's
      // "How was it won?" branch is the path a spec has to walk.
      const advance = route.request().postDataJSON();
      if (advance.status === "won" && !advance.won_without_contract_reason) {
        return json(
          {
            title: "Unprocessable",
            status: 422,
            code: "validation_error",
            details: {
              errors: [
                {
                  field: "won_without_contract_reason",
                  code: "win_evidence_required",
                  message:
                    "a won deal needs a signed contract with its paper attached, or a reason why there is none",
                },
              ],
            },
          },
          422,
        );
      }
      dealPatches[target.id] = {
        ...dealPatches[target.id],
        stage_id: "s4",
        status: "won",
        won_without_contract_reason:
          advance.won_without_contract_reason ?? null,
        version: current + 1,
      };
      // The win starts the delivery it was sold for, in the same write.
      projectState.startDelivery(
        dealPatches[target.id]?.project_id ?? target.project_id,
      );
      return json({ ...target, ...dealPatches[target.id] });
    }
    if (path.startsWith("/deals/") && path.endsWith("/stakeholders")) {
      return json(page([]));
    }
    if (path.startsWith("/deals/")) {
      const base = deals.find((deal) => path.endsWith(deal.id)) ?? deals[0];
      return json({ ...base, ...dealPatches[base.id] });
    }
    if (path === "/brief" && method === "GET") {
      return json(brief);
    }
    if (path === "/brief" && method === "POST") {
      return json(brief, 201);
    }
    const briefMark = /^\/brief\/items\/([^/]+)\/(act|dismiss)$/.exec(path);
    if (briefMark && method === "POST") {
      const item = brief.items.find((entry) => entry.id === briefMark[1]);
      if (!item) {
        return json({ title: "Not Found" }, 404);
      }
      item.state = briefMark[2] === "act" ? "acted" : "dismissed";
      item.state_at = "2026-07-05T06:00:00Z";
      return json(item);
    }
    if (path === "/approvals") {
      return json(page([approval]));
    }
    if (path.startsWith("/approvals/") && method === "POST") {
      return json({ ...approval, status: "approved" });
    }
    if (path === "/activities" && method === "POST") {
      const body = route.request().postDataJSON();
      const links: { entity_type: string; entity_id: string }[] =
        body.links ?? [];
      const created = {
        id: "act-new",
        workspace_id: "w",
        kind: body.kind ?? "note",
        subject: body.subject ?? "",
        body: body.body ?? null,
        occurred_at: "2026-07-06T09:00:00Z",
        thread_key: null,
        links,
        source: "manual",
        captured_by: "human:u1",
        version: 1,
        created_at: "2026-07-06T09:00:00Z",
        updated_at: "2026-07-06T09:00:00Z",
      };
      if (
        links.some((link) => ["deal", "project"].includes(link.entity_type))
      ) {
        created.id = `act-new-${loggedActivities.length + 1}`;
        loggedActivities.push(created);
      }
      return json(created, 201);
    }
    const relinkRoute = /^\/activities\/([^/]+)\/relink$/.exec(path);
    if (relinkRoute && method === "POST") {
      // Idempotent like the real one: the same link twice is one link.
      const activity = loggedActivities.find(
        (row) => row.id === relinkRoute[1],
      );
      if (!activity) {
        return json({ title: "Not Found", status: 404 }, 404);
      }
      const body = route.request().postDataJSON();
      if (body.replace_existing_of_type) {
        activity.links = activity.links.filter(
          (link) => link.entity_type !== body.entity_type,
        );
      }
      if (
        !activity.links.some(
          (link) =>
            link.entity_type === body.entity_type &&
            link.entity_id === body.entity_id,
        )
      ) {
        activity.links.push({
          entity_type: body.entity_type,
          entity_id: body.entity_id,
        });
      }
      return json(activity);
    }
    if (path === "/activities") {
      const entityType = url.searchParams.get("entity_type");
      const entityId = url.searchParams.get("entity_id");
      if (entityType === "deal" || entityType === "project") {
        return json(
          page(
            loggedActivities.filter((row) =>
              row.links.some(
                (link) =>
                  link.entity_type === entityType &&
                  link.entity_id === entityId,
              ),
            ),
          ),
        );
      }
      return json(page([]));
    }
    if (path === "/consent-purposes") {
      return json(
        page([
          {
            id: "cp1",
            workspace_id: "w",
            key: "marketing_email",
            label: "Marketing",
            requires_double_opt_in: true,
            created_at: "2026-06-01T00:00:00Z",
          },
        ]),
      );
    }
    if (path === "/data-subject-requests") {
      return json(page([]));
    }
    if (path === "/passports" && method === "GET") {
      return json({ data: passports });
    }
    if (path === "/audit-log") {
      const actor = url.searchParams.get("actor");
      const entityType = url.searchParams.get("entity_type");
      const action = url.searchParams.get("action");
      const cursor = url.searchParams.get("cursor");
      const rows = auditEntries.filter(
        (entry) =>
          (!actor || entry.actor_id === actor) &&
          (!entityType || entry.entity_type === entityType) &&
          (!action || entry.action === action),
      );
      if (!cursor && rows.length > 2) {
        return json({ data: rows.slice(0, 2), page: { next_cursor: "c1" } });
      }
      return json({
        data: cursor ? rows.slice(2) : rows,
        page: { next_cursor: null },
      });
    }
    if (path === "/automations/catalog") {
      return json({ data: automationCatalog });
    }
    if (path === "/automations" && method === "GET") {
      return json(page(automations));
    }
    if (path === "/automations" && method === "POST") {
      const body = route.request().postDataJSON();
      const created = {
        id: `au-${automations.length + 1}`,
        key: String(body.key),
        name: String(body.name),
        params: body.params,
        status: "paused",
        version: 1,
        created_at: "2026-07-05T08:00:00Z",
      };
      automations = [...automations, created];
      return json(created, 201);
    }
    if (path.startsWith("/automations/")) {
      const id = path.slice("/automations/".length);
      const existing = automations.find((entry) => entry.id === id);
      if (!existing) {
        return json({ title: "Not Found" }, 404);
      }
      if (method === "PATCH") {
        // the contract's optimistic lock: a PATCH without the row's
        // version is a version-skew conflict, so a UI that forgets
        // If-Match fails this harness loudly
        const ifMatch = route.request().headers()["if-match"];
        if (ifMatch !== String(existing.version)) {
          return json(
            {
              title: "Conflict",
              detail: "version skew — reload and retry",
              code: "version_skew",
            },
            409,
          );
        }
        const body = route.request().postDataJSON();
        if (typeof body.name === "string") {
          existing.name = body.name;
        }
        if (body.params) {
          existing.params = body.params;
        }
        if (body.status === "enabled" || body.status === "paused") {
          existing.status = body.status;
        }
        existing.version += 1;
        return json(existing);
      }
      if (method === "DELETE") {
        automations = automations.filter((entry) => entry.id !== id);
        return route.fulfill({ status: 204 });
      }
      return json(existing);
    }
    if (path === "/public/booking/host-1/availability") {
      return json({ slots: publicSlots });
    }
    if (path === "/public/booking/host-1" && method === "POST") {
      const body = route.request().postDataJSON();
      if (!body?.consent?.purpose_id || !body?.consent?.policy_version) {
        return json(
          {
            title: "Unprocessable",
            detail: "consent is mandatory on the public capture surface",
          },
          422,
        );
      }
      if (body.start === publicSlots[1].start) {
        return json(
          {
            title: "Conflict",
            detail: "slot no longer available",
            code: "slot_taken",
          },
          409,
        );
      }
      return json({ start: body.start, end: body.end }, 201);
    }
    if (path === "/availability") {
      return json({
        slots: [
          { start: "2026-07-06T09:00:00Z", end: "2026-07-06T09:30:00Z" },
          { start: "2026-07-06T10:00:00Z", end: "2026-07-06T10:30:00Z" },
        ],
      });
    }
    if (path === "/search") {
      // Cross-object: the relink picker searches projects by name alongside
      // the seeded person, so a spec can file an activity under one.
      const q = (url.searchParams.get("q") ?? "").toLowerCase();
      const projectHits = projectState.projects
        .filter((project) => project.name.toLowerCase().includes(q))
        .map((project) => ({
          type: "project",
          id: project.id,
          title: project.name,
          score: 0.8,
        }));
      return json(
        page([
          { type: "person", id: "p-anna", title: "Anna Weber", score: 0.9 },
          ...projectHits,
        ]),
      );
    }
    if (path.startsWith("/reports/")) {
      return json({
        report: "deals-by-stage",
        // The plan echoes what the CLIENT asked for, and the client groups money
        // by currency. A plan naming one dimension while the rows carry two
        // describes a request nobody made, and it would let a reader of this
        // fixture believe a single-dimension total is what the screen receives.
        plan: { group_by: ["stage_id", "currency"] },
        // The aliases are the REQUEST's, not this file's taste: the board asks
        // for `count as deals` and reads `row.deals`, so a row keyed
        // `deal_count` is a row it counts as nothing — every board column here
        // reported "0 deals" beside a real card. The weighted total is asked for
        // on the same request and is the server's own per-deal-rounded figure,
        // never the raw total scaled by the stage probability.
        columns: [
          "stage_id",
          "currency",
          "raw_minor",
          "weighted_minor",
          "deals",
        ],
        rows: [
          {
            stage_id: "s1",
            raw_minor: 1_250_000,
            weighted_minor: 250_000,
            deals: 1,
            currency: "EUR",
          },
          {
            stage_id: "s2",
            raw_minor: 4_800_000,
            weighted_minor: 1_920_000,
            deals: 1,
            currency: "EUR",
          },
          // A SECOND currency in a stage that already has one, because that is
          // the case the grouping exists for: the board must show this column's
          // count and refuse its total, and a fixture with one currency per
          // stage can never tell whether it does.
          {
            stage_id: "s2",
            raw_minor: 22_000_000_000,
            weighted_minor: 8_800_000_000,
            deals: 1,
            currency: "VND",
          },
        ],
      });
    }
    // Phase-3/4 reads the 360 fires: strength (P-4), partner (P-6), roll-up
    // (P-7). Without these the catch-all's list-envelope shape reaches a
    // record card that expects an entity, so mock them explicitly.
    if (path.endsWith("/strength")) {
      return json({
        score: 0,
        bucket: "dormant",
        factors: { recency: 0, frequency: 0, reciprocity: 0, direction: 0 },
        inbound_90d: 0,
        outbound_90d: 0,
        last_interaction: null,
        contributing_activity_ids: [],
        computed_at: "2026-07-13T00:00:00Z",
      });
    }
    if (path.endsWith("/partner") && method === "GET") {
      return json({ code: "not_found", title: "no partner" }, 404);
    }
    if (path.endsWith("/hierarchy-rollup")) {
      return json({
        root_id: "o-brandt",
        scope: "tree",
        weighted_pipeline: { amount_minor: 0, currency: "EUR" },
        closed_won: { amount_minor: 0, currency: "EUR" },
        activity_count_30d: 0,
        aggregated_account_count: 1,
        restricted_excluded: [],
        computed_at: "2026-07-13T00:00:00Z",
      });
    }
    // RS-3's context panel and the IT-1 tool console both read fixed-shape
    // envelopes the list catch-all below doesn't produce (`{sections:[]}`,
    // `{data:[AgentTool]}` vs `{data:[],page}`) — mock them explicitly so a
    // 360 open or the tool console doesn't crash on an undefined field.
    if (path.includes("/context")) {
      return json({ anchor: { type: "person", id: "x" }, sections: [] });
    }
    // The home digest card (CAP-WIRE-6): a MorningDigest, not the list
    // envelope — the generic fallthrough below would 200 a page shape the
    // card destructures and crashes on.
    if (path === "/digest") {
      return json({
        date: "2026-07-13",
        generated_at: "2026-07-13T05:00:00Z",
        capture: {
          messages_synced: 24,
          activities_created: 18,
          people_created: 3,
          organizations_created: 1,
        },
        review: {
          dedupe_open: 2,
          approvals_pending: 1,
          classify: { commitments: 4, meetings: 2, noise: 9 },
        },
        connectors: [
          {
            provider: "gmail",
            status: "connected",
            last_synced_at: "2026-07-13T04:55:00Z",
            last_sync_error_class: null,
          },
        ],
      });
    }
    if (path === "/ai/usage") {
      return json(aiUsage);
    }
    if (path === "/ai/calls" && method === "GET") {
      return json(aiCalls);
    }
    if (path === "/admin/job-health") {
      return json(jobHealth);
    }
    if (path === "/agent-tools") {
      return json({
        data: [
          {
            name: "search_records",
            required_scope: "read",
            tier: "auto_execute",
            egress: false,
          },
          {
            name: "send_email",
            required_scope: "send",
            tier: "confirmation_required",
            egress: true,
          },
        ],
      });
    }
    return json(page([]));
  });
}
