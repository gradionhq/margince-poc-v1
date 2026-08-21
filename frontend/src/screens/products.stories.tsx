import type { Meta, StoryObj } from "@storybook/react-vite";
import { meFixture } from "../app/mefixture";
import { ProductsAdmin } from "./products";
import {
  emptyPage,
  installFetchStub,
  jsonResponse,
  StoryProviders,
} from "./story-utils";

const meta: Meta = {
  title: "Settings/Admin settings/Data model/Products",
  parameters: { layout: "padded" },
};
export default meta;
type Story = StoryObj;

const product = {
  id: "p-1",
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

// Every story here needs a principal, because the screen's write affordances are
// gated on product grants now. The stub REFUSES to answer an unrouted `GET /me`
// rather than guessing one — so without
// this the whole catalog captured the read-only posture and no story showed the
// editor. Named once rather than repeated per story.
const AUTHORING_ME = () =>
  jsonResponse(
    meFixture({ allow: { product: ["read", "create", "update", "delete"] } }),
  );

export const List: Story = {
  render: () => {
    installFetchStub({
      "GET /me": AUTHORING_ME,
      "GET /products": () =>
        jsonResponse({
          data: [product],
          page: { next_cursor: null, has_more: false },
        }),
    });
    return (
      <StoryProviders>
        <ProductsAdmin />
      </StoryProviders>
    );
  },
};
// The catalogue table at 390px. Every settings page takes the whole page column,
// which is what a table needs and what a phone has none of. Seven values per row (name, SKU, unit, price, tax, active, the row verbs) have
// to end up either wrapped or inside a scroller, and no story has drawn this
// table narrow enough to say which.
export const ListPhone: Story = {
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
  render: () => {
    installFetchStub({
      "GET /me": AUTHORING_ME,
      "GET /products": () =>
        jsonResponse({
          data: [product],
          page: { next_cursor: null, has_more: false },
        }),
    });
    return (
      <StoryProviders>
        <ProductsAdmin />
      </StoryProviders>
    );
  },
};
export const Empty: Story = {
  render: () => {
    installFetchStub({
      "GET /me": AUTHORING_ME,
      "GET /products": () => jsonResponse(emptyPage),
    });
    return (
      <StoryProviders>
        <ProductsAdmin />
      </StoryProviders>
    );
  },
};
export const LoadError: Story = {
  render: () => {
    installFetchStub({
      "GET /me": AUTHORING_ME,
      "GET /products": () =>
        jsonResponse(
          { title: "server error", detail: "products unavailable" },
          500,
        ),
    });
    return (
      <StoryProviders>
        <ProductsAdmin />
      </StoryProviders>
    );
  },
};
