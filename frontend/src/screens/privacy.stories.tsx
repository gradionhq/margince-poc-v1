// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { PrivacyInboxCard } from "./privacy";
import {
  installFetchStub,
  jsonResponse,
  type RouteMap,
  StoryProviders,
} from "./story-utils";

// The DSR inbox (the settings/privacy tab's PrivacyInboxCard): the G-2 open
// form, the G-9/case-work row expansion, and the single most destructive
// action in the product — fulfilling an erasure. Fixtures mirror
// privacy.test.tsx's DSRS shape exactly; the legal-hold 409 in particular
// carries no `retain_until` — the server's ErrConflict wraps a bare
// `legal_hold` boolean (erasure.go:86-93), never a retention date, and this
// story must not invent one.

const DSRS = {
  data: [
    {
      id: "d1",
      kind: "erasure",
      subject_ref: "8f3a-person-uuid",
      status: "open",
      due_at: "2026-08-01T00:00:00Z",
      created_at: "2026-07-01T00:00:00Z",
    },
    {
      id: "d2",
      kind: "access",
      subject_ref: "anna@acme.test",
      status: "fulfilled",
      resolution: "sent by post",
      due_at: "2026-07-12T00:00:00Z",
      created_at: "2026-06-01T00:00:00Z",
    },
  ],
  page: { next_cursor: null, has_more: false },
};

function inbox(routes: RouteMap) {
  return () => {
    installFetchStub(routes, () => jsonResponse(DSRS));
    return (
      <StoryProviders>
        <PrivacyInboxCard />
      </StoryProviders>
    );
  };
}

async function expandRow(canvasElement: HTMLElement, subjectRef: string) {
  const canvas = within(canvasElement);
  await userEvent.click(
    await canvas.findByRole("button", { name: new RegExp(subjectRef, "i") }),
  );
}

// The facet bar's "Fulfilled" filter button substring-matches /fulfil/i too —
// scope every row-only control lookup to the expanded row itself, same
// findDsrRow idiom privacy.test.tsx uses.
async function findRow(
  canvasElement: HTMLElement,
  subjectRef: string,
): Promise<HTMLElement> {
  const canvas = within(canvasElement);
  const [match] = await canvas.findAllByText(subjectRef);
  const row = match.closest(".dsr-row");
  if (!(row instanceof HTMLElement)) {
    throw new Error(`dsr row for "${subjectRef}" not found`);
  }
  return row;
}

const meta: Meta<typeof PrivacyInboxCard> = {
  title: "screens/privacy",
  component: PrivacyInboxCard,
};
export default meta;

type Story = StoryObj<typeof PrivacyInboxCard>;

// One open erasure + one fulfilled access request, collapsed.
export const Inbox: Story = {
  render: inbox({ "GET /data-subject-requests": () => jsonResponse(DSRS) }),
};

// The case-work panel for a still-open request: subject, assignee, and only
// the transitions the server's closed status machine would accept.
export const RowExpanded: Story = {
  render: inbox({ "GET /data-subject-requests": () => jsonResponse(DSRS) }),
  play: async ({ canvasElement }) => {
    await expandRow(canvasElement, "8f3a-person-uuid");
  },
};

// G-2: the inline open-request form (kind defaults to access — the
// free-text subject field, not the erasure RecordPicker).
export const NewRequestForm: Story = {
  render: inbox({ "GET /data-subject-requests": () => jsonResponse(DSRS) }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: /new request/i }),
    );
  },
};

// The typed-ERASE confirm modal for the destructive erasure fulfil —
// confirmVariant="danger" throughout, distinct from every routine transition.
export const ErasureConfirm: Story = {
  render: inbox({ "GET /data-subject-requests": () => jsonResponse(DSRS) }),
  play: async ({ canvasElement }) => {
    await expandRow(canvasElement, "8f3a-person-uuid");
    const canvas = within(canvasElement);
    await userEvent.type(
      await canvas.findByLabelText(/resolution/i),
      "verified",
    );
    const row = await findRow(canvasElement, "8f3a-person-uuid");
    await userEvent.click(within(row).getByRole("button", { name: /fulfil/i }));
    await userEvent.type(await canvas.findByLabelText(/type erase/i), "ERASE");
  },
};

// Art. 17(3)(b): a documented, lawful refusal — never a red toast. The wire
// shape is the real one (erasure.go's ErrConflict): {type, title, status:
// 409, code: "conflict", detail} — no retain_until, ever.
export const LegalHoldBlocked: Story = {
  render: inbox({
    "GET /data-subject-requests": () => jsonResponse(DSRS),
    "PATCH /data-subject-requests/d1": () =>
      jsonResponse(
        {
          type: "https://errors.gradion.com/conflict",
          title: "Conflict",
          status: 409,
          code: "conflict",
          detail: "erasing a person under legal hold: conflict",
        },
        409,
      ),
  }),
  play: async ({ canvasElement }) => {
    await expandRow(canvasElement, "8f3a-person-uuid");
    const canvas = within(canvasElement);
    await userEvent.type(
      await canvas.findByLabelText(/resolution/i),
      "verified",
    );
    const row = await findRow(canvasElement, "8f3a-person-uuid");
    await userEvent.click(within(row).getByRole("button", { name: /fulfil/i }));
    await userEvent.type(await canvas.findByLabelText(/type erase/i), "ERASE");
    await userEvent.click(
      canvas.getByRole("button", { name: /erase \+ suppress/i }),
    );
    await canvas.findByText(/legal hold/i);
  },
};

export const Forbidden: Story = {
  render: inbox({
    "GET /data-subject-requests": () =>
      jsonResponse(
        { title: "permission denied", status: 403, code: "permission_denied" },
        403,
      ),
  }),
};

export const Empty: Story = {
  render: inbox({
    "GET /data-subject-requests": () =>
      jsonResponse({ data: [], page: { next_cursor: null, has_more: false } }),
  }),
};
