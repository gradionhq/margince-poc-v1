import type { Envelope } from "../types";

/**
 * One realistic `review_commitments` answer, shaped exactly as the tool seals
 * it.
 *
 * MIRRORED BY HAND from agents.ReviewCommitmentsResult / agents.CommitmentItem
 * — see the account brief's fixture for why that is hand work rather than
 * generated, and issue 808 for the gate that does not exist yet.
 *
 * The four items are the four states this view has to keep apart: badly
 * overdue, overdue by less than a day, upcoming, and a promise nobody dated.
 * The third one carries NO assignee, which is the state the panel exists to
 * surface — a promise with no owner.
 */
export const commitmentsFixture: Envelope = {
  data: {
    as_of: "2026-06-10T12:00:00Z",
    commitments: [
      {
        task_id: "2f1c9d64-5a3b-4e17-9c8d-0b1a2c3d4e5f",
        subject: "Send the signed SOW to Acme",
        due_at: "2026-06-03T09:00:00Z",
        state: "overdue",
        days_overdue: 7,
        assignee_id: "0f8fad5b-d9cb-469f-a165-70867728950e",
        assignee_name: "Dana Okafor",
        about: [
          {
            entity_type: "deal",
            entity_id: "9b2ffd94-0a1c-4b73-8e5d-6f7a8b9c0d1e",
            name: "Acme ERP licence",
          },
        ],
      },
      {
        task_id: "3a2b1c0d-9e8f-4a7b-8c6d-5e4f3a2b1c0d",
        subject: "Confirm the security review date",
        due_at: "2026-06-10T06:00:00Z",
        state: "overdue",
        // Zero whole days is a real answer: hours past its date. The view
        // renders it as "overdue today" rather than as "0d overdue".
        days_overdue: 0,
        assignee_id: "7c3e2f1a-5b6d-4c8e-9f01-2a3b4c5d6e7f",
        assignee_name: "Ravi Bhatt",
        about: [
          {
            entity_type: "organization",
            entity_id: "1d2e3f4a-5b6c-4d7e-8f90-a1b2c3d4e5f6",
            name: "Acme GmbH",
          },
        ],
      },
      {
        task_id: "4b3c2d1e-0f9a-4b8c-9d7e-6f5a4b3c2d1e",
        subject: "Draft the kickoff agenda",
        due_at: "2026-06-14T08:00:00Z",
        state: "upcoming",
        // No assignee_id and no assignee_name: nobody owns this promise.
        about: [
          {
            entity_type: "project",
            entity_id: "5c4d3e2f-1a0b-4c9d-8e7f-6a5b4c3d2e1f",
            name: "Acme ERP rollout",
          },
        ],
      },
      {
        task_id: "6d5e4f3a-2b1c-4d0e-9f8a-7b6c5d4e3f2a",
        subject: "Chase the reference call",
        state: "undated",
        assignee_id: "0f8fad5b-d9cb-469f-a165-70867728950e",
        assignee_name: "Dana Okafor",
        about: [],
      },
    ],
  },
  warnings: [],
};
