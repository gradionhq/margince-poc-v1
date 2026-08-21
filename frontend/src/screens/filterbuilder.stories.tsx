// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { useState } from "react";
import { FilterBuilder } from "./filterbuilder";
import type { VocabularyField } from "./filterdata";
import { type Node, newGroup, newLeaf } from "./segmentpredicate";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// The predicate builder: a tree of clauses, each offering only what the server's
// vocabulary admits for the field it names.
//
// What these stories document is the VALUE control, because it is the part whose
// right answer depends on the field. An operand is a record picker, a date, a
// two-way choice or a box, and picking wrong makes a field either unusable or
// silently wrong — a uuid box nobody can fill, or a free-text box over a closed
// set that answers "0 match" to a typo.
//
// There is no story for the OPEN listbox: Select portals it outside the story
// root, so the capture is indistinguishable from the closed state and would
// document nothing. That interaction is covered in filterbuilder.test.tsx, which
// can reach the portal.
const meta: Meta<typeof FilterBuilder> = {
  title: "Screens/Filter builder",
  component: FilterBuilder,
  parameters: { layout: "padded" },
};
export default meta;

const FIELDS: VocabularyField[] = [
  {
    name: "owner_id",
    type: "id",
    operators: ["eq", "neq", "in", "exists"],
    custom: false,
    references: "app_user",
  },
  {
    // Unbounded: a workspace has as many accounts as customers, so this one
    // keeps a plain box rather than a dropdown that could never be complete.
    name: "organization_id",
    type: "id",
    operators: ["eq", "neq", "in", "exists"],
    custom: false,
    references: "organization",
  },
  {
    name: "full_name",
    type: "text",
    operators: ["eq", "neq", "in", "contains", "exists"],
    custom: false,
  },
  {
    name: "created_at",
    type: "date",
    operators: ["eq", "neq", "gt", "gte", "lt", "lte", "exists"],
    custom: false,
  },
  {
    name: "cf_loyalty_tier",
    type: "picklist",
    operators: ["eq", "neq", "in", "exists"],
    custom: true,
  },
];

// The seat roster is a keyset walk, shared with every other picker in the
// product. `nextCursor` is what says whether the story's list is the whole
// workspace: null ends the walk, and a cursor the server never stops offering
// makes it stop at its page budget instead, which the clause has to disclose.
function routes(nextCursor: string | null = null): void {
  installFetchStub({
    "GET /users": () =>
      jsonResponse({
        data: [
          { id: "u-1", display_name: "Ann Lee" },
          { id: "u-2", display_name: "Bruno Sá" },
        ],
        page: { next_cursor: nextCursor, has_more: nextCursor !== null },
      }),
  });
}

/** Controlled, because the builder answers edits rather than holding them. */
function Editing({ start }: Readonly<{ start: Node }>) {
  const [tree, setTree] = useState<Node>(start);
  return <FilterBuilder tree={tree} onChange={setTree} fields={FIELDS} />;
}

function story(start: Node, nextCursor: string | null = null) {
  return () => {
    routes(nextCursor);
    return (
      <StoryProviders>
        <Editing start={start} />
      </StoryProviders>
    );
  };
}

type Story = StoryObj<typeof FilterBuilder>;

export const RecordPicker: Story = {
  // An id clause names a seat, chosen by name. The stored value is still the id.
  render: story(newGroup("and", [newLeaf("owner_id", "eq", "u-2")])),
};

export const RecordPickerOnAPartialRoster: Story = {
  // The clause is written against a list that stopped short of the workspace, so
  // it says so — under the picker, inside the value's own column, where it cannot
  // land between the value and the button that removes the clause.
  render: story(newGroup("and", [newLeaf("owner_id", "eq", "u-2")]), "next"),
};

export const UnboundedTargetKeepsABox: Story = {
  // The documented exception. A dropdown here could never be complete, and a
  // half-filled one would read as "this account does not exist".
  render: story(newGroup("and", [newLeaf("organization_id", "eq", "")])),
};

export const OperatorAnswersTheQuestion: Story = {
  // `exists` asks the whole question itself, so there is no operand to collect —
  // the two readings ARE the choice.
  render: story(newGroup("and", [newLeaf("owner_id", "exists", true)])),
};

export const NestedGroups: Story = {
  // ALL over ANY, which is the shape a real filter takes once it has more than
  // one idea in it.
  render: story(
    newGroup("and", [
      newLeaf("full_name", "contains", "ann"),
      newGroup("or", [
        newLeaf("cf_loyalty_tier", "eq", "gold"),
        newLeaf("created_at", "gt", "2026-01-01"),
      ]),
    ]),
  ),
};
