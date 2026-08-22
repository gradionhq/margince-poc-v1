import type { Route } from "@playwright/test";

// The project half of the network-edge mock: a stateful stand-in for the
// project endpoints, so the create → attach → win → deliver → close flow in
// projects.spec.ts runs against a server that REMEMBERS its writes and
// enforces the same preconditions the real one does. A generic 200 would let
// every screen report success and then reload the seed — the flow would pass
// having moved nothing.
//
// It also mirrors the one cross-record rule the flow depends on: winning a
// deal moves its project into `delivering` (deals/project_delivery.go), from
// initiative or pursuing only. The deal advance handler in seed.ts calls
// startDelivery for that.

type Json = (body: unknown, status?: number) => Promise<void>;

export type MockProject = {
  id: string;
  workspace_id: string;
  name: string;
  key: string | null;
  organization_id: string;
  owner_id: string | null;
  phase: "initiative" | "pursuing" | "delivering" | "closed";
  closed_reason: string | null;
  description: string | null;
  target_end_date: string | null;
  version: number;
  source: string;
  captured_by: string;
  created_at: string;
  updated_at: string;
  last_activity_at: string | null;
};

type MockDeal = {
  id: string;
  organization_id: string;
  project_id?: string | null;
  status: string;
  amount_minor: number | null;
  currency: string | null;
};

type MockActivity = {
  id: string;
  kind: string;
  subject: string;
  occurred_at: string;
  links: { entity_type: string; entity_id: string }[];
};

type Transition = {
  id: string;
  from_phase: string | null;
  to_phase: string;
  reason: string | null;
  changed_at: string;
  changed_by: { id: string; display_name: string | null };
};

const ACTOR = { id: "u1", display_name: "Lars Brandt" };
const NOW = "2026-07-06T09:00:00Z";

function page(data: unknown[]) {
  return { data, page: { next_cursor: null } };
}

export function projectMock(input: {
  seeded: MockProject;
  organizationName: string;
  deals: () => MockDeal[];
  activities: () => MockActivity[];
}) {
  const projects: MockProject[] = [{ ...input.seeded }];
  const history = new Map<string, Transition[]>();
  let minted = 0;

  const record = (
    project: MockProject,
    from: string | null,
    to: MockProject["phase"],
    reason: string | null,
  ) => {
    const rows = history.get(project.id) ?? [];
    rows.push({
      id: `ph-${project.id}-${rows.length + 1}`,
      from_phase: from,
      to_phase: to,
      reason,
      changed_at: NOW,
      changed_by: ACTOR,
    });
    history.set(project.id, rows);
    project.phase = to;
    project.closed_reason = to === "closed" ? reason : null;
    project.version += 1;
  };
  record(projects[0], null, projects[0].phase, null);
  projects[0].version = input.seeded.version;

  const find = (id: string) => projects.find((project) => project.id === id);

  const projectActivities = (id: string) =>
    input
      .activities()
      .filter((activity) =>
        activity.links.some(
          (link) => link.entity_type === "project" && link.entity_id === id,
        ),
      );

  const view = (project: MockProject) => {
    const deals = input
      .deals()
      .filter((deal) => deal.project_id === project.id);
    const sum = (status: string) =>
      deals
        .filter((deal) => deal.status === status)
        .reduce((total, deal) => total + (deal.amount_minor ?? 0), 0);
    const transitions = history.get(project.id) ?? [];
    const visited = [...new Set(transitions.map((row) => row.to_phase))];
    const filed = projectActivities(project.id);
    return {
      as_of: NOW,
      project,
      sections_omitted: [],
      organization: {
        id: project.organization_id,
        name: input.organizationName,
      },
      phase_history: {
        data: transitions,
        phase_durations: visited.map((phase) => ({
          phase,
          seconds: 0,
          current: phase === project.phase,
        })),
      },
      deals: page(deals),
      stakeholders: page([]),
      contracts: page([]),
      documents: page([]),
      commitments: page([]),
      activities: page(filed),
      coverage: {
        attributed: filed.length,
        unattributed_nearby: 0,
        awaiting_decision: 0,
      },
      rollups: {
        open_deal_value: { amount_minor: sum("open"), currency: "EUR" },
        won_deal_value: { amount_minor: sum("won"), currency: "EUR" },
        open_commitments: 0,
        last_activity_at: filed.at(-1)?.occurred_at ?? null,
        activity_count: filed.length,
      },
    };
  };

  return {
    projects,
    // The win's side effect: only from initiative or pursuing, never out of
    // closed, and a project already delivering is left as it is.
    startDelivery(projectId: string | null | undefined) {
      const project = projectId ? find(projectId) : undefined;
      if (
        project &&
        (project.phase === "initiative" || project.phase === "pursuing")
      ) {
        record(project, project.phase, "delivering", null);
      }
    },
    async handle(
      route: Route,
      path: string,
      method: string,
      json: Json,
    ): Promise<boolean> {
      if (path === "/projects" && method === "GET") {
        await json(page(projects));
        return true;
      }
      if (path === "/projects" && method === "POST") {
        const body = route.request().postDataJSON();
        minted += 1;
        const project: MockProject = {
          ...input.seeded,
          id: `pr-new-${minted}`,
          name: String(body.name),
          key: body.key ?? null,
          organization_id: String(body.organization_id),
          owner_id: body.owner_id ?? null,
          description: body.description ?? null,
          target_end_date: body.target_end_date ?? null,
          phase: "initiative",
          closed_reason: null,
          version: 0,
          last_activity_at: null,
        };
        projects.push(project);
        record(project, null, "initiative", null);
        await json(project, 201);
        return true;
      }
      const one = /^\/projects\/([^/]+)(\/360|\/advance|\/stakeholders)?$/.exec(
        path,
      );
      if (!one) {
        return false;
      }
      const project = find(one[1]);
      if (!project) {
        await json({ title: "Not Found", status: 404 }, 404);
        return true;
      }
      const current = String(project.version);
      const skew = () =>
        json({ title: "Conflict", code: "version_skew", status: 409 }, 409);
      if (one[2] === "/360") {
        await json(view(project));
      } else if (one[2] === "/stakeholders") {
        await json(page([]));
      } else if (one[2] === "/advance" && method === "POST") {
        if (route.request().headers()["if-match"] !== current) {
          await skew();
          return true;
        }
        const body = route.request().postDataJSON();
        const reason =
          typeof body.reason === "string" && body.reason.trim() !== ""
            ? body.reason
            : null;
        if (body.to_phase === "closed" && reason === null) {
          await json(
            {
              title: "Unprocessable",
              status: 422,
              code: "validation_error",
              errors: [{ field: "reason", code: "closed_reason_required" }],
            },
            422,
          );
          return true;
        }
        record(project, project.phase, body.to_phase, reason);
        await json(project);
      } else if (one[2] === undefined && method === "PATCH") {
        if (route.request().headers()["if-match"] !== current) {
          await skew();
          return true;
        }
        Object.assign(project, route.request().postDataJSON(), {
          version: project.version + 1,
        });
        await json(project);
      } else if (one[2] === undefined && method === "GET") {
        await json(project);
      } else {
        return false;
      }
      return true;
    },
  };
}
