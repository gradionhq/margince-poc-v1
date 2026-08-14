import type { Meta, StoryObj } from "@storybook/react-vite";
import { meFixture } from "../app/mefixture";
import { OfferTemplatesAdmin } from "./offertemplates";
import {
  emptyPage,
  installFetchStub,
  jsonResponse,
  StoryProviders,
} from "./story-utils";

const meta: Meta = {
  title: "Settings/Organization/Data model/Offer templates",
  parameters: { layout: "padded" },
};
export default meta;
type Story = StoryObj;

const template = {
  id: "t-1",
  name: "Standard DE",
  locale: "de-DE",
  is_default: true,
  layout: {},
  version: 1,
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-06-01T08:00:00Z",
};

// Every story here needs a principal, because the screen's write affordances are
// gated on offer template grants now. The stub's catch-all answers `GET /me` with an
// empty page, which resolves to a caller holding no grant at all — so without
// this the whole catalog captured the read-only posture and no story showed the
// editor. Named once rather than repeated per story.
const AUTHORING_ME = () =>
  jsonResponse(
    meFixture({
      allow: { offer_template: ["read", "create", "update", "delete"] },
    }),
  );

export const List: Story = {
  render: () => {
    installFetchStub({
      "GET /me": AUTHORING_ME,
      "GET /offer-templates": () =>
        jsonResponse({
          data: [template],
          page: { next_cursor: null, has_more: false },
        }),
    });
    return (
      <StoryProviders>
        <OfferTemplatesAdmin />
      </StoryProviders>
    );
  },
};
export const Empty: Story = {
  render: () => {
    installFetchStub({
      "GET /me": AUTHORING_ME,
      "GET /offer-templates": () => jsonResponse(emptyPage),
    });
    return (
      <StoryProviders>
        <OfferTemplatesAdmin />
      </StoryProviders>
    );
  },
};
export const LoadError: Story = {
  render: () => {
    installFetchStub({
      "GET /me": AUTHORING_ME,
      "GET /offer-templates": () =>
        jsonResponse(
          { title: "server error", detail: "offer templates unavailable" },
          500,
        ),
    });
    return (
      <StoryProviders>
        <OfferTemplatesAdmin />
      </StoryProviders>
    );
  },
};
