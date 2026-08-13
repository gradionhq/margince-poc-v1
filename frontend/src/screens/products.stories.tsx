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
  title: "Screens/Products",
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
// gated on product grants now. The stub's catch-all answers `GET /me` with an
// empty page, which resolves to a caller holding no grant at all — so without
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
