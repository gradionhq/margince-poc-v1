// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { LocaleProvider } from "../i18n";
import { AskFab } from "./fab";

// The "Ask Margince" FAB and its anchored panel. It reads nothing from the
// server — the context line is derived from the `route` prop against NAV — so
// LocaleProvider is the whole context it needs: no query client, no fetch stub,
// and no route for a story to forget.
//
// The panel's open state is component-local (there is no `startOpen` prop), so
// the open stories drive the trigger through play(), the way
// screens/archive.stories.tsx drives its confirm modal. fe-uat gives a play-fn
// story a longer settle before it captures the frame.
//
// Deliberately NOT covered: the screens where AskFab renders null (the full Ask
// surface, and every RAIL_LESS_SCREEN). Returning null is correct there, but a
// story whose root stays empty fails the capture gate as a render error — the
// gate would read a correct refusal as a broken component. `nav.test.ts` holds
// that branch instead.
const meta: Meta<typeof AskFab> = {
  title: "Shell/Ask FAB",
  component: AskFab,
  decorators: [
    (Story) => (
      <LocaleProvider initial="en">
        <Story />
      </LocaleProvider>
    ),
  ],
};
export default meta;
type Story = StoryObj<typeof AskFab>;

async function openPanel({ canvasElement }: { canvasElement: HTMLElement }) {
  const canvas = within(canvasElement);
  await userEvent.click(canvas.getByRole("button", { name: "Ask about this" }));
}

// The resting state: one round trigger pinned bottom-right, nothing else drawn.
export const Closed: Story = { args: { route: { screen: "deals" } } };

// Opened on a list screen, so the context line names the nav destination the
// reader is standing on.
export const OpenOnScreen: Story = {
  args: { route: { screen: "deals" } },
  play: openPanel,
};

// Opened on a record: `route.id` is the context, printed verbatim — the panel
// names the record rather than the list it was opened from. The scope line
// below it is load-bearing copy (the agent reads only the RBAC ∩ Passport
// intersection), so it is in every open frame on purpose.
export const OpenOnRecord: Story = {
  args: { route: { screen: "contacts", id: "p-1042" } },
  play: openPanel,
};

// Dark, because three surfaces here are drawn from tokens that move between
// themes and have to stay three different things: the --ai trigger, the card
// the panel stands on, and the tinted scope line inside it. The trigger is the
// one to watch — it is the only accent-filled control on the page, and it
// carries --textOnAccent rather than the page ink.
export const OpenOnScreenDark: Story = {
  globals: { theme: "dark" },
  args: { route: { screen: "deals" } },
  play: openPanel,
};
