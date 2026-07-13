// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { useState } from "react";
import { LocaleProvider } from "../i18n";
import {
  ListGate,
  type ListGateState,
  type ListQuery,
  ListToolbar,
} from "./listquery";

// ListToolbar and ListGate are both prop-driven (the screen owns the
// react-query wiring via useListQuery) — these stories exercise the toolbar
// in its searchable and non-searchable (partners-style) shapes, plus the
// ListGate state ladder (loading/error/empty/loaded) off a hand-built state
// object rather than a live query.
const meta: Meta = {
  title: "Screens/ListQuery",
  parameters: { layout: "padded" },
  decorators: [
    (Story) => (
      <LocaleProvider initial="en">
        <Story />
      </LocaleProvider>
    ),
  ],
};
export default meta;

type Story = StoryObj;

function ToolbarDemo({
  searchable,
  showArchivedToggle,
  withFilters,
}: Readonly<{
  searchable?: boolean;
  showArchivedToggle?: boolean;
  withFilters?: boolean;
}>) {
  const [query, setQuery] = useState<ListQuery>({
    q: "",
    sort: "-created_at",
    includeArchived: false,
    filters: {},
  });
  return (
    <ListToolbar
      query={query}
      setQuery={setQuery}
      sortOptions={[
        { value: "-created_at", label: "list.sortNewest" },
        { value: "full_name", label: "people.name" },
      ]}
      searchable={searchable}
      showArchivedToggle={showArchivedToggle}
      filters={
        withFilters
          ? [
              {
                kind: "select",
                key: "status",
                label: "lead.filterStatus",
                options: [
                  { value: "new", label: "lead.statusNew" },
                  { value: "working", label: "lead.statusWorking" },
                ],
              },
            ]
          : undefined
      }
    />
  );
}

export const SearchableWithFilters: Story = {
  render: () => <ToolbarDemo withFilters />,
};

// The partners-style toolbar: no search field, no archived toggle — only
// select filters, since GET /partners has no `q` param.
export const NonSearchable: Story = {
  render: () => (
    <ToolbarDemo searchable={false} showArchivedToggle={false} withFilters />
  ),
};

function gateState(
  overrides: Partial<ListGateState<{ id: string; name: string }>>,
): ListGateState<{ id: string; name: string }> {
  return {
    rows: [],
    isPending: false,
    isError: false,
    error: null,
    refetch: () => undefined,
    hasMore: false,
    loadMore: () => undefined,
    ...overrides,
  };
}

export const GateLoading: Story = {
  render: () => (
    <ListGate state={gateState({ isPending: true })} empty="Nothing yet">
      {(rows) => <ul>{rows.map((row) => row.name)}</ul>}
    </ListGate>
  ),
};

export const GateError: Story = {
  render: () => (
    <ListGate
      state={gateState({
        isError: true,
        error: new Error("missing scope people:read"),
      })}
      empty="Nothing yet"
    >
      {(rows) => <ul>{rows.map((row) => row.name)}</ul>}
    </ListGate>
  ),
};

export const GateEmpty: Story = {
  render: () => (
    <ListGate state={gateState({})} empty="Nothing yet">
      {(rows) => <ul>{rows.map((row) => row.name)}</ul>}
    </ListGate>
  ),
};

export const GateLoaded: Story = {
  render: () => (
    <ListGate
      state={gateState({
        rows: [
          { id: "p-1", name: "Anna Weber" },
          { id: "p-2", name: "Otto Fischer" },
        ],
        hasMore: true,
      })}
      empty="Nothing yet"
    >
      {(rows) => (
        <ul>
          {rows.map((row) => (
            <li key={row.id}>{row.name}</li>
          ))}
        </ul>
      )}
    </ListGate>
  ),
};
