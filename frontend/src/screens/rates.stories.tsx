// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { meFixture } from "../app/mefixture";
import { FxRatesCard, ModelCostsCard } from "./rates";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// READ before create. Both cards gate on `useCan(<object>, "read")` and skip
// their query entirely without it, so a create-only fixture reached the withheld
// body — and these two stories captured "only an admin or ops can see this"
// under names promising a populated price sheet, with the fixtures below as
// dead code. The predicate a surface opens on is the one a story has to hold.
function admin() {
  return () =>
    jsonResponse(
      meFixture({
        allow: {
          fx_rate: ["read", "create"],
          ai_model_rate: ["read", "create"],
        },
      }),
    );
}

const FX = {
  data: [
    {
      from_currency: "USD",
      to_currency: "EUR",
      rate: "0.9200000000",
      effective_date: "2026-07-23",
    },
    {
      from_currency: "GBP",
      to_currency: "EUR",
      rate: "1.1700000000",
      effective_date: "2026-07-01",
    },
  ],
};

const MODELS = {
  data: [
    {
      provider: "anthropic",
      model_id: "claude-opus-4-8",
      input_per_mtok: "5",
      output_per_mtok: "25",
      cache_read_per_mtok: "0.5",
      cache_write_per_mtok: "6.25",
      effective_date: "2026-07-23",
    },
    {
      provider: "gemini",
      model_id: "gemini-3.5-flash",
      input_per_mtok: "1.5",
      output_per_mtok: "9",
      cache_read_per_mtok: "0.15",
      cache_write_per_mtok: "0",
      effective_date: "2026-07-23",
    },
  ],
};

// The two sheets no longer share a page — FX rates sit under Organization, next
// to the base currency they convert to, and model prices under AI, next to the
// runtime they price. They are pictured together here because the fixture that
// feeds them is one backend, and a reader comparing the two shapes wants both in
// one frame.
function RateSheets() {
  return (
    <>
      <FxRatesCard />
      <ModelCostsCard />
    </>
  );
}

const meta: Meta<typeof RateSheets> = {
  title: "Settings/Admin settings/Rates and model costs",
  component: RateSheets,
};
export default meta;
type Story = StoryObj<typeof RateSheets>;

// An admin sees both price sheets populated, each with its "Set rate" /
// "Add model rate" affordance.
export const Populated: Story = {
  render: () => {
    installFetchStub({
      "GET /me": admin(),
      "GET /fx-rates": () => jsonResponse(FX),
      "GET /ai-model-rates": () => jsonResponse(MODELS),
    });
    return (
      <StoryProviders>
        <RateSheets />
      </StoryProviders>
    );
  },
};

// The reason the header action band was restructured on this branch, pictured.
// Beside the title, Refresh + "Set rate" were one unwrappable row sized to their
// max content: at 390px the pair measured 353px inside a 324px card and pushed
// the page 12px past the viewport. Both cards now put the pair in the panel's own
// wrapping action band, and this is the only render that can show it holds —
// nothing else in the catalogue draws either sheet below 1024px.
//
// No `layout` override: the canvas frame's 2rem gutter is what puts the card at
// the ~324px the regression was measured in.
export const PopulatedPhone: Story = {
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
  render: () => {
    installFetchStub({
      "GET /me": admin(),
      "GET /fx-rates": () => jsonResponse(FX),
      "GET /ai-model-rates": () => jsonResponse(MODELS),
    });
    return (
      <StoryProviders>
        <RateSheets />
      </StoryProviders>
    );
  },
};

// A fresh workspace: both sheets empty, the honest empty states render.
export const Empty: Story = {
  render: () => {
    installFetchStub({
      "GET /me": admin(),
      "GET /fx-rates": () => jsonResponse({ data: [] }),
      "GET /ai-model-rates": () => jsonResponse({ data: [] }),
    });
    return (
      <StoryProviders>
        <RateSheets />
      </StoryProviders>
    );
  },
};

// A reader with the read grant and no write verb: the sheet, the row that names
// it, and one caption saying why nothing here can be changed. The panel draws no
// action band at all — a withheld write affordance inside a readable surface is
// absent, not disabled, because the surface has already said what this is.
export const ReadOnly: Story = {
  render: () => {
    installFetchStub({
      "GET /me": () =>
        jsonResponse(
          meFixture({ allow: { fx_rate: ["read"], ai_model_rate: ["read"] } }),
        ),
      "GET /fx-rates": () => jsonResponse(FX),
      "GET /ai-model-rates": () => jsonResponse(MODELS),
    });
    return (
      <StoryProviders>
        <RateSheets />
      </StoryProviders>
    );
  },
};

/** Open a dialog the way the reader does: the sheets hold the open state
 *  themselves, so the frame has to press the verb in the action band. */
function openDialog(name: string) {
  return async ({ canvasElement }: { canvasElement: HTMLElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name }));
  };
}

// Setting a currency rate: three boxes submitted together, so they live behind
// the verb rather than on the card, and each one is a `Field` — the label above
// its control, the id owned by the field, the save row right-aligned at the
// bottom.
export const SetRateDialog: Story = {
  name: "Set rate dialog",
  play: openDialog("Set rate"),
  render: () => {
    installFetchStub({
      "GET /me": admin(),
      "GET /fx-rates": () => jsonResponse(FX),
      "GET /ai-model-rates": () => jsonResponse(MODELS),
    });
    return (
      <StoryProviders>
        <RateSheets />
      </StoryProviders>
    );
  },
};

// The model price dialog, which asks the same question seven times over — the
// case the shared `Field` spelling was worth having, and the tallest form either
// sheet opens.
export const ModelPriceDialog: Story = {
  name: "Model price dialog",
  play: openDialog("Add model rate"),
  render: () => {
    installFetchStub({
      "GET /me": admin(),
      "GET /fx-rates": () => jsonResponse(FX),
      "GET /ai-model-rates": () => jsonResponse(MODELS),
    });
    return (
      <StoryProviders>
        <RateSheets />
      </StoryProviders>
    );
  },
};
