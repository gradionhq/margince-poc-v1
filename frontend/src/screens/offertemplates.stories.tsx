import type { Meta, StoryObj } from "@storybook/react";
import { OfferTemplatesScreen } from "./offertemplates";
import {
  emptyPage,
  installFetchStub,
  jsonResponse,
  StoryProviders,
} from "./story-utils";

const meta: Meta = {
  title: "Screens/OfferTemplates",
  parameters: { layout: "padded" },
};
export default meta;
type Story = StoryObj;

const template = {
  id: "t-1",
  workspace_id: "w",
  name: "Standard DE",
  locale: "de-DE",
  is_default: true,
  layout: {},
  version: 1,
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-06-01T08:00:00Z",
};

export const List: Story = {
  render: () => {
    installFetchStub({
      "GET /offer-templates": () =>
        jsonResponse({
          data: [template],
          page: { next_cursor: null, has_more: false },
        }),
    });
    return (
      <StoryProviders>
        <OfferTemplatesScreen />
      </StoryProviders>
    );
  },
};
export const Empty: Story = {
  render: () => {
    installFetchStub({
      "GET /offer-templates": () => jsonResponse(emptyPage),
    });
    return (
      <StoryProviders>
        <OfferTemplatesScreen />
      </StoryProviders>
    );
  },
};
export const LoadError: Story = {
  render: () => {
    installFetchStub({
      "GET /offer-templates": () =>
        jsonResponse(
          { title: "server error", detail: "offer templates unavailable" },
          500,
        ),
    });
    return (
      <StoryProviders>
        <OfferTemplatesScreen />
      </StoryProviders>
    );
  },
};
