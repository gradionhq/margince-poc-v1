// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import { ContactsScreen, PersonScreen } from "./people";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";

type Person = components["schemas"]["Person"];

// ContactsScreen (list) and PersonScreen (360 Overview) both read through
// the api client on mount — fixtures mirror people.test.tsx's `anna` +
// dormant-strength default (the Overview tab fires the strength GET
// unconditionally).
const meta: Meta = {
  title: "Records/People",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

const anna = {
  id: "p-1",
  full_name: "Anna Weber",
  title: "Head of Procurement",
  emails: [{ id: "e-1", email: "anna.weber@brandt.example", is_primary: true }],
  captured_by: "connector:gmail",
  source: "gmail",
  version: 1,
};

const dormantStrength = {
  score: 0,
  bucket: "dormant",
  factors: { recency: 0, frequency: 0, reciprocity: 0, direction: 0 },
  last_interaction: null,
};

export const ContactsList: Story = {
  render: () => {
    installFetchStub({
      "GET /me": meRoute({ person: ["read", "update"] }),
      "GET /people": () =>
        jsonResponse({
          data: [anna],
          page: { next_cursor: null, has_more: false },
        }),
    });
    return (
      <StoryProviders>
        <ContactsScreen />
      </StoryProviders>
    );
  },
};

// The empty list: no rows, the "unit.contacts" copy from ListSurface's
// generic empty branch (table.none), nothing else on the page to distract
// from it.
export const ContactsListEmpty: Story = {
  render: () => {
    installFetchStub({
      "GET /people": () =>
        jsonResponse({
          data: [],
          page: { next_cursor: null, has_more: false },
        }),
    });
    return (
      <StoryProviders>
        <ContactsScreen />
      </StoryProviders>
    );
  },
};

// A never-resolving GET is the house pattern for a pending story: the query
// never settles, so ListTable stays on its skeleton rows rather than racing
// a timer against Storybook's own render.
export const ContactsListLoading: Story = {
  render: () => {
    installFetchStub({
      "GET /people": () => new Promise<Response>(() => undefined),
    });
    return (
      <StoryProviders>
        <ContactsScreen />
      </StoryProviders>
    );
  },
};

// A non-2xx body is what useListQuery's isError branch renders: the problem
// detail plus the retry button (listquery.tsx's `problem` slot), never a
// thrown exception the story would surface as a broken render instead.
export const ContactsListFailed: Story = {
  render: () => {
    installFetchStub({
      "GET /people": () =>
        jsonResponse(
          { title: "Forbidden", detail: "missing scope people:read" },
          403,
        ),
    });
    return (
      <StoryProviders>
        <ContactsScreen />
      </StoryProviders>
    );
  },
};

// has_more: true with a next_cursor is what lights the pager's "next" button
// even though only one page has loaded yet (design-system/listtable.tsx's
// Pager: `disabled={current === lastPage && !hasMore}`), so the load-more
// affordance is that button, not a separate control.
export const ContactsListMorePages: Story = {
  render: () => {
    installFetchStub({
      "GET /people": () =>
        jsonResponse({
          data: [anna],
          page: { next_cursor: "cursor-2", has_more: true },
        }),
    });
    return (
      <StoryProviders>
        <ContactsScreen />
      </StoryProviders>
    );
  },
};

// A row whose archived_at is set: the warn badge next to the name
// (people.tsx's name column) renders off the row's own field, independent
// of the includeArchived toggle's checked state. The checkbox itself starts
// unchecked every render (useListQuery seeds includeArchived: false with no
// prop to override it) and only flips on click, so this story shows the
// badge a toggled-on list would surface without claiming the checkbox is
// lit.
const archivedContact: Person = {
  id: "p-2",
  full_name: "Mara Voss",
  title: "Former Head of Ops",
  // Every field the contract makes required, not just the ones the row
  // happens to paint: a partial email object still satisfies the response
  // stub, whose body is `unknown`, so the shape only fails where it is
  // typed. Typing it here is what catches the omission at all.
  emails: [
    {
      id: "e-2",
      email: "mara.voss@brandt.example",
      email_type: "work",
      is_primary: true,
      position: 0,
      source: "manual",
      captured_by: "human:u1",
    },
  ],
  captured_by: "human:u1",
  source: "manual",
  version: 1,
  created_at: "2026-05-01T08:00:00Z",
  updated_at: "2026-07-01T08:00:00Z",
  archived_at: "2026-07-01T08:00:00Z",
};

export const ContactsListArchivedRow: Story = {
  render: () => {
    installFetchStub({
      "GET /people": () =>
        jsonResponse({
          data: [anna, archivedContact],
          page: { next_cursor: null, has_more: false },
        }),
    });
    return (
      <StoryProviders>
        <ContactsScreen />
      </StoryProviders>
    );
  },
};

// PersonScreen (the 360 view below) is UNROUTED DEAD CODE: App.tsx routes
// ContactsScreen for the list and PersonPageV2 for the record, never this
// component. Left in place rather than expanded or deleted so a future
// reader does not mistake it for a live surface.
export const PersonOverview: Story = {
  render: () => {
    installFetchStub({
      "GET /me": meRoute({ person: ["read", "update"] }),
      "GET /people/p-1": () => jsonResponse(anna),
      "GET /people/p-1/strength": () => jsonResponse(dormantStrength),
      "GET /activities": () => jsonResponse({ data: [] }),
      "GET /records/person/p-1/context": () =>
        jsonResponse({ anchor: { type: "person", id: "p-1" }, sections: [] }),
    });
    return (
      <StoryProviders>
        <PersonScreen id="p-1" />
      </StoryProviders>
    );
  },
};
