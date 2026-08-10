import type { Envelope } from "../types";

/**
 * One realistic `prepare_handoff` answer, shaped exactly as the tool seals it.
 *
 * MIRRORED BY HAND from agents.PreparedHandoff — see the account brief's
 * fixture for why that is hand work rather than generated, and issue 808 for the
 * gate that does not exist yet.
 *
 * It is a handover that is NOT ready, which is the case worth drawing: two
 * gaps, an untitled seat, an unpriced won deal beside a priced one, and a
 * promise already past due at handover. A complete one renders as the empty
 * state, and the stories carry that too.
 */
export const handoffFixture: Envelope = {
  data: {
    project_id: "5c4d3e2f-1a0b-4c9d-8e7f-6a5b4c3d2e1f",
    name: "Acme ERP rollout",
    key: "ERP",
    phase: "delivering",
    organization_id: "1d2e3f4a-5b6c-4d7e-8f90-a1b2c3d4e5f6",
    // No owner_id and no owner_name: nobody is receiving the work, which is
    // the gap below.
    started_at: "2026-05-01T00:00:00Z",
    as_of: "2026-06-10T12:00:00Z",
    deals: [
      {
        deal_id: "9b2ffd94-0a1c-4b73-8e5d-6f7a8b9c0d1e",
        name: "Acme ERP licence",
        status: "won",
        amount_minor: 24_000_000,
        currency: "EUR",
      },
      {
        deal_id: "2f1c9d64-5a3b-4e17-9c8d-0b1a2c3d4e5f",
        name: "Acme ERP services",
        status: "won",
      },
    ],
    stakeholders: [
      {
        person_id: "7c9e6679-7425-40de-944b-e07fc1f90ae7",
        name: "Alice Müller",
        role: "Sponsor",
      },
      // Named but untitled: the seat exists and the person is readable, and
      // what nobody recorded is their part in the work.
      {
        person_id: "0f8fad5b-d9cb-469f-a165-70867728950e",
        name: "Bob Schmidt",
      },
    ],
    open_commitments: [
      {
        task_id: "3a2b1c0d-9e8f-4a7b-8c6d-5e4f3a2b1c0d",
        subject: "Hand over the security questionnaire",
        due_at: "2026-06-05T09:00:00Z",
        state: "overdue",
        days_overdue: 5,
        about: [],
      },
      {
        task_id: "4b3c2d1e-0f9a-4b8c-9d7e-6f5a4b3c2d1e",
        subject: "Book the kickoff",
        due_at: "2026-06-14T08:00:00Z",
        state: "upcoming",
        about: [],
      },
    ],
    gaps: [
      {
        code: "no_delivery_owner",
        source: "project.owner_id",
        message:
          "Nobody owns this project, so the handover has no receiving side.",
      },
      {
        code: "no_target_end_date",
        source: "project.target_end_date",
        message: "No target end date, so there is nothing to deliver against.",
      },
      {
        code: "stakeholder_role_unset",
        source: "relationship.role",
        message: "1 named contact has no recorded part in the work.",
      },
      {
        code: "unpriced_won_deal",
        source: "deal.amount_minor",
        message:
          "1 won deal carries no amount, so delivery cannot see what was sold.",
      },
      {
        code: "overdue_commitment",
        source: "activity.due_at",
        message: "1 commitment is already past due at handover.",
      },
    ],
  },
  warnings: [],
};
