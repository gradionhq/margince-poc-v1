// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import type { components } from "../api/schema";
import { ListAction, NewDealAction, TagAction } from "./companyactions";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";

// The three verbs the company page hands the rep directly, so none of them
// is a click-through to another screen: open a deal on this account, tag it,
// or add it to a list. All three share CreateAction's button+modal shape
// (create.tsx), so the states worth a story are the same shape too: the
// resting button, the open form, mid-submit, and a failed submit. None of
// these controls reads a capability (no useCan anywhere in companyactions.tsx
// or create.tsx) — there is no read-only or permission-denied variant to
// cover, unlike companyraildetails' Archived story.
//
// The session still has to be stated, for a reason that is not object RBAC:
// CreateAction reads the workspace's system-of-record mode off /me and renders
// NOTHING on a mirrored screen in overlay mode (companies, deals — all three
// controls here). Every story below is a NATIVE workspace, which is what
// meFixture answers by default. The grants each one names are the ones its
// flow actually spends, so the session reads as the rep the story is about.

type Pipeline = components["schemas"]["Pipeline"];
type Tag = components["schemas"]["Tag"];
type List = components["schemas"]["List"];

const page = { has_more: false, next_cursor: null };

const meta: Meta = {
  title: "Screens/Company actions",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

// A default pipeline with two OPEN stages — populated enough that the deal
// form's stage select shows a real choice, not a single unavoidable option.
const openPipeline: Pipeline = {
  id: "p-1",
  name: "Sales",
  is_default: true,
  position: 0,
  stages: [
    {
      id: "s-1",
      pipeline_id: "p-1",
      name: "Qualifying",
      position: 0,
      semantic: "open",
      win_probability: 20,
    },
    {
      id: "s-2",
      pipeline_id: "p-1",
      name: "Proposal",
      position: 1,
      semantic: "open",
      win_probability: 60,
    },
  ],
};

// A pipeline whose every stage is already won or lost — nowhere for a new
// deal to land, which is why NewDealAction renders nothing rather than a
// button that can only fail (see the docblock on the component itself).
const closedPipeline: Pipeline = {
  id: "p-2",
  name: "Sales",
  is_default: true,
  position: 0,
  stages: [
    {
      id: "s-3",
      pipeline_id: "p-2",
      name: "Won",
      position: 0,
      semantic: "won",
      win_probability: 100,
    },
    {
      id: "s-4",
      pipeline_id: "p-2",
      name: "Lost",
      position: 1,
      semantic: "lost",
      win_probability: 0,
    },
  ],
};

function NewDeal({
  pipeline,
  submit,
}: Readonly<{
  pipeline: Pipeline;
  submit?: (body: unknown) => Response | Promise<Response>;
}>) {
  installFetchStub({
    // Where the deal may land, and the write that puts it there.
    "GET /me": meRoute({ pipeline: ["read"], deal: ["create"] }),
    "GET /pipelines": () => jsonResponse({ data: [pipeline], page }),
    ...(submit ? { "POST /deals": submit } : {}),
  });
  return (
    <StoryProviders>
      <NewDealAction orgId="o-1" orgName="Brandt Automotive GmbH" />
    </StoryProviders>
  );
}

// Resting: the button alone, before the rep has asked for anything.
export const NewDealResting: Story = {
  render: () => <NewDeal pipeline={openPipeline} />,
};

// No open stage anywhere in the default pipeline: the component returns
// null, so this story's canvas is intentionally empty — the honest render
// of "there is nowhere for this button to send a deal".
export const NewDealNoTarget: Story = {
  render: () => <NewDeal pipeline={closedPipeline} />,
};

// Open: the stage select carries both open stages, currency defaults to the
// first option, and the deal name is still blank — Save stays disabled on
// the one field the form cannot default.
//
// The trigger lives in canvasElement, but CreateAction's modal is Modal
// (design-system/atoms), which portals to document.body rather than
// rendering in place (so a dialog opened from a collapsed menu survives the
// menu's own collapse): every query for something the modal shows has to
// scope past canvasElement to the document it sits in, or it never sees the
// form at all.
export const NewDealOpen: Story = {
  render: () => <NewDeal pipeline={openPipeline} />,
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const body = within(canvasElement.ownerDocument.body);
    await userEvent.click(
      await canvas.findByRole("button", { name: "New deal" }),
    );
    await body.findByRole("textbox", { name: "Deal name" });
  },
};

