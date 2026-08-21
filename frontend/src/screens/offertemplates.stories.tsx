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
  title: "Settings/Admin settings/Data model/Offer templates",
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
// gated on offer template grants now. The stub REFUSES to answer an unrouted `GET /me`
// rather than guessing one — so without
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

// The list at 390px, which is the one width ListTable's own devices all have to
// answer at once. `.lt-head` is a non-wrapping row holding the count and the New
// action; the locale filter chip sits under it; and the table itself is
// `table-layout: fixed` with a `--lt-floor` min-width, so past that floor the
// BODY scrolls sideways under a stuck header while the pinned name column casts
// its shadow over what passes beneath. What to check is that the sideways scroll
// stays inside `.lt-scroll` — the surface may scroll, the page may not — and that
// the columns clip with ellipses rather than the row growing to fit.
//
// Storybook applies the viewport from the MANAGER, by resizing the preview
// iframe — so the fe-uat capture, which loads a bare iframe.html, renders this at
// the harness's own width and its PNG is NOT a picture of a phone. Review it in
// Storybook, or by narrowing the browser.
export const ListPhone: Story = {
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
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
