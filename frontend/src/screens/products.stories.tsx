import type { Meta, StoryObj } from "@storybook/react-vite";
import { ProductsScreen } from "./products";
import {
  emptyPage,
  installFetchStub,
  jsonResponse,
  StoryProviders,
} from "./story-utils";

const meta: Meta = {
  title: "Screens/Products",
  parameters: { layout: "padded" },
};
export default meta;
type Story = StoryObj;

const product = {
  id: "p-1",
  workspace_id: "w",
  name: "Consulting Day",
  sku: "CONS-DAY",
  unit: "day",
  unit_price_minor: 150000,
  currency: "EUR",
  default_tax_rate: 19,
  active: true,
  source: "manual",
  captured_by: "human:u1",
  version: 1,
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-06-01T08:00:00Z",
};

export const List: Story = {
  render: () => {
    installFetchStub({
      "GET /products": () =>
        jsonResponse({
          data: [product],
          page: { next_cursor: null, has_more: false },
        }),
    });
    return (
      <StoryProviders>
        <ProductsScreen />
      </StoryProviders>
    );
  },
};
export const Empty: Story = {
  render: () => {
    installFetchStub({ "GET /products": () => jsonResponse(emptyPage) });
    return (
      <StoryProviders>
        <ProductsScreen />
      </StoryProviders>
    );
  },
};
export const LoadError: Story = {
  render: () => {
    installFetchStub({
      "GET /products": () =>
        jsonResponse(
          { title: "server error", detail: "products unavailable" },
          500,
        ),
    });
    return (
      <StoryProviders>
        <ProductsScreen />
      </StoryProviders>
    );
  },
};
