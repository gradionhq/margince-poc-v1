import type { Page } from "@playwright/test";

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
    status: "open",
    stalled: true,
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
    status: "open",
    stalled: false,
    source: "manual",
    captured_by: "human:u1",
    created_at: "2026-06-15T08:00:00Z",
    updated_at: "2026-06-20T08:00:00Z",
    last_activity_at: "2026-06-28T08:00:00Z",
  },
];

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

export const auditEntries = [
  {
    id: "al-1",
    workspace_id: "w",
    actor_type: "human",
    actor_id: "u1",
    action: "update",
    entity_type: "deal",
    entity_id: "d-fleet",
    occurred_at: "2026-07-05T07:00:00Z",
  },
  {
    id: "al-2",
    workspace_id: "w",
    actor_type: "agent",
    actor_id: "runner",
    passport_id: "pp-1",
    on_behalf_of: "u1",
    action: "send_email",
    entity_type: "activity",
    entity_id: null,
    occurred_at: "2026-07-05T06:00:00Z",
  },
  {
    id: "al-3",
    workspace_id: "w",
    actor_type: "connector",
    actor_id: "gmail",
    action: "create",
    entity_type: "person",
    entity_id: "p-anna",
    occurred_at: "2026-07-05T05:00:00Z",
  },
];

export const publicSlots = [
  { start: "2026-07-06T09:00:00Z", end: "2026-07-06T09:30:00Z" },
  { start: "2026-07-06T10:00:00Z", end: "2026-07-06T10:30:00Z" },
];

function page(data: unknown[]) {
  return { data, page: { next_cursor: null } };
}

export async function mockApi(target: Page): Promise<void> {
  if (process.env.BASE_URL) {
    return; // live-backend mode: no mocking
  }
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
  // per-page brief state so act/dismiss marks stick within a test
  const brief = {
    ...briefRun,
    items: briefRun.items.map((item) => ({ ...item })),
  };

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
      return json({
        user: { id: "u1", email: "lars@brandt.example", locale: "de-DE" },
        roles: ["admin"],
        teams: [],
      });
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
          amount_minor: body.amount_minor ?? null,
          currency: body.currency ?? "EUR",
          stage_id: String(body.stage_id),
        },
        201,
      );
    }
    if (path === "/people/p-anna") {
      return json(anna);
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
      return json(page([]));
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
      return json(page(deals));
    }
    if (path.startsWith("/deals/") && path.endsWith("/advance")) {
      return json({ ...deals[0], stage_id: "s4", status: "won" });
    }
    if (path.startsWith("/deals/") && path.endsWith("/stakeholders")) {
      return json(page([]));
    }
    if (path.startsWith("/deals/")) {
      return json(deals.find((deal) => path.endsWith(deal.id)) ?? deals[0]);
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
    if (path === "/activities") {
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
          (!actor || `${entry.actor_type}:${entry.actor_id}` === actor) &&
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
      return json(
        page([
          { type: "person", id: "p-anna", title: "Anna Weber", score: 0.9 },
        ]),
      );
    }
    if (path.startsWith("/reports/")) {
      return json({
        report: "deals-by-stage",
        plan: { group_by: ["stage_id"] },
        columns: ["stage_id", "raw_minor", "deal_count"],
        rows: [
          {
            stage_id: "s1",
            raw_minor: 1_250_000,
            deal_count: 1,
            currency: "EUR",
          },
          {
            stage_id: "s2",
            raw_minor: 4_800_000,
            deal_count: 1,
            currency: "EUR",
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
