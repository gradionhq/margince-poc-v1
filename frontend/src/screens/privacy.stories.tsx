// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { screen, userEvent, within } from "storybook/test";
import { PrivacyInboxCard } from "./privacy";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
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

// The subject-request queue is the admin's alone (useHoldsAdminRole), so the
// session is what decides whether these stories show the queue at all: without
// it every one of them drew "this is admin only" under a name promising rows.
function inbox(routes: RouteMap) {
  return () => {
    installFetchStub({ "GET /me": meRoute({}), ...routes }, () =>
      jsonResponse(DSRS),
    );
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
  title: "Settings/Admin settings/Privacy/Subject requests",
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

// The narrow render of the row privacy.css's `.dsr-row-toggle` rule exists for:
// a kind badge, a mono subject reference, a status badge, a due date and an
// overdue badge are five nowrap children in one flex line, and before the wrap
// they pushed the card's scroll width past the phone viewport. That comment
// describes a fix no story has ever pictured. Expanded, because the case-work
// panel underneath adds the status SegmentedControl and the transition verbs to
// the same 390px column, and the facet bar above it is in frame either way.
export const RowExpandedPhone: Story = {
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
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

// Opens the fulfil confirm on the erasure row and types the word that arms it.
// The resolution field and the row's own Fulfil verb are canvas-scoped because
// they are in the card; the ERASE field is NOT — ConfirmModal portals to
// document.body, outside canvasElement, so that one lookup goes through `screen`.
// Looking for it on the canvas is why both stories below stopped at an un-armed
// dialog: the query rejected, the play aborted, and the capture showed a confirm
// nobody had confirmed. webhooks.stories.tsx carries the same note over its own
// clickTestIds for the same reason.
async function armErasureConfirm(canvasElement: HTMLElement) {
  await expandRow(canvasElement, "8f3a-person-uuid");
  const canvas = within(canvasElement);
  await userEvent.type(await canvas.findByLabelText(/resolution/i), "verified");
  const row = await findRow(canvasElement, "8f3a-person-uuid");
  await userEvent.click(within(row).getByRole("button", { name: /fulfil/i }));
  await userEvent.type(await screen.findByLabelText(/type erase/i), "ERASE");
}

// The typed-ERASE confirm modal for the destructive erasure fulfil —
// confirmVariant="danger" throughout, distinct from every routine transition.
export const ErasureConfirm: Story = {
  render: inbox({ "GET /data-subject-requests": () => jsonResponse(DSRS) }),
  play: async ({ canvasElement }) => {
    await armErasureConfirm(canvasElement);
  },
};

// Art. 17(3)(b): a documented, lawful refusal — never a red toast. The wire
// shape is the real one (erasure.go's ErrConflict): {type, title, status:
// 409, code: "conflict", detail} — no retain_until, ever.
const legalHoldRoutes: RouteMap = {
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
};

const driveToLegalHold = async ({
  canvasElement,
}: {
  canvasElement: HTMLElement;
}) => {
  await armErasureConfirm(canvasElement);
  // Portalled too, and the refusal it produces is portalled with it.
  await userEvent.click(
    screen.getByRole("button", { name: /erase \+ suppress/i }),
  );
  await screen.findByText(/legal hold/i);
};

export const LegalHoldBlocked: Story = {
  render: inbox(legalHoldRoutes),
  play: driveToLegalHold,
};

// The lawful refusal in dark. A `Callout tone="danger"` is a tinted surface, a
// border and body text that all have to stay separable — the tone IS the claim
// that this is a documented refusal and not a routine note, so if the danger
// surface flattens into the card behind it the refusal stops reading as one. The
// row underneath is still expanded, so the callout is judged against the panel,
// the transition verbs and the mono subject reference it interrupts.
export const LegalHoldBlockedDark: Story = {
  globals: { theme: "dark" },
  render: inbox(legalHoldRoutes),
  play: driveToLegalHold,
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
