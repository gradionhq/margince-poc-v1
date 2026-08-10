import type { Envelope } from "../types";

/**
 * One realistic `read_brief` answer, shaped exactly as the tool seals it.
 *
 * MIRRORED BY HAND from agents.ReadBriefResult / agents.BriefItem, because
 * nothing pins a view's expectation of `structuredContent.data` to what the tool
 * answers — `agents/outputshapes.go` never reaches TypeScript. That gap is filed
 * as a follow-up; until it closes, every member name here is a hand-checked copy
 * and a rename on the Go side will not fail this suite.
 */
export const accountBriefFixture: Envelope = {
  data: {
    brief_id: "3f2504e0-4f89-41d3-9a0c-0305e82c3301",
    generated_at: "2026-08-10T06:15:00Z",
    as_of: "2026-08-10T06:00:00Z",
    // Seven cleared the honest-short bar; two are queued. The difference is
    // what the ranking left out, and the view says so.
    candidate_count: 7,
    items: [
      {
        item_id: "16fd2706-8baf-433b-82eb-8c7fada847da",
        deal_id: "8f14e45f-ceea-467a-9a1a-2e9b0e4c3d21",
        rank: 1,
        composite: 0.82,
        factors: {
          winnability: 0.9,
          revenue: 0.7,
          timing: 0.85,
          momentum: 0.94,
          warmth: 0.61,
        },
        state: "new",
      },
      {
        item_id: "6fa459ea-ee8a-3ca4-894e-db77e160355e",
        deal_id: "c9f0f895-fb98-4b1b-9a5b-1d3f2e6a7c04",
        rank: 2,
        composite: 0.64,
        factors: {
          winnability: 0.5,
          revenue: 0.8,
          timing: 0.42,
          momentum: 0.66,
          warmth: 0.73,
        },
        state: "snoozed",
      },
    ],
  },
  warnings: [],
};