// Pending: the deal name is filled and Save clicked, and the create endpoint
// never resolves — the form freezes on "Creating…" the way a slow request
// actually looks, rather than a state nobody could otherwise catch on screen.
export const NewDealPending: Story = {
  render: () => (
    <NewDeal pipeline={openPipeline} submit={() => new Promise(() => {})} />
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const body = within(canvasElement.ownerDocument.body);
    await userEvent.click(
      await canvas.findByRole("button", { name: "New deal" }),
    );
    await userEvent.type(
      await body.findByRole("textbox", { name: "Deal name" }),
      "Fleet renewal",
    );
    await userEvent.click(body.getByRole("button", { name: "Create" }));
    await body.findByRole("button", { name: "Creating…" });
  },
};

// Failed: the server's own refusal (amount and currency travel together or
// not at all) renders verbatim under the form, per problemMessageOf's rule
// that the server's words are never replaced from here.
export const NewDealFailed: Story = {
  render: () => (
    <NewDeal
      pipeline={openPipeline}
      submit={() =>
        jsonResponse(
          {
            code: "amount_currency_pair",
            title: "Amount and currency must travel together",
            detail:
              "Amount and currency travel together or not at all on a deal.",
          },
          422,
        )
      }
    />
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const body = within(canvasElement.ownerDocument.body);
    await userEvent.click(
      await canvas.findByRole("button", { name: "New deal" }),
    );
    await userEvent.type(
      await body.findByRole("textbox", { name: "Deal name" }),
      "Fleet renewal",
    );
    await userEvent.click(body.getByRole("button", { name: "Create" }));
    await body.findByText(/travel together/);
  },
};

// The id the workspace mints for a name its catalog has never carried. The
// apply call is addressed to a RESOLVED tag, so a story that wants to answer
// it has to route the id the create handed back, not the contract's template.
const mintedTagId = "t-9";

function TagCompany({
  tags,
  apply,
}: Readonly<{
  tags: Tag[];
  apply?: (body: unknown) => Response | Promise<Response>;
}>) {
  installFetchStub({
    // Read the catalog, mint the tag a typed name has no match for, put it on
    // the company: three grants on the one object, all three spent per submit.
    "GET /me": meRoute({ tag: ["read", "create", "update"] }),
    "GET /tags": () => jsonResponse({ data: tags, page }),
    "POST /tags": () => jsonResponse({ id: mintedTagId }, 201),
    ...(apply ? { [`POST /tags/${mintedTagId}/apply`]: apply } : {}),
  });
  return (
    <StoryProviders>
      <TagAction orgId="o-1" />
    </StoryProviders>
  );
}

// Resting: the workspace already has tags on file, but that catalog never
// surfaces in this control — it is a typed name, not a picker, matching to
// an existing tag underneath rather than presenting one on screen.
export const TagResting: Story = {
  render: () => (
    <TagCompany
      tags={[
        { id: "t-1", name: "VIP" },
        { id: "t-2", name: "Churn risk" },
      ]}
    />
  ),
};

// Open: one field, the name the rep types — a fresh workspace with no tags
// yet renders identically, because the form never lists what already exists.
export const TagOpen: Story = {
  render: () => <TagCompany tags={[]} />,
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const body = within(canvasElement.ownerDocument.body);
    await userEvent.click(
      await canvas.findByRole("button", { name: "Add tag" }),
    );
    await body.findByRole("textbox", { name: "Tag name" });
  },
};

// Pending: the name is typed, Save is clicked, and the apply call never
// resolves.
export const TagPending: Story = {
  render: () => <TagCompany tags={[]} apply={() => new Promise(() => {})} />,
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const body = within(canvasElement.ownerDocument.body);
    await userEvent.click(
      await canvas.findByRole("button", { name: "Add tag" }),
    );
    await userEvent.type(
      await body.findByRole("textbox", { name: "Tag name" }),
      "VIP",
    );
    await userEvent.click(body.getByRole("button", { name: "Create" }));
    await body.findByRole("button", { name: "Creating…" });
  },
};

