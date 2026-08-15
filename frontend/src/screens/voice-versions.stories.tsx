// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";
import { VoiceHistory } from "./voice-versions";

// Every build this profile has had, and what each one changed. A `candidate`
// is a finished build waiting for a human to accept or reject it — the state
// that makes this surface actionable rather than a log.
const ACTIVE = {
  id: "vv-3",
  profile_version: 3,
  status: "active",
  created_at: "2026-07-30T10:00:00Z",
};

const CANDIDATE = {
  id: "vv-4",
  profile_version: 4,
  status: "candidate",
  created_at: "2026-08-12T09:15:00Z",
};

const DELTA = {
  id: "vd-1",
  from_version: 3,
  to_version: 4,
  classification: "refinement",
  activation_outcome: "pending",
};

function story(
  versions: Record<string, unknown>[],
  canEdit: boolean,
  deltas: Record<string, unknown>[] = [DELTA],
) {
  return () => {
    installFetchStub({
      "GET /me": meRoute({ voice_profile: ["read", "update"] }),
      "GET /voice-profiles/vp-1/versions": () =>
        jsonResponse({ data: versions }),
      "GET /voice-profiles/vp-1/deltas": () => jsonResponse({ data: deltas }),
    });
    return (
      <StoryProviders>
        <VoiceHistory
          profileId="vp-1"
          canEdit={canEdit}
          onChanged={() => undefined}
        />
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof VoiceHistory> = {
  title: "Settings/You/Writing voice/Build history",
  component: VoiceHistory,
};
export default meta;
type Story = StoryObj<typeof VoiceHistory>;

export const CandidateWaiting: Story = {
  render: story([CANDIDATE, ACTIVE], true),
};

export const OnlyActive: Story = {
  render: story([ACTIVE], true, []),
};

// The history is a read this seat keeps; accepting or rolling back a build is
// not. The rows stay and the verbs go.
export const ReadOnly: Story = {
  render: story([CANDIDATE, ACTIVE], false),
};
