import type { Envelope } from "../types";

/**
 * One realistic `who_knows` answer, shaped exactly as the tool seals it.
 *
 * MIRRORED BY HAND from agents.WhoKnowsAnswer / agents.KnownColleague — see the
 * account brief's fixture for why that is hand work rather than generated.
 *
 * The third colleague carries NO `strength`, which is the case this view exists
 * to keep honest: the seam omits it when the band is "none", because never
 * having spoken is not a score of zero.
 */
export const relationshipMapFixture: Envelope = {
  data: {
    person_id: "7c9e6679-7425-40de-944b-e07fc1f90ae7",
    colleagues: [
      {
        user_id: "0f8fad5b-d9cb-469f-a165-70867728950e",
        display_name: "Dana Okafor",
        strength: 88,
        strength_bucket: "high",
        interactions_90d: 41,
      },
      {
        user_id: "7c3e2f1a-5b6d-4c8e-9f01-2a3b4c5d6e7f",
        display_name: "Ravi Bhatt",
        strength: 52,
        strength_bucket: "medium",
        interactions_90d: 9,
      },
      {
        user_id: "9b2ffd94-0a1c-4b73-8e5d-6f7a8b9c0d1e",
        display_name: "Mira Lindqvist",
        strength_bucket: "none",
        interactions_90d: 0,
      },
    ],
  },
  warnings: [],
};
