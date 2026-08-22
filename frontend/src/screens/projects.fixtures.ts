// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { components } from "../api/schema";

// Fixtures for the project surfaces: one project and one 360, shared by the
// tests and the stories. No test runner in here, so a story can import it.
// The fetch stub the tests drive lives beside it in projects.testing.ts.

type Project = components["schemas"]["Project"];
type Project360 = components["schemas"]["Project360"];

export const ORG = { id: "o-1", display_name: "Brandt Automotive" };

export function project(overrides: Partial<Project> = {}): Project {
  return {
    id: "pr-1",
    name: "CRM rollout",
    key: "ACME-CRM",
    organization_id: ORG.id,
    owner_id: "u-me",
    phase: "initiative",
    description: null,
    started_at: null,
    target_end_date: null,
    ended_at: null,
    last_activity_at: "2026-07-01T09:00:00Z",
    source: "manual",
    captured_by: "u-me",
    version: 3,
    created_at: "2026-06-01T09:00:00Z",
    updated_at: "2026-07-01T09:00:00Z",
    archived_at: null,
    ...overrides,
  };
}

const emptySection = { data: [], page: { next_cursor: null, has_more: false } };

/** A 360 with every section present and most of them empty. */
export function project360(overrides: Partial<Project360> = {}): Project360 {
  return {
    as_of: "2026-07-02T10:00:00Z",
    project: project(),
    sections_omitted: [],
    organization: { id: ORG.id, name: ORG.display_name },
    phase_history: {
      data: [
        {
          id: "ph-1",
          from_phase: null,
          to_phase: "initiative",
          reason: null,
          changed_at: "2026-06-01T09:00:00Z",
          changed_by: { id: "u-me", display_name: "Me" },
        },
      ],
      phase_durations: [
        { phase: "initiative", seconds: 2_678_400, current: true },
      ],
    },
    deals: emptySection,
    stakeholders: {
      data: [
        {
          relationship_id: "rel-1",
          person_id: "p-1",
          person_name: "Anna Weber",
          role: "project_lead",
        },
      ],
      page: { next_cursor: null, has_more: false },
    },
    contracts: emptySection,
    documents: emptySection,
    commitments: emptySection,
    activities: emptySection,
    coverage: {
      attributed: 142,
      unattributed_nearby: 23,
      awaiting_decision: 8,
    },
    rollups: {
      open_deal_value: { amount_minor: 1_200_000, currency: "EUR" },
      won_deal_value: { amount_minor: 450_000, currency: "EUR" },
      open_commitments: 4,
      last_activity_at: "2026-07-01T09:00:00Z",
      activity_count: 142,
    },
    ...overrides,
  };
}
