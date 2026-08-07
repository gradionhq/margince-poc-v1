// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { ListColumn } from "../design-system/listtable";
import {
  type FilterSpec,
  type ListPage,
  type ListQuery,
  ListTable,
  useListQuery,
  type ViewSpec,
} from "./listquery";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// ListTable (screens/listquery.tsx) binds useListQuery's server-query state
// to the design-system list surface (ListTable in listtable.tsx, imported
// there under the alias ListSurface). It also reads the shared workspace
// mode off the cached ["me"] query — to drop the sort/filter dials the
// overlay mirror refuses — so every story needs the same QueryClientProvider
// + stubbed /me every screen story provides, not just LocaleProvider.
function nativeMe() {
  return () =>
    jsonResponse({
      user: { id: "u1", email: "ada@acme.test", display_name: "Ada" },
      roles: ["admin"],
      teams: [],
    });
}

const meta: Meta = {
  title: "Screens/ListQuery",
  parameters: { layout: "padded" },
  decorators: [
    (Story) => {
      installFetchStub({ "GET /me": nativeMe() });
      return (
        <StoryProviders>
          <Story />
        </StoryProviders>
      );
    },
  ],
};
export default meta;

type Story = StoryObj;

type Row = { id: string; name: string; region: string };

const columns: readonly ListColumn<Row>[] = [
  {
    key: "name",
    header: "Name",
    cell: (row) => row.name,
    sort: "full_name",
    fixed: true,
  },
  { key: "region", header: "Region", cell: (row) => row.region },
];

function rows(count: number, offset = 0): Row[] {
  return Array.from({ length: count }, (_, index) => {
    const n = offset + index + 1;
    return {
      id: `r${n}`,
      name: `Contact ${String(n).padStart(2, "0")}`,
      region: n % 2 === 0 ? "EU" : "US",
    };
  });
}

function pageOf(data: Row[], hasMore = false): ListPage<Row> {
  return {
    data,
    page: { next_cursor: hasMore ? "next" : null, has_more: hasMore },
  };
}

function Harness({
  fetchPage,
  chips,
  views,
}: Readonly<{
  fetchPage: (
    query: ListQuery,
    cursor: string | null,
  ) => Promise<ListPage<Row>>;
  chips?: readonly FilterSpec[];
  views?: readonly ViewSpec[];
}>) {
  const state = useListQuery<Row>({
    key: "story-list",
    initialSort: "-created_at",
    fetchPage,
  });
  return (
    <ListTable
      state={state}
      unit="unit.contacts"
      columns={columns}
      rowKey={(row) => row.id}
      chips={chips}
      views={views}
    />
  );
}

export const Loaded: Story = {
  render: () => <Harness fetchPage={async () => pageOf(rows(3))} />,
};

export const Pending: Story = {
  render: () => (
    <Harness fetchPage={() => new Promise<ListPage<Row>>(() => {})} />
  ),
};

export const ErrorState: Story = {
  render: () => (
    <Harness
      fetchPage={async () => {
        throw new Error("missing scope people:read");
      }}
    />
  ),
};

export const Empty: Story = {
  render: () => <Harness fetchPage={async () => pageOf([])} />,
};

// The overlay mirror 422s the sort/filter dials, so the bound ListTable
// passes neither through and shows the note explaining why instead.
export const OverlayDialsUnavailable: Story = {
  decorators: [
    (Story) => {
      installFetchStub({
        "GET /me": () =>
          jsonResponse({
            user: { id: "u1", email: "ada@acme.test", display_name: "Ada" },
            roles: ["admin"],
            teams: [],
            system_of_record: { mode: "overlay" },
          }),
      });
      return <Story />;
    },
  ],
  render: () => <Harness fetchPage={async () => pageOf(rows(3))} />,
};

// Views, a filter chip and a keyset cursor with more than one page: the
// server-side fixture the earlier scenarios don't need, all three
// together.
export const ViewsChipsAndPaging: Story = {
  render: () => (
    <Harness
      fetchPage={async (query, cursor) => {
        const all = rows(60).filter(
          (row) => !query.filters.region || row.region === query.filters.region,
        );
        const from = cursor ? Number(cursor) : 0;
        const size = 25;
        const slice = all.slice(from, from + size);
        const hasMore = from + size < all.length;
        return {
          data: slice,
          page: {
            next_cursor: hasMore ? String(from + size) : null,
            has_more: hasMore,
          },
        };
      }}
      chips={[
        {
          key: "region",
          label: "lead.filterStatus",
          allLabel: "lead.filterStatusAll",
          options: [
            { value: "EU", label: "lead.statusNew" },
            { value: "US", label: "lead.statusWorking" },
          ],
        },
      ]}
      views={[
        { label: "list.viewAll" },
        { label: "list.viewAZ", sort: "full_name", filters: { region: "EU" } },
      ]}
    />
  ),
};
