// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { LeadScreen, LeadsScreen } from "./leads";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";

// LeadsScreen (list, accent-tinted "segregated" surface) and LeadScreen (its
// own 360 — never person.html, per the §3.5 segregation gap) both read
// through the api client on mount; LeadScreen's lifecycle panel also reads
// GET /me (the session-principal probe every role-aware surface shares).
const meta: Meta = {
  title: "Records/Leads",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

const lead = {
  id: "l-1",
  full_name: "Jonas Petersen",
  email: "jonas@nordwind.example",
  company_name: "Nordwind Logistik",
  status: "working" as const,
  score: 72,
  source: "manual",
  captured_by: "human:u1",
  version: 1,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

export const LeadsList: Story = {
  render: () => {
    installFetchStub({
      "GET /me": meRoute({ lead: ["read", "update"] }),
      "GET /leads": () =>
        jsonResponse({
          data: [lead],
          page: { next_cursor: null, has_more: false },
        }),
    });
    return (
      <StoryProviders>
        <LeadsScreen />
      </StoryProviders>
    );
  },
};

export const LeadOverview: Story = {
  render: () => {
    installFetchStub({
      "GET /leads/l-1": () => jsonResponse(lead),
      "GET /me": () =>
        jsonResponse({
          user: { id: "u-9", display_name: "Me" },
          roles: ["rep"],
          teams: [],
        }),
    });
    return (
      <StoryProviders>
        <LeadScreen id="l-1" />
      </StoryProviders>
    );
  },
};

// A lead that earns nothing, which is the common shape of a fresh one: the
// panel states the reasons rather than the score's storage history, so a 0
// stops reading as a bad prospect when it means an unassessed one
// (ADR-0108 §4).
export const LeadScoringZero: Story = {
  render: () => {
    installFetchStub({
      "GET /leads/l-1": () =>
        jsonResponse({ ...lead, score: 0, title: "Boss", source: "manual" }),
      "GET /leads/l-1/score": () =>
        jsonResponse({ score: 0, explained: false }),
      "GET /me": () =>
        jsonResponse({
          user: { id: "u-9", display_name: "Me" },
          roles: ["rep"],
          teams: [],
        }),
    });
    return (
      <StoryProviders>
        <LeadScreen id="l-1" />
      </StoryProviders>
    );
  },
};

// The score explained: factors with their points and the decay as arithmetic
// a reader can check, plus the line that reconciles them to the stored score.
export const LeadScoreExplained: Story = {
  render: () => {
    installFetchStub({
      "GET /leads/l-1": () => jsonResponse(lead),
      "GET /leads/l-1/score": () =>
        jsonResponse({
          score: 72,
          explained: true,
          current: {
            score: 72,
            score_computed: 72,
            raw_sum: 71.6,
            rounded_sum: 72,
            computed_at: "2026-06-04T00:00:00Z",
            factors: [
              { factor: "decision_maker_title", points: 15 },
              { factor: "high_intent_source", points: 8 },
              { factor: "reply", points: 22.6, base_points: 25 },
            ],
          },
        }),
      "GET /me": () =>
        jsonResponse({
          user: { id: "u-9", display_name: "Me" },
          roles: ["rep"],
          teams: [],
        }),
    });
    return (
      <StoryProviders>
        <LeadScreen id="l-1" />
      </StoryProviders>
    );
  },
};

// A disqualified lead keeps its controls, DISABLED with the reason — hiding
// them hid the fact the reader needed (STATE-4a).
export const LeadDisqualified: Story = {
  render: () => {
    installFetchStub({
      "GET /leads/l-1": () =>
        jsonResponse({
          ...lead,
          status: "disqualified",
          archived_at: "2026-07-13T00:00:00Z",
        }),
      "GET /leads/l-1/score": () =>
        jsonResponse({ score: 72, explained: false }),
      "GET /me": () =>
        jsonResponse({
          user: { id: "u-9", display_name: "Me" },
          roles: ["rep"],
          teams: [],
        }),
    });
    return (
      <StoryProviders>
        <LeadScreen id="l-1" />
      </StoryProviders>
    );
  },
};

// A PROMOTED lead keeps its page too (ADR-0119/A170), and leads with what the
// promotion did: which contact it became, and whether it merged into one we
// already knew or created a new one. The outcome is read from the promote
// audit row, so the story stubs that read as well as the lead itself.
export const LeadPromotedAfterMerge: Story = {
  render: () => {
    installFetchStub({
      "GET /leads/l-1": () =>
        jsonResponse({
          ...lead,
          status: "promoted",
          promoted_person_id: "p-42",
          promoted_at: "2026-06-20T08:00:00Z",
          archived_at: "2026-06-20T08:00:00Z",
        }),
      "GET /records/lead/l-1/history": () =>
        jsonResponse({
          data: [
            {
              id: "a-1",
              actor_type: "human",
              actor_id: "human:u-9",
              action: "promote",
              occurred_at: "2026-06-20T08:00:00Z",
              after: {
                dedupe_outcome: "merged",
                trigger: "inbound_reply",
                evidence_note: "Replied asking for a quote.",
              },
            },
          ],
          page: { next_cursor: null, has_more: false },
        }),
      "GET /leads/l-1/score": () =>
        jsonResponse({ score: 72, explained: false }),
      "GET /me": () =>
        jsonResponse({
          user: { id: "u-9", display_name: "Me" },
          roles: ["rep"],
          teams: [],
        }),
    });
    return (
      <StoryProviders>
        <LeadScreen id="l-1" />
      </StoryProviders>
    );
  },
};

// The board: the live leads in the two columns they can actually move between.
// Terminal statuses get no column — a lead is promoted or disqualified through
// its own audited verb, never by dragging a card.
export const LeadsBoard: Story = {
  render: () => {
    installFetchStub({
      "GET /leads": () =>
        jsonResponse({
          data: [
            { ...lead, id: "l-1", status: "new", score: 82 },
            {
              ...lead,
              id: "l-2",
              full_name: "Petra Vogel",
              company_name: "Südwind AG",
              status: "working",
              score: 54,
            },
          ],
          page: { next_cursor: null, has_more: false },
        }),
      "GET /me": () =>
        jsonResponse({
          user: { id: "u-9", display_name: "Me" },
          roles: ["rep"],
          teams: [],
        }),
    });
    return (
      <StoryProviders>
        <LeadsScreen />
      </StoryProviders>
    );
  },
};
