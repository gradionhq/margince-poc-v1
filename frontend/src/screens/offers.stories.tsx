import type { Meta, StoryObj } from "@storybook/react-vite";
import { OfferScreen } from "./offers";
import {
  emptyPage,
  installFetchStub,
  jsonResponse,
  StoryProviders,
} from "./story-utils";

const meta: Meta = {
  title: "Screens/Offers",
  parameters: { layout: "padded" },
};
export default meta;
type Story = StoryObj;

const consultingLine = {
  id: "li-1",
  position: 1,
  product_id: null,
  description: "Consulting hours",
  unit: "hour",
  quantity: 10,
  unit_price_minor: 10000,
  discount_pct: 0,
  tax_rate: 19,
  line_net_minor: 100000,
  line_tax_minor: 19000,
  line_total_minor: 119000,
  evidence: null,
  price_grounded: true,
  version: 1,
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-06-01T08:00:00Z",
};

const draftOffer = {
  id: "o-1",
  workspace_id: "w",
  deal_id: "d-1",
  offer_number: "ANG-2026-0007",
  revision: 1,
  status: "draft",
  currency: "EUR",
  buyer_org_id: null,
  valid_until: "2026-08-01",
  intro_text: null,
  terms_text: null,
  net_minor: 100000,
  tax_minor: 19000,
  gross_minor: 119000,
  template_id: null,
  line_items: [consultingLine],
  source: "manual",
  captured_by: "human:u1",
  version: 1,
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-06-01T08:00:00Z",
};

export const Draft: Story = {
  render: () => {
    installFetchStub({
      "GET /offers/o-1": () => jsonResponse(draftOffer),
      "GET /offer-templates": () => jsonResponse(emptyPage),
      "GET /products": () => jsonResponse(emptyPage),
    });
    return (
      <StoryProviders>
        <OfferScreen id="o-1" />
      </StoryProviders>
    );
  },
};

// An AI-proposed line whose price couldn't be grounded: unit_price_minor is
// the honest 0 sentinel, never rendered as a real €0.00 price (P11).
export const DraftUnpricedLine: Story = {
  render: () => {
    installFetchStub({
      "GET /offers/o-1": () =>
        jsonResponse({
          ...draftOffer,
          line_items: [
            consultingLine,
            {
              ...consultingLine,
              id: "li-2",
              position: 2,
              description: "Bespoke integration (AI-proposed)",
              unit_price_minor: 0,
              line_net_minor: 0,
              line_tax_minor: 0,
              line_total_minor: 0,
              price_grounded: false,
              version: 1,
            },
          ],
        }),
      "GET /offer-templates": () => jsonResponse(emptyPage),
      "GET /products": () => jsonResponse(emptyPage),
    });
    return (
      <StoryProviders>
        <OfferScreen id="o-1" />
      </StoryProviders>
    );
  },
};

export const Sent: Story = {
  render: () => {
    installFetchStub({
      "GET /offers/o-1": () =>
        jsonResponse({ ...draftOffer, status: "sent", revision: 2 }),
      "GET /offer-templates": () => jsonResponse(emptyPage),
    });
    return (
      <StoryProviders>
        <OfferScreen id="o-1" />
      </StoryProviders>
    );
  },
};

export const LoadError: Story = {
  render: () => {
    installFetchStub({
      "GET /offers/o-1": () =>
        jsonResponse(
          { title: "server error", detail: "offer unavailable" },
          500,
        ),
    });
    return (
      <StoryProviders>
        <OfferScreen id="o-1" />
      </StoryProviders>
    );
  },
};
