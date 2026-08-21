// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { Panel, PanelBody } from "../design-system/panel";
import { installFetchStub, meRoute, StoryProviders } from "./story-utils";
import { VoiceCorpusIntake } from "./voice-corpus-settings";

// Where a writing sample arrives: pasted, or dropped as a file. The two forms it
// takes are `first` — the control that MINTS the profile, whose verb leads — and
// every one after it.
//
// The resting state is a ROW: what the card is asking for on the left, and the
// two ways in on the right. The paste box itself is a form, so it lives in the
// dialog the first verb opens; `PasteDialog` below is that state.
//
// A file drop cannot be performed from a story, so what is catalogued is the
// resting state of both. The label matters here: inside the dialog it is drawn
// by a `Field` AND names the dialog, so the words above the box are its
// accessible name — which they were not when a div and an `aria-label`
// disagreed about what the box was called.
function story(first: boolean) {
  return () => {
    installFetchStub({
      "GET /me": meRoute({ voice_profile: ["read", "create", "update"] }),
    });
    return (
      <StoryProviders>
        {/* In the card the row sits in a `SettingList` beside the manifest;
            here the panel stands in for that card so the row is drawn on the
            ground it actually lands on. */}
        <Panel title="Writing samples">
          <PanelBody>
            <VoiceCorpusIntake
              first={first}
              profileId={first ? null : "vp-1"}
              onChanged={() => undefined}
            />
          </PanelBody>
        </Panel>
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof VoiceCorpusIntake> = {
  title: "Settings/You/Writing voice/Corpus intake",
  component: VoiceCorpusIntake,
};
export default meta;
type Story = StoryObj<typeof VoiceCorpusIntake>;

export const FirstSample: Story = { render: story(true) };

export const AnotherSample: Story = { render: story(false) };

/** The form behind the verb: one box, its own label, and the verb that commits
 * the sample rather than the row's verb that opened the form. */
export const PasteDialog: Story = {
  render: story(false),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: "Paste writing" }),
    );
  },
};
