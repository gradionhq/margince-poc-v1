import type { Envelope } from "../types";

/**
 * One realistic `whats_slipping_this_week` answer, shaped exactly as the tool
 * seals it.
 *
 * MIRRORED BY HAND from agents.WhatsSlippingResult / agents.SlippingDealItem —
 * see the account brief's fixture for why that is hand work rather than
 * generated, and issue 808 for the gate that does not exist yet.
 *
 * The third deal carries NO amount, which is a real state the view has to keep
 * honest: a deal can be worked before it is priced, and a blank amount
 * rendered as a currency zero would say it is worth nothing. The second one
 * carries two evidence lines, because a deal can be both quiet and late.
 */
export const pipelineReviewFixture: Envelope = {
  data: {
    deals: [
      {
        rank: 1,
        deal_id: "9b2ffd94-0a1c-4b73-8e5d-6f7a8b9c0d1e",
        name: "Acme ERP licence",
        amount_minor: 24_000_000,
        currency: "EUR",
        evidence: [
          {
            source: "deal.last_activity_at",
            snippet: "no recorded activity since 2026-04-28",
          },
        ],
      },
      {
        rank: 2,
        deal_id: "1d2e3f4a-5b6c-4d7e-8f90-a1b2c3d4e5f6",
        name: "Globex platform expansion",
        amount_minor: 9_500_000,
        currency: "EUR",
        evidence: [
          {
            source: "deal.last_activity_at",
            snippet: "no recorded activity since 2026-05-12",
          },
          {
            source: "deal.expected_close_date",
            snippet: "expected close 2026-05-31 is past due",
          },
        ],
      },
      {
        rank: 3,
        deal_id: "5c4d3e2f-1a0b-4c9d-8e7f-6a5b4c3d2e1f",
        name: "Initech pilot",
        evidence: [
          {
            source: "deal.created_at",
            snippet: "no recorded activity since 2026-05-20",
          },
        ],
      },
    ],
  },
  warnings: [],
};