// Failed: the apply call refuses for a reason that is not "already applied"
// (that case is folded into success — see resolveTagId's own docblock), so
// the server's refusal is the one thing left to show. The session holds
// tag:update, and it is meant to: the refusal is about THIS account, which is
// row scope, and no grant a client can read predicts it.
export const TagFailed: Story = {
  render: () => (
    <TagCompany
      tags={[]}
      apply={() =>
        jsonResponse(
          {
            code: "forbidden",
            title: "Not permitted",
            detail: "You cannot tag this account.",
          },
          403,
        )
      }
    />
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const body = within(canvasElement.ownerDocument.body);
    await userEvent.click(
      await canvas.findByRole("button", { name: "Add tag" }),
    );
    await userEvent.type(
      await body.findByRole("textbox", { name: "Tag name" }),
      "VIP",
    );
    await userEvent.click(body.getByRole("button", { name: "Create" }));
    await body.findByText(/cannot tag this account/);
  },
};

// The list a name with no static match is made as — same reason as
// mintedTagId: membership is written against a resolved list.
const mintedListId = "l-9";

function ListCompany({
  lists,
  addMember,
}: Readonly<{
  lists: List[];
  addMember?: (body: unknown) => Response | Promise<Response>;
}>) {
  installFetchStub({
    // Match an existing static list, make one when nothing matches, add the
    // company to it — membership is an update on the list itself.
    "GET /me": meRoute({ list: ["read", "create", "update"] }),
    "GET /lists": () => jsonResponse({ data: lists, page }),
    "POST /lists": () => jsonResponse({ id: mintedListId }, 201),
    ...(addMember
      ? { [`POST /lists/${mintedListId}/members`]: addMember }
      : {}),
  });
  return (
    <StoryProviders>
      <ListAction orgId="o-1" />
    </StoryProviders>
  );
}

// Resting: same one-field shape as TagAction, for the same reason (see
// ListAction's docblock) — only static lists are ever matched or made.
export const ListResting: Story = {
  render: () => (
    <ListCompany
      lists={[
        {
          id: "l-1",
          name: "Renewal Q3",
          entity_type: "organization",
          list_type: "static",
        },
      ]}
    />
  ),
};

export const ListOpen: Story = {
  render: () => <ListCompany lists={[]} />,
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const body = within(canvasElement.ownerDocument.body);
    await userEvent.click(
      await canvas.findByRole("button", { name: "Add to list" }),
    );
    await body.findByRole("textbox", { name: "List name" });
  },
};

// Pending: the name is typed, Save is clicked, and the membership call never
// resolves.
export const ListPending: Story = {
  render: () => (
    <ListCompany lists={[]} addMember={() => new Promise(() => {})} />
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const body = within(canvasElement.ownerDocument.body);
    await userEvent.click(
      await canvas.findByRole("button", { name: "Add to list" }),
    );
    await userEvent.type(
      await body.findByRole("textbox", { name: "List name" }),
      "Renewal Q3",
    );
    await userEvent.click(body.getByRole("button", { name: "Create" }));
    await body.findByRole("button", { name: "Creating…" });
  },
};

// Failed: the membership write refuses for a reason other than "already a
// member" (that case is folded into success — see ListAction's own comment).
// The session holds list:update for the same reason TagFailed holds
// tag:update — the account is what the server refuses, not the verb.
export const ListFailed: Story = {
  render: () => (
    <ListCompany
      lists={[]}
      addMember={() =>
        jsonResponse(
          {
            code: "forbidden",
            title: "Not permitted",
            detail: "You cannot add this account to a list.",
          },
          403,
        )
      }
    />
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const body = within(canvasElement.ownerDocument.body);
    await userEvent.click(
      await canvas.findByRole("button", { name: "Add to list" }),
    );
    await userEvent.type(
      await body.findByRole("textbox", { name: "List name" }),
      "Renewal Q3",
    );
    await userEvent.click(body.getByRole("button", { name: "Create" }));
    await body.findByText(/cannot add this account/);
  },
};
