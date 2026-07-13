// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { useQuery } from "@tanstack/react-query";
import { QueryGate } from "./common";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// QueryGate is the shared loading/error/empty/loaded ladder every screen's
// detail read renders through — exercised here off a small demo query
// against the shared fetch stub, one story per state, rather than a
// hand-built UseQueryResult (react-query's result shape isn't meant to be
// constructed by hand).
const meta: Meta = {
  title: "Screens/Common",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

function Demo({ empty }: Readonly<{ empty?: boolean }>) {
  const query = useQuery({
    queryKey: ["story-demo"],
    queryFn: async () => {
      const response = await fetch("/v1/story-demo");
      const body = (await response.json()) as { name: string } | null;
      if (!response.ok) {
        throw new Error("request failed");
      }
      return body;
    },
  });
  return (
    <QueryGate query={query} empty={empty ? () => true : undefined}>
      {(data) => <p>{data?.name}</p>}
    </QueryGate>
  );
}

export const Pending: Story = {
  render: () => {
    installFetchStub({
      "GET /story-demo": () => new Promise<Response>(() => undefined),
    });
    return (
      <StoryProviders>
        <Demo />
      </StoryProviders>
    );
  },
};

export const ErrorState: Story = {
  render: () => {
    installFetchStub({
      "GET /story-demo": () =>
        jsonResponse(
          { title: "Forbidden", detail: "missing scope people:read" },
          403,
        ),
    });
    return (
      <StoryProviders>
        <Demo />
      </StoryProviders>
    );
  },
};

export const EmptyState: Story = {
  render: () => {
    installFetchStub({
      "GET /story-demo": () => jsonResponse({ name: "unused" }),
    });
    return (
      <StoryProviders>
        <Demo empty />
      </StoryProviders>
    );
  },
};

export const Loaded: Story = {
  render: () => {
    installFetchStub({
      "GET /story-demo": () => jsonResponse({ name: "Anna Weber" }),
    });
    return (
      <StoryProviders>
        <Demo />
      </StoryProviders>
    );
  },
};
