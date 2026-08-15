// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { installFetchStub, meRoute, StoryProviders } from "./story-utils";
import { VoiceCorpusIntake } from "./voice-corpus-settings";

// Where a writing sample arrives: pasted, or dropped as a file. The two forms it
// takes are `first` — the control that MINTS the profile, drawn taller and with
// its own label — and every one after it.
//
// A file drop cannot be performed from a story, so what is catalogued is the
// resting state of both. The label matters here: it is drawn by a `Field`, so
// the words above the box are its accessible name, which they were not when a
// div and an `aria-label` disagreed about what the box was called.
function story(first: boolean) {
  return () => {
    installFetchStub({
      "GET /me": meRoute({ voice_profile: ["read", "create", "update"] }),
    });
    return (
      <StoryProviders>
        <VoiceCorpusIntake
          first={first}
          profileId={first ? null : "vp-1"}
          onChanged={() => undefined}
        />
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
